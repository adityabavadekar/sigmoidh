package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/litellm"
)

// session resume (ported from the /tmp/opencode/simui harness)
func writeFakeSession(t *testing.T, dir, name string, entries []historyEntry) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestListSessionsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	writeFakeSession(t, dir, "2026-08-22_10-00-00.jsonl", nil)
	writeFakeSession(t, dir, "2026-08-21_09-00-00.jsonl", nil)
	writeFakeSession(t, dir, "2026-08-20_09-00-00.jsonl", nil)

	files, err := listSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || !strings.HasSuffix(files[0], "2026-08-22_10-00-00.jsonl") {
		t.Fatalf("expected newest-first ordering, got %v", files)
	}

	// empty dir -> error
	if _, err := listSessions(t.TempDir()); err == nil {
		t.Fatal("expected error for empty history dir")
	}
}

func TestResumeFromRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	writeFakeSession(t, dir, "2026-08-22_10-00-00.jsonl", []historyEntry{
		{Timestamp: "t0", Role: "system", Content: "OLD SYSTEM PROMPT"},
		{Timestamp: "t1", Role: "user", Content: "list the files please"},
		{Timestamp: "t2", Role: "assistant", Content: "Checking.", ToolCalls: []historyToolCall{
			{ID: "tc1", Name: "bash", Args: json.RawMessage(`{"cmd":"ls"}`)},
		}},
		{Timestamp: "t3", Role: "tool", Results: []historyResult{
			{ToolUseID: "tc1", Output: "a.txt\nb.txt"},
		}},
		{Timestamp: "t4", Role: "assistant", Content: "There are two files: a.txt and b.txt."},
	})

	s := &Sigmoidh{
		system:   "FRESH SYSTEM PROMPT",
		messages: []litellm.Message{litellm.System("FRESH SYSTEM PROMPT")},
		stats:    &RunStats{},
	}
	n, err := s.ResumeFrom(filepath.Join(dir, "2026-08-22_10-00-00.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("expected 4 resumed messages (system skipped), got %d", n)
	}
	if len(s.messages) != 5 || s.messages[0].Role != litellm.RoleSystem || s.system != "FRESH SYSTEM PROMPT" {
		t.Fatalf("fresh system prompt not preserved: %+v", s.messages[0])
	}

	// [1] user
	if s.messages[1].Role != litellm.RoleUser {
		t.Fatalf("msg[1]: want user, got %s", s.messages[1].Role)
	}
	// [2] assistant with text + tool_use
	blocks := s.messages[2].Blocks
	if len(blocks) != 2 {
		t.Fatalf("msg[2]: want text+tool_use blocks, got %d", len(blocks))
	}
	tb, ok := blocks[0].(litellm.TextBlock)
	if !ok || tb.Text != "Checking." {
		t.Fatalf("msg[2] block0: want TextBlock 'Checking.', got %#v", blocks[0])
	}
	ub, ok := blocks[1].(litellm.ToolUseBlock)
	if !ok || ub.ID != "tc1" || ub.Name != "bash" {
		t.Fatalf("msg[2] block1: want ToolUseBlock tc1/bash, got %#v", blocks[1])
	}
	// [3] tool result
	rb, ok := s.messages[3].Blocks[0].(litellm.ToolResultBlock)
	if !ok || rb.ToolUseID != "tc1" || rb.IsError {
		t.Fatalf("msg[3]: want ToolResultBlock for tc1, got %#v", s.messages[3].Blocks[0])
	}
	// [4] final assistant text restored into /copy targets
	want := "There are two files: a.txt and b.txt."
	if len(s.lastTexts) != 1 || s.lastTexts[0] != want {
		t.Fatalf("lastTexts: want [%q], got %q", want, s.lastTexts)
	}
}

