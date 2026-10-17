package sigmoidh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/litellm"
)

// Claude Code transcripts (real fixture shape) convert to resumable history.
func TestImportClaudeCode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	jsonl := `
{"type":"queue-operation","operation":"enqueue","content":"ignored"}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"fix the bug"}]},"cwd":"/tmp/proj"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"read main first"},{"type":"tool_use","id":"t1","name":"read","input":{"path":"main.go"}},{"type":"text","text":"On it."}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"contents"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Fixed."}]}}
{"type":"ai-title","title":"ignored"}
`
	if err := os.WriteFile(path, []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}
	_, name, n, err := importClaudeCode(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("entries = %d, want 4", n)
	}
	msgs, err := loadSessionMessages(filepath.Join(homeDir(), ".sigmoidh", "history", name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 { // user, assistant, tool, assistant
		t.Fatalf("loaded %d messages, want 4", len(msgs))
	}
	if msgs[1].Role != litellm.RoleAssistant {
		t.Fatalf("msg1 role %q", msgs[1].Role)
	}
	var sawReasoning, sawToolUse, sawText bool
	for _, blk := range msgs[1].Blocks {
		switch b := blk.(type) {
		case litellm.ReasoningBlock:
			sawReasoning = b.Text == "read main first"
		case litellm.ToolUseBlock:
			sawToolUse = b.Name == "read" && b.ID == "t1"
		case litellm.TextBlock:
			sawText = b.Text == "On it."
		}
	}
	if !sawReasoning || !sawToolUse || !sawText {
		t.Fatalf("assistant blocks wrong (reasoning=%t tool=%t text=%t)", sawReasoning, sawToolUse, sawText)
	}
	if msgs[2].Role != litellm.RoleTool {
		t.Fatalf("msg2 role %q", msgs[2].Role)
	}
}

// Session names with source suffixes stay resumable.
func TestParseSessionNameWithSourceSuffix(t *testing.T) {
	if _, ok := parseSessionName("20260824-005051-claude"); !ok {
		t.Fatal("suffixed name not parsed")
	}
	if _, ok := parseSessionName("20260824-005051"); !ok {
		t.Fatal("plain name not parsed")
	}
	if _, ok := parseSessionName("garbage"); ok {
		t.Fatal("garbage accepted")
	}
}

// Imported opencode transcripts keep text, reasoning, and tool I/O.
func TestImportOpenCodeRoundTrip(t *testing.T) {
	if _, err := os.Stat(filepath.Join(homeDir(), ".local", "share", "opencode", "opencode.db")); err != nil {
		t.Skip("no opencode database on this machine")
	}
	_, name, n, err := importOpenCode("")
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("imported 0 entries")
	}
	msgs, err := loadSessionMessages(filepath.Join(homeDir(), ".sigmoidh", "history", name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for _, m := range msgs {
		roles = append(roles, string(m.Role))
	}
	if !strings.Contains(strings.Join(roles, ","), "tool") {
		t.Fatalf("no tool messages in imported transcript: %v", roles)
	}
}
