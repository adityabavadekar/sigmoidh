package sigmoidh

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/litellm"
	"time"
)

func TestTriggerValidation(t *testing.T) {
	s := &Sigmoidh{stats: &RunStats{}}
	cases := []string{
		`{}`,
		`{"when":"later"}`,
		`{"when":"command_done"}`,
		`not json`,
	}
	for _, c := range cases {
		out, isErr := s.handleTrigger(context.Background(), json.RawMessage(c), ".")
		if !isErr {
			t.Fatalf("args %q must fail", c)
		}
		if strings.TrimSpace(out) == "" {
			t.Fatalf("args %q: empty error message", c)
		}
	}
}

func TestRunCompleteTriggerFiresOnce(t *testing.T) {
	var mu sync.Mutex
	var got []string
	old := notifyFn
	defer func() { notifyFn = old }()
	notifyFn = func(_, msg string) { mu.Lock(); got = append(got, msg); mu.Unlock() }

	s := &Sigmoidh{stats: &RunStats{}, observer: nopObserver{}}
	out, isErr := s.handleTrigger(context.Background(), json.RawMessage(`{"when":"run_complete","message":"build done"}`), ".")
	if isErr || !strings.Contains(out, "run completes") {
		t.Fatalf("register failed: %q %v", out, isErr)
	}
	if len(s.triggers) != 1 || s.triggers[0] != "build done" {
		t.Fatalf("trigger not stored: %v", s.triggers)
	}

	s.fireRunCompleteTriggers()
	s.fireRunCompleteTriggers() // drained: must not double-fire
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "build done" {
		t.Fatalf("notifications wrong: %v", got)
	}
	if len(s.triggers) != 0 {
		t.Fatal("triggers must be cleared after firing")
	}
}

func TestTriggerCommandDoneUnknownSession(t *testing.T) {
	s := &Sigmoidh{stats: &RunStats{}, observer: nopObserver{}}
	out, isErr := s.handleTrigger(context.Background(), json.RawMessage(`{"when":"command_done","session_id":999999}`), ".")
	if !isErr || !strings.Contains(out, "no exec session") {
		t.Fatalf("unknown session must error cleanly: %q %v", out, isErr)
	}
}

func TestMemoryFlow(t *testing.T) {
	s := &Sigmoidh{stats: &RunStats{}, memory: newMemoryStore()}
	call := func(raw string) (string, bool) {
		return s.handleMemory(context.Background(), json.RawMessage(raw), ".")
	}

	if out, isErr := call(`{"op":"list"}`); isErr || out != "(working memory empty)" {
		t.Fatalf("empty list: %q %v", out, isErr)
	}

	out, isErr := call(`{"op":"write","key":"auth","value":"JWT in middleware/auth.go"}`)
	if isErr || !strings.Contains(out, "remembered") {
		t.Fatalf("write failed: %q %v", out, isErr)
	}
	call(`{"op":"write","key":"db","value":"sqlite at ./data/app.db"}`)

	out, _ = call(`{"op":"read","key":"auth"}`)
	if out != "JWT in middleware/auth.go" {
		t.Fatalf("read mismatch: %q", out)
	}

	out, _ = call(`{"op":"list"}`)
	for _, want := range []string{"2 memories", "auth:", "db:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list missing %q:\n%s", want, out)
		}
	}
	// insertion order preserved
	if strings.Index(out, "auth:") > strings.Index(out, "db:") {
		t.Fatal("insertion order broken:\n" + out)
	}

	if out, isErr := call(`{"op":"delete","key":"auth"}`); isErr || !strings.Contains(out, "forgot") {
		t.Fatalf("delete failed: %q %v", out, isErr)
	}
	if _, isErr := call(`{"op":"read","key":"auth"}`); !isErr {
		t.Fatal("deleted key must not read back")
	}

	out, _ = call(`{"op":"clear"}`)
	if !strings.Contains(out, "cleared 1") {
		t.Fatalf("clear wrong: %q", out)
	}
}

func TestMemoryCaps(t *testing.T) {
	s := &Sigmoidh{stats: &RunStats{}, memory: newMemoryStore()}
	long := strings.Repeat("x", maxMemoryValueLen+1)
	out, isErr := s.handleMemory(context.Background(), json.RawMessage(fmt.Sprintf(`{"op":"write","key":"k","value":%q}`, long)), ".")
	if !isErr || !strings.Contains(out, "value too long") {
		t.Fatalf("long value must be refused: %q %v", out, isErr)
	}

	s2 := &Sigmoidh{stats: &RunStats{}, memory: newMemoryStore()}
	for i := 0; i < maxMemoryKeys; i++ {
		raw := fmt.Sprintf(`{"op":"write","key":"k%d","value":"v"}`, i)
		if _, isErr := s2.handleMemory(context.Background(), json.RawMessage(raw), "."); isErr {
			t.Fatalf("write %d failed: %v", i, isErr)
		}
	}
	out2, isErr2 := s2.handleMemory(context.Background(), json.RawMessage(`{"op":"write","key":"overflow","value":"v"}`), ".")
	if !isErr2 || !strings.Contains(out2, "memory full") {
		t.Fatalf("cap must refuse: %q %v", out, isErr)
	}
}