// Regression: `-continue` must not pick up its own just-created (empty)
// session file. History file creation is lazy; before any run there is no file.
func TestHistoryFileCreatedLazily(t *testing.T) {
	s := &Sigmoidh{system: "SYS", stats: &RunStats{}}
	fname := s.HistoryFilePath()
	os.RemoveAll(filepath.Dir(fname)) // ensure clean state

	if _, err := os.Stat(fname); !os.IsNotExist(err) {
		t.Fatal("history file must not exist before first write")
	}

	s.writeHistory(historyEntry{Timestamp: "t", Role: "user", Content: "hi"})
	defer func() { s.Close() }()

	data, err := os.ReadFile(fname)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want system-seed + user entries, got %d lines", len(lines))
	}
	var seeded historyEntry
	if err := json.Unmarshal([]byte(lines[0]), &seeded); err != nil {
		t.Fatal(err)
	}
	if seeded.Role != "system" || seeded.Content != "SYS" {
		t.Fatalf("seed entry wrong: %+v", seeded)
	}
	var user historyEntry
	if err := json.Unmarshal([]byte(lines[1]), &user); err != nil {
		t.Fatal(err)
	}
	if user.Role != "user" || user.Content != "hi" {
		t.Fatalf("user entry wrong: %+v", user)
	}
}

