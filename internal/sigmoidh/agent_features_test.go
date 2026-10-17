package sigmoidh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/litellm"
)

// --- compaction ---

func bigText(n int) string { return strings.Repeat("word ", n) }

func TestCompactKeepsTailAndAlternation(t *testing.T) {
	s := &Sigmoidh{stats: &RunStats{}, plan: newPlanStore(), observer: nopObserver{}, contextWindow: 4000}
	s.messages = []litellm.Message{litellm.System("SYS")}
	for i := 0; i < 6; i++ {
		s.messages = append(s.messages,
			litellm.UserText("request "+string(rune('A'+i))+" "+bigText(300)),
			litellm.Assistant(litellm.Text("answer "+bigText(300))))
	}
	before := len(s.messages)
	// no client: the summary falls back to the mechanical outline
	b, a, err := s.Compact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a >= b || len(s.messages) >= before {
		t.Fatalf("compaction did not shrink: %d→%d tokens, %d→%d msgs", b, a, before, len(s.messages))
	}
	if s.messages[0].Role != litellm.RoleSystem || s.messages[1].Role != litellm.RoleUser {
		t.Fatalf("want system then user summary, got %s, %s", s.messages[0].Role, s.messages[1].Role)
	}
	first := s.messages[1].Blocks[0].(litellm.TextBlock).Text
	if !strings.Contains(first, "[Context compacted") || !strings.Contains(first, "request A") {
		t.Fatalf("summary note missing or lost the early request:\n%.300s", first)
	}
	for i := 2; i < len(s.messages); i++ {
		if s.messages[i].Role == s.messages[i-1].Role {
			t.Fatalf("roles must alternate, %s twice at %d", s.messages[i].Role, i)
		}
	}
	last := s.messages[len(s.messages)-1].Blocks[0].(litellm.TextBlock).Text
	if !strings.HasPrefix(last, "answer") {
		t.Fatal("the most recent exchange must be kept verbatim")
	}
}

func TestCompactCutNeverStartsOnToolResult(t *testing.T) {
	msgs := []litellm.Message{
		litellm.System("SYS"),
		litellm.UserText(bigText(400)),
		litellm.Assistant(litellm.ToolUseBlock{ID: "1", Name: "read", Arguments: json.RawMessage(`{}`)}),
		{Role: litellm.RoleTool, Blocks: []litellm.Block{litellm.ToolResultBlock{ToolUseID: "1", Content: []litellm.Block{litellm.Text(bigText(400))}}}},
		litellm.Assistant(litellm.ToolUseBlock{ID: "2", Name: "read", Arguments: json.RawMessage(`{}`)}),
		{Role: litellm.RoleTool, Blocks: []litellm.Block{litellm.ToolResultBlock{ToolUseID: "2", Content: []litellm.Block{litellm.Text(bigText(400))}}}},
	}
	cut := compactCut(msgs, 50)
	if cut <= 1 || msgs[cut].Role == litellm.RoleTool {
		t.Fatalf("bad cut %d (role %s)", cut, msgs[cut].Role)
	}
}

func TestErrorClassifiers(t *testing.T) {
	if !isContextLengthError(errors.New("This model's maximum context length is 32768 tokens")) {
		t.Error("context length error not recognized")
	}
	if isContextLengthError(errors.New("rate limited")) {
		t.Error("rate limit misclassified")
	}
	if !isThinkingUnsupported(errors.New("openai: thinking is only supported for reasoning chat models")) {
		t.Error("thinking rejection not recognized")
	}
}

// --- AGENTS.md ---

func TestProjectInstructionsRootToLeaf(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	sub := filepath.Join(root, "svc", "api")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Run tests with ./test.sh"), 0o644)
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("ignored: AGENTS.md wins in the same dir"), 0o644)
	os.WriteFile(filepath.Join(sub, "CLAUDE.md"), []byte("API uses snake_case"), 0o644)

	got := projectInstructions(sub)
	i, j := strings.Index(got, "./test.sh"), strings.Index(got, "snake_case")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("want root then nested instructions:\n%s", got)
	}
	if strings.Contains(got, "ignored") {
		t.Fatalf("only one instruction file per directory:\n%s", got)
	}
}