var _ = context.Background

func TestSessionTimestampParsedAsLocal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	path := writeFakeSession(t, dir, "2026-08-22_10-00-00.jsonl", []historyEntry{
		{Role: "user", Content: "hi", Cwd: "."},
	})
	info, err := scanSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.At.Format(sessionTimeLayout); got != "2026-08-22_10-00-00" {
		t.Fatalf("wall-clock roundtrip mismatch: %s", got)
	}
	if info.At.Location() == time.UTC {
		t.Fatal("filename timestamps must parse as local time, not UTC")
	}
}

func TestCallSignatureLoopDetection(t *testing.T) {
	mk := func(name, args string) []litellm.ToolUseBlock {
		return []litellm.ToolUseBlock{{Name: name, Arguments: json.RawMessage(args)}}
	}
	a := mk("bash", `{"command":"ls"}`)
	if callSignature(a) != callSignature(mk("bash", `{"command":"ls"}`)) {
		t.Fatal("identical calls must share a signature")
	}
	if callSignature(a) == callSignature(mk("bash", `{"command":"ls -la"}`)) {
		t.Fatal("different args must differ")
	}
	if callSignature(mk("read", `{"path":"x"}`)) == callSignature(mk("write", `{"path":"x"}`)) {
		t.Fatal("different tools must differ")
	}
}