// Resume appends to the same transcript so continued sessions chain.
func TestResumeKeepsAppendingToSameFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	path := writeFakeSession(t, dir, "2026-08-22_11-00-00.jsonl", []historyEntry{
		{Timestamp: "t1", Role: "user", Content: "hello"},
	})

	s := &Sigmoidh{
		system:   "SYS",
		messages: []litellm.Message{litellm.System("SYS")},
		stats:    &RunStats{},
	}
	if _, err := s.ResumeFrom(path); err != nil {
		t.Fatal(err)
	}
	s.writeHistory(historyEntry{Timestamp: "t2", Role: "user", Content: "continuing"})
	s.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"continuing"`) {
		t.Fatal("resumed session file should keep receiving new history")
	}
}

// UI rendering sequence (non-TTY paths of the PTY simulator)
func newTestLogger(buf *bytes.Buffer, showThinking bool) *TerminalLogger {
	return &TerminalLogger{w: buf, debug: false, outTTY: false, showThinking: showThinking}
}

func driveThinkingSequence(l *TerminalLogger) {
	l.OnThinkingStart()
	l.OnReasoningDelta("First line of thought.\n")
	l.OnReasoningDelta("Second line: 19*27 = 513.")
	l.OnThinkingEnd(1500 * time.Millisecond)
}

// Regression: streamed reasoning must remain visible after thinking completes.
func TestThinkingBlockPersistsAfterEnd(t *testing.T) {
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)
	driveThinkingSequence(l)

	out := buf.String()
	for _, want := range []string{"✻ thinking", "First line of thought.", "19*27 = 513", "thought for 1.5s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Index(out, "19*27 = 513") > strings.Index(out, "thought for") {
		t.Fatal("reasoning content must appear before the footer")
	}
}

func TestThinkingHiddenSuppressed(t *testing.T) {
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, false)
	l.OnThinkingStart()
	l.OnReasoningDelta("secret thoughts\n")
	l.OnThinkingEnd(100 * time.Millisecond)

	out := buf.String()
	if strings.Contains(out, "secret thoughts") || strings.Contains(out, "✻") {
		t.Fatalf("hidden thinking leaked:\n%s", out)
	}
}

func TestToggleAccessors(t *testing.T) {
	l := newTestLogger(&bytes.Buffer{}, true)
	if !l.ThinkingVisible() {
		t.Fatal("thinking must default to visible")
	}
	l.SetThinkingVisible(false)
	if l.ThinkingVisible() {
		t.Fatal("SetThinkingVisible(false) did not stick")
	}
	l.SetThinkingVisible(true)
	if !l.ThinkingVisible() {
		t.Fatal("SetThinkingVisible(true) did not stick")
	}
}

func TestToolResultLineShowsDurationAndBytes(t *testing.T) {
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)
	l.OnToolCall("bash", []byte(`{"cmd":"ls"}`))
	l.OnToolResult("bash", "a\nb\nc\n", false, 300*time.Millisecond)

	out := stripANSI(buf.String())
	if !strings.Contains(out, "▲ bash") || !strings.Contains(out, "▼ result") {
		t.Fatalf("missing call/result markers:\n%s", out)
	}
	if !strings.Contains(out, "6 bytes") || !strings.Contains(out, "0.3s") {
		t.Fatalf("bytes/duration not shown:\n%s", out)
	}
}

func TestSummaryLineIncludesDuration(t *testing.T) {
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)
	l.PrintSummaryLine(&RunStats{
		TotalTurns: 2, TotalToolCalls: 3,
		TotalInputTokens: 100, TotalOutputTokens: 50,
		TotalTokens: 150, TotalReasoningTokens: 10,
		TotalCacheReadTokens: 5, TotalCacheWriteTokens: 7,
		TotalDuration: 9500 * time.Millisecond,
	})
	out := buf.String()
	for _, want := range []string{"● Done", "took 9.5s", "cache"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
}

// session metadata, cwd filtering, and listing
func TestScanSessionMetadata(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	path := writeFakeSession(t, dir, "2026-08-22_10-00-00.jsonl", []historyEntry{
		{Timestamp: "t0", Role: "system", Content: "SYS", Cwd: "/proj/a"},
		{Timestamp: "t1", Role: "user", Content: "fix\n  the\tlogin bug  please"},
		{Timestamp: "t2", Role: "assistant", Content: "ok"},
	})

	info, err := scanSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Cwd != "/proj/a" {
		t.Fatalf("cwd: want /proj/a, got %q", info.Cwd)
	}
	if info.Msgs != 2 {
		t.Fatalf("msgs: want 2 (system excluded), got %d", info.Msgs)
	}
	if info.At.IsZero() || info.At.Format(sessionTimeLayout) != "2026-08-22_10-00-00" {
		t.Fatalf("timestamp not parsed from filename: %v", info.At)
	}
	want := "fix the login bug please"
	if info.Preview != want {
		t.Fatalf("preview: want %q, got %q", want, info.Preview)
	}
}

func TestListSessionInfosFiltersByCwd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	writeFakeSession(t, dir, "2026-08-22_12-00-00.jsonl", []historyEntry{
		{Role: "user", Content: "newest A", Cwd: "/proj/a"},
	})
	writeFakeSession(t, dir, "2026-08-21_09-00-00.jsonl", []historyEntry{
		{Role: "user", Content: "older B", Cwd: "/proj/b"},
	})
	writeFakeSession(t, dir, "2026-08-20_09-00-00.jsonl", []historyEntry{
		{Role: "user", Content: "legacy no-cwd"},
	})
	writeFakeSession(t, dir, "2026-08-19_09-00-00.jsonl", []historyEntry{ // system-only = unresumable
		{Role: "system", Content: "SYS", Cwd: "/proj/a"},
	})

	aInfos, err := listSessionInfos(dir, "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(aInfos) != 1 || aInfos[0].Preview != "newest A" {
		t.Fatalf("cwd filter wrong: %+v", aInfos)
	}

	all, err := listSessionInfos(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 { // legacy + both stamped; system-only skipped
		t.Fatalf("unfiltered view: want 3, got %d (%+v)", len(all), all)
	}
	if !strings.HasSuffix(all[0].Path, "2026-08-22_12-00-00.jsonl") {
		t.Fatal("infos must be newest-first")
	}
}

func TestRelativeTimeFormatting(t *testing.T) {
	now := time.Now()
	cases := []struct {
		at   time.Time
		want string
	}{
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-25 * time.Hour), "yesterday"},
		{now.Add(-4 * 24 * time.Hour), "4d ago"},
	}
	for _, c := range cases {
		if got := relativeTime(c.at); got != c.want {
			t.Errorf("relativeTime(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}

func TestResolveSessionPath(t *testing.T) {
	dir := t.TempDir()
	writeFakeSession(t, dir, "2026-08-22_10-00-00.jsonl", nil)

	p, err := resolveSessionPath(dir, "2026-08-22_10-00-00")
	if err != nil || !strings.HasSuffix(p, "2026-08-22_10-00-00.jsonl") {
		t.Fatalf("bare id resolve failed: %q %v", p, err)
	}
	p, err = resolveSessionPath(dir, "2026-08-22_10-00-00.jsonl")
	if err != nil || p != filepath.Join(dir, "2026-08-22_10-00-00.jsonl") {
		t.Fatalf("full name resolve failed: %q %v", p, err)
	}
	if _, err := resolveSessionPath(dir, "../escape"); err == nil {
		t.Fatal("path traversal must be rejected")
	}
	if _, err := resolveSessionPath(dir, "missing"); err == nil {
		t.Fatal("missing session must error")
	}
}

func TestResolveSessionArg(t *testing.T) {
	dir := t.TempDir()
	writeFakeSession(t, dir, "2026-08-22_12-00-00.jsonl", []historyEntry{{Role: "user", Content: "newest", Cwd: "/w"}})
	writeFakeSession(t, dir, "2026-08-21_09-00-00.jsonl", []historyEntry{{Role: "user", Content: "older", Cwd: "/w"}})

	// table number -> newest-first index
	p, err := resolveSessionArg(dir, "/w", "1")
	if err != nil || !strings.HasSuffix(p, "2026-08-22_12-00-00.jsonl") {
		t.Fatalf("arg 1: got %q %v", p, err)
	}
	p, err = resolveSessionArg(dir, "/w", "2")
	if err != nil || !strings.HasSuffix(p, "2026-08-21_09-00-00.jsonl") {
		t.Fatalf("arg 2: got %q %v", p, err)
	}

	// out of range
	if _, err := resolveSessionArg(dir, "/w", "3"); err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("want out-of-range error, got %v", err)
	}

	// explicit id still works (and is not confused with a number)
	p, err = resolveSessionArg(dir, "/w", "2026-08-21_09-00-00")
	if err != nil || !strings.HasSuffix(p, "2026-08-21_09-00-00.jsonl") {
		t.Fatalf("id arg: got %q %v", p, err)
	}
}

// The static system prompt is the cached prefix for the whole conversation:
// it must stay short and mention the persistent memory file.
func TestSystemPromptLeanAndMentionsMemory(t *testing.T) {
	if len(defaultSystemPrompt) > 600 {
		t.Fatalf("static prompt grew to %d chars; keep it under 600", len(defaultSystemPrompt))
	}
	p, err := generateSystemPrompt(defaultSystemPrompt, false, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MEMORY.md", memoryFilePath(), projectMemoryPath(".")} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt must advertise %q:\n%s", want, p)
		}
	}
}

// The shipped system prompt must stay identical to the "lean" A/B variant so
// future -eval-prompt runs keep measuring what actually ships.
func TestDefaultPromptIsLeanVariant(t *testing.T) {
	if defaultSystemPrompt != promptVariants["lean"] {
		t.Fatal("defaultSystemPrompt drifted from the lean variant; re-run the eval matrix before shipping changes")
	}
}

// Replayed reasoning must never leak back into provider requests.
func TestSanitizeForRequestDropsReasoning(t *testing.T) {
	msgs := []litellm.Message{
		litellm.UserText("q"),
		litellm.Assistant(
			litellm.ReasoningBlock{Text: "secret thoughts"},
			litellm.TextBlock{Text: "answer"},
		),
		{Role: litellm.RoleAssistant}, // empty assistant: must be dropped too
	}
	out := sanitizeForRequest(msgs)
	if len(out) != 2 {
		t.Fatalf("want 2 messages, got %d", len(out))
	}
	for _, m := range out {
		for _, blk := range m.Blocks {
			if _, ok := blk.(litellm.ReasoningBlock); ok {
				t.Fatal("reasoning block leaked into request")
			}
		}
	}
}