// --- -json events ---

func TestJSONObserverEvents(t *testing.T) {
	var buf bytes.Buffer
	j := newJSONObserver(&buf)
	j.OnToolCall("read", json.RawMessage(`{"path":"a.go"}`))
	j.OnToolResult("read", "data", false, 0)
	j.OnAssistantText("hello")
	j.done("hello", &RunStats{TotalTurns: 2})

	var types []string
	for _, ln := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(ln), &ev); err != nil {
			t.Fatalf("not JSON: %q", ln)
		}
		types = append(types, ev["type"].(string))
		if ev["type"] == "tool_call" {
			if args, ok := ev["args"].(map[string]any); !ok || args["path"] != "a.go" {
				t.Fatalf("args should be a JSON object: %v", ev)
			}
		}
	}
	if strings.Join(types, ",") != "tool_call,tool_result,text,done" {
		t.Fatalf("events %v", types)
	}
}

// --- sessions listing ---

func TestSubstantiveRequestSkipsGreetings(t *testing.T) {
	for in, want := range map[string]string{
		"Hi":                          "",
		"hey there, can you fix it?":  "can you fix it?",
		"fix the login bug please":    "fix the login bug please",
		"Thanks! now add tests":       "now add tests",
		"Hello world program in rust": "world program in rust",
	} {
		if got := substantiveRequest(in); got != want {
			t.Errorf("substantiveRequest(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScanSessionCountsWork(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	path := writeFakeSession(t, dir, "20260822-100000.jsonl", []historyEntry{
		{Timestamp: "2026-08-22T10:00:00+05:30", Role: "user", Content: "Hi", Cwd: "/p"},
		{Timestamp: "2026-08-22T10:00:05+05:30", Role: "user", Content: "add a cache to the loader"},
		{Timestamp: "2026-08-22T10:01:00+05:30", Role: "assistant", ToolCalls: []historyToolCall{
			{ID: "1", Name: "edit", Args: json.RawMessage(`{"path":"a.go"}`)},
			{ID: "2", Name: "write", Args: json.RawMessage(`{"path":"b.go"}`)},
			{ID: "3", Name: "edit", Args: json.RawMessage(`{"path":"a.go"}`)},
			{ID: "4", Name: "read", Args: json.RawMessage(`{"path":"c.go"}`)},
		}},
	})
	info, err := scanSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Request != "add a cache to the loader" || info.Prompts != 2 || info.Tools != 4 || info.Files != 2 {
		t.Fatalf("info %+v", info)
	}
	if info.LastAt.IsZero() {
		t.Fatal("last activity not parsed")
	}
}

// --- providers ---

func TestProviderKeysNumberedAccounts(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "k0")
	t.Setenv("GROQ_API_KEY_ACC_10", "k10")
	t.Setenv("GROQ_API_KEY_ACC_2", "k2")
	t.Setenv("GROQ_API_KEY_ACC_3", "k0") // duplicate of the primary
	sp, _ := specFor("groq")
	if got := strings.Join(providerKeys(sp), ","); got != "k0,k2,k10" {
		t.Fatalf("keys %q, want primary then numeric order, deduped", got)
	}
	if _, ok := specFor("gemini"); !ok {
		t.Fatal("gemini provider missing")
	}
}

// A tool call with truncated JSON arguments must not poison the history:
// requests send {} in its place (clients reject invalid arguments).
func TestSanitizeRepairsInvalidToolArgs(t *testing.T) {
	msgs := []litellm.Message{
		litellm.UserText("hi"),
		litellm.Assistant(litellm.ToolUseBlock{ID: "1", Name: "edit", Arguments: json.RawMessage(`{"path":"a.go","old_str":"x`)}),
	}
	out := sanitizeForRequest(msgs)
	tu := out[1].Blocks[0].(litellm.ToolUseBlock)
	if string(tu.Arguments) != "{}" {
		t.Fatalf("args not repaired: %s", tu.Arguments)
	}
	orig := msgs[1].Blocks[0].(litellm.ToolUseBlock)
	if string(orig.Arguments) == "{}" {
		t.Fatal("the stored history must not be mutated")
	}
}