func TestListSessionsSkipsJunkNames(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	writeFakeSession(t, dir, "22008-08-22_20-55-23.jsonl", []historyEntry{{Role: "user", Content: "junk", Cwd: "."}})
	writeFakeSession(t, dir, "2026-08-22_10-00-00.jsonl", []historyEntry{{Role: "user", Content: "real", Cwd: "."}})

	infos, err := listSessionInfos(dir, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || !strings.HasSuffix(infos[0].Path, "2026-08-22_10-00-00.jsonl") {
		t.Fatalf("junk transcript must be filtered out: %+v", infos)
	}
}

func TestPlanFlow(t *testing.T) {
	s := &Sigmoidh{stats: &RunStats{}, plan: newPlanStore(), observer: nopObserver{}}
	call := func(raw string) (string, bool) {
		return s.handlePlan(context.Background(), json.RawMessage(raw), ".")
	}

	if out, _ := call(`{"op":"list"}`); out != "(plan empty)" {
		t.Fatalf("empty: %q", out)
	}
	out, isErr := call(`{"op":"write","items":["scaffold site","add theme","wire pnpm build"],"active":1}`)
	if isErr || !strings.Contains(out, "1. [>] scaffold site") || !strings.Contains(out, "plan 0/3 done") {
		t.Fatalf("write: %q %v", out, isErr)
	}

	// indexes are 1-based, exactly as displayed
	out, isErr = call(`{"op":"update","index":2,"status":"in_progress"}`)
	if isErr || !strings.Contains(out, "2. [>] add theme") {
		t.Fatalf("update: %q %v", out, isErr)
	}
	// moving on completes the previous in-progress item instead of resetting it
	if !strings.Contains(out, "1. [x] scaffold site") {
		t.Fatalf("previous active item lost its progress: %s", out)
	}

	// next: finish the current item and start the following one in one call
	out, _ = call(`{"op":"next"}`)
	if !strings.Contains(out, "2. [x] add theme") || !strings.Contains(out, "3. [>] wire pnpm build") {
		t.Fatalf("next: %s", out)
	}
	out, _ = call(`{"op":"next"}`)
	if !strings.Contains(out, "all done") {
		t.Fatalf("finishing the last item should report all done: %s", out)
	}

	if _, isErr := call(`{"op":"update","index":0,"status":"done"}`); !isErr {
		t.Fatal("index 0 must error (1-based)")
	}
	if _, isErr := call(`{"op":"update","index":9,"status":"done"}`); !isErr {
		t.Fatal("out-of-range index must error")
	}
	if _, isErr := call(`{"op":"bogus"}`); !isErr {
		t.Fatal("bad op must error")
	}
}

// Rewriting the plan after a scope change keeps finished items finished.
func TestPlanRewriteKeepsStatus(t *testing.T) {
	s := &Sigmoidh{plan: newPlanStore()}
	s.handlePlan(nil, json.RawMessage(`{"op":"write","items":["a","b"],"active":1}`), "")
	s.handlePlan(nil, json.RawMessage(`{"op":"next"}`), "")
	out, _ := s.handlePlan(nil, json.RawMessage(`{"op":"write","items":["a","b","c"]}`), "")
	if !strings.Contains(out, "1. [x] a") || !strings.Contains(out, "2. [>] b") || !strings.Contains(out, "3. [ ] c") {
		t.Fatalf("statuses not kept across rewrite:\n%s", out)
	}
}

func TestAskUserSetsPendingAndFormats(t *testing.T) {
	var fired []string
	s := &Sigmoidh{stats: &RunStats{}, observer: nopObserver{}}
	// capture the trigger-fired card via a recording observer
	rec := &recorder{inner: nopObserver{}}
	s.observer = rec

	out, isErr := s.handleAskUser(context.Background(),
		json.RawMessage(`{"question":"Which framework?","options":["vite","next"]}`), ".")
	if isErr {
		t.Fatalf("ask_user failed: %q", out)
	}
	if !strings.Contains(out, "End your turn") {
		t.Fatalf("result must instruct turn end: %q", out)
	}
	if s.pendingAsk == nil || !strings.Contains(*s.pendingAsk, "Which framework?") {
		t.Fatalf("pendingAsk not stored: %v", s.pendingAsk)
	}
	_ = fired
}

type recorder struct{ inner AgentObserver }

func (r *recorder) OnTriggerFired(msg string) {}

func (r *recorder) OnBootstrap(v string, p string, m string, ro bool) {
	r.inner.OnBootstrap(v, p, m, ro)
}
func (r *recorder) OnRequestStart()                        { r.inner.OnRequestStart() }
func (r *recorder) OnRequestDone()                         { r.inner.OnRequestDone() }
func (r *recorder) OnThinkingStart()                       { r.inner.OnThinkingStart() }
func (r *recorder) OnReasoningDelta(t string)              { r.inner.OnReasoningDelta(t) }
func (r *recorder) OnThinkingEnd(d time.Duration)          { r.inner.OnThinkingEnd(d) }
func (r *recorder) OnAssistantTextStart()                  { r.inner.OnAssistantTextStart() }
func (r *recorder) OnAssistantDelta(t string)              { r.inner.OnAssistantDelta(t) }
func (r *recorder) OnAssistantText(t string)               { r.inner.OnAssistantText(t) }
func (r *recorder) OnToolCall(n string, a json.RawMessage) { r.inner.OnToolCall(n, a) }
func (r *recorder) OnToolResult(n string, o string, e bool, d time.Duration) {
	r.inner.OnToolResult(n, o, e, d)
}
func (r *recorder) OnProviderFallback(p string, m string) { r.inner.OnProviderFallback(p, m) }
func (r *recorder) OnRetryWait(p string, a int, d time.Duration) {
	r.inner.OnRetryWait(p, a, d)
}
func (r *recorder) OnTurnUsage(u litellm.Usage)   { r.inner.OnTurnUsage(u) }
func (r *recorder) OnRunComplete(s *RunStats)     { r.inner.OnRunComplete(s) }
func (r *recorder) OnPrintUsageStats(s *RunStats) { r.inner.OnPrintUsageStats(s) }
func (r *recorder) SetThinkingVisible(v bool)     { r.inner.SetThinkingVisible(v) }
func (r *recorder) ThinkingVisible() bool         { return r.inner.ThinkingVisible() }
func (r *recorder) SetUsageVisible(v bool)        { r.inner.SetUsageVisible(v) }
func (r *recorder) UsageVisible() bool            { return r.inner.UsageVisible() }

var _ AgentObserver = (*recorder)(nil)

func TestThinkScrubberStreamingSplitTags(t *testing.T) {
	var ts thinkScrubber
	var out strings.Builder
	for _, chunk := range []string{"Hello <th", "ink>secret plan</th", "ink> world", "!"} {
		out.WriteString(ts.Scrub(chunk))
	}
	if got := out.String(); got != "Hello  world!" {
		t.Fatalf("got %q", got)
	}
}

func TestThinkScrubberKeepsPlainText(t *testing.T) {
	var ts thinkScrubber
	got := ts.Scrub("a < b and c > d")
	if got != "a < b and c > d" {
		t.Fatalf("plain text mangled: %q", got)
	}
}

func TestSessionSearchFindsAndRanks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(homeDir(), ".sigmoidh", "history")
	os.MkdirAll(dir, 0o755)
	writeFakeSession(t, dir, "2026-08-22_10-00-00.jsonl", []historyEntry{
		{Role: "user", Content: "how do we handle auth tokens in the API?", Cwd: "."},
	})
	writeFakeSession(t, dir, "2026-08-21_09-00-00.jsonl", []historyEntry{
		{Role: "assistant", Content: "we chose JWT auth tokens with rotation", Cwd: "."},
	})

	s := &Sigmoidh{stats: &RunStats{}}
	out, isErr := s.handleSessionSearch(context.Background(),
		json.RawMessage(`{"query":"auth tokens"}`), ".")
	if isErr {
		t.Fatalf("search errored: %q", out)
	}
	if !strings.Contains(out, "2 matches") || !strings.Contains(out, "JWT") {
		t.Fatalf("results wrong:\n%s", out)
	}
	if !strings.Contains(out, ".jsonl") {
		t.Fatalf("must include source path:\n%s", out)
	}
}
