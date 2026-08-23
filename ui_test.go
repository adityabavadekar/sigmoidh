package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/voocel/litellm"
)

// liveWriter: wrap/prefix/row accounting used by streaming output
func TestLiveWriterPrefixOnEveryLine(t *testing.T) {
	var buf bytes.Buffer
	lw := newLiveWriter(&buf, 40, "  │ ")
	lw.WriteString("line one\nline two\nline three")
	got := buf.String()
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d: %q", len(lines), got)
	}
	for i, ln := range lines {
		if !strings.HasPrefix(ln, "  │ ") {
			t.Fatalf("line %d missing prefix: %q", i+1, ln)
		}
	}
	if lw.rows != 3 {
		t.Fatalf("rows: want 3, got %d", lw.rows)
	}
}

func TestLiveWriterWrapsLongTokens(t *testing.T) {
	var buf bytes.Buffer
	lw := newLiveWriter(&buf, 30, "  ") // content width = 28
	long := strings.Repeat("x", 60)
	lw.WriteString(long)

	lines := strings.Split(buf.String(), "\n")
	if len(lines) != 3 {
		t.Fatalf("60 chars in width 28 should wrap to 3 lines, got %d", len(lines))
	}
	for _, ln := range lines {
		if !strings.HasPrefix(ln, "  ") || strings.TrimPrefix(ln, "  ") == "" {
			t.Fatalf("bad wrapped line: %q", ln)
		}
	}
	// prefix counts inside the total width: content = 28-2 per line -> 26+26+8
	if lw.rows != 3 || lw.col != 10 {
		t.Fatalf("rows=%d col=%d, want 3/10", lw.rows, lw.col)
	}
}

func TestLiveWriterWideRunes(t *testing.T) {
	var buf bytes.Buffer
	lw := newLiveWriter(&buf, 20, "")
	lw.WriteString("日本語") // 3 runes x 2 columns
	if buf.String() != "日本語" || lw.col != 6 {
		t.Fatalf("wide rune handling wrong: out=%q col=%d", buf.String(), lw.col)
	}
}

func TestLiveWriterTabExpansion(t *testing.T) {
	var buf bytes.Buffer
	lw := newLiveWriter(&buf, 40, "")
	lw.WriteString("a\tb")
	if buf.String() != "a   b" || lw.col != 5 {
		t.Fatalf("tab expansion wrong: %q col=%d", buf.String(), lw.col)
	}
}

// spinner lifecycle (single-writer invariant; TTY off = no-op)
func TestSpinnerNoopWithoutTTY(t *testing.T) {
	l := newTestLogger(&bytes.Buffer{}, true)
	l.OnRequestStart()
	if l.spin != nil {
		t.Fatal("spinner must not start when stdout is not a TTY")
	}
	l.OnToolCall("bash", []byte(`{}`))
	l.startSpinner("running bash…")
	if l.spin != nil {
		t.Fatal("startSpinner must no-op without TTY")
	}
	l.stopSpinner() // must not hang or panic
}

func TestSpinnerStopIdempotent(t *testing.T) {
	l := newTestLogger(&bytes.Buffer{}, true)
	l.stopSpinner()
	l.stopSpinner()
}

// full turn sequence through the real observer (non-TTY)
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	w.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// Mirrors the exact callback order of Sigmoidh.Run/streamTurn for a
// thinking -> text -> tool -> result turn.
func TestFullTurnSequenceSmoke(t *testing.T) {
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)

	stdout := captureStdout(t, func() {
		l.OnRequestStart()
		time.Sleep(10 * time.Millisecond)
		l.OnThinkingStart()
		l.OnReasoningDelta("Compute 19*27.\nAnswer is 513.")
		l.OnThinkingEnd(1200 * time.Millisecond)
		l.OnAssistantTextStart()
		l.OnAssistantDelta("**19 × 27 = 513**")
		l.OnAssistantText("**19 × 27 = 513**")
		l.OnRequestDone()
	})

	chrome := buf.String()
	for _, want := range []string{"thought for 1.2s"} {
		if !strings.Contains(chrome, want) {
			t.Fatalf("chrome missing %q:\n%s", want, chrome)
		}
	}
	if strings.Contains(chrome, "● Turn") || strings.Contains(chrome, "└") {
		t.Fatalf("per-turn chrome should be gone:\n%s", chrome)
	}
	// non-TTY: thinking falls back to chrome; assistant text renders to stdout
	for _, want := range []string{"513"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

// /statsline toggle
func TestStatsLineDefaultOffAndToggleable(t *testing.T) {
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)

	l.OnTurnUsage(litellm.Usage{TotalTokens: 1234, InputTokens: 1200, OutputTokens: 34})
	if buf.Len() != 0 {
		t.Fatalf("stats line must be off by default:\n%s", buf.String())
	}

	l.SetUsageVisible(true)
	if !l.UsageVisible() {
		t.Fatal("SetUsageVisible(true) did not stick")
	}
	l.OnTurnUsage(litellm.Usage{TotalTokens: 1234, InputTokens: 1200, OutputTokens: 34})
	out := buf.String()
	for _, want := range []string{"1.2K tok", "1.2K in", "34 out"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stats line missing %q:\n%s", want, out)
		}
	}

	buf.Reset()
	l.OnTurnUsage(litellm.Usage{}) // zero usage prints nothing even when on
	if buf.Len() != 0 {
		t.Fatalf("zero usage must not print:\n%s", buf.String())
	}
}

// diff previews for edit/apply_patch
func TestEditDiffPreviewRendered(t *testing.T) {
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)
	l.OnToolCall("edit", json.RawMessage(`{"path":"a.go","old_str":"foo","new_str":"bar"}`))
	l.OnToolResult("edit", "ok", false, 20*time.Millisecond)

	out := stripANSI(buf.String())
	for _, want := range []string{"a.go", "- foo", "+ bar", "▼ result"} {
		if !strings.Contains(out, want) {
			t.Fatalf("diff preview missing %q:\n%s", want, out)
		}
	}
}

func TestApplyPatchPreviewTruncates(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("--- a.txt\n+++ b.txt\n@@ -1 +1 @@\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&sb, "+line%d\n", i)
	}
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)
	l.OnToolCall("apply_patch", json.RawMessage(`{"patch":`+strconv.Quote(sb.String())+`}`))
	l.OnToolResult("apply_patch", "ok", false, time.Millisecond)

	out := stripANSI(buf.String())
	if !strings.Contains(out, "@@ -1 +1 @@") || !strings.Contains(out, "more line") {
		t.Fatalf("patch preview/truncation wrong:\n%s", out)
	}
}

func TestDiffQueueOrderWithPlainToolsBetween(t *testing.T) {
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)
	l.OnToolCall("bash", json.RawMessage(`{"cmd":"ls"}`))
	l.OnToolCall("edit", json.RawMessage(`{"path":"x","old_str":"o","new_str":"n"}`))
	l.OnToolResult("bash", "", false, time.Millisecond) // plain tool pops empty preview
	l.OnToolResult("edit", "", false, time.Millisecond)

	out := stripANSI(buf.String())
	beforeEdit := out[:strings.Index(out, "- o")]
	if strings.Contains(beforeEdit, "- o") {
		t.Fatal("diff leaked into earlier tool result")
	}
	if !strings.Contains(out, "+ n") {
		t.Fatalf("edit diff missing:\n%s", out)
	}
}

func stripANSI(s string) string {
	return regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]").ReplaceAllString(s, "")
}

// Renders the full edit block (call line + diff + result) into the test log
// so `./test.sh -v` output doubles as a visual regression artifact.
func TestVisualEditBlockSnapshot(t *testing.T) {
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)
	args := `{"path":"/home/adi/projects/sigmoidh/main.go","old_str":"const VERSION = \"v0.4.0\"","new_str":"const VERSION = \"v0.5.0\""}`
	l.OnToolCall("edit", json.RawMessage(args))
	l.OnToolResult("edit", "ok", false, 310*time.Millisecond)

	plain := stripANSI(buf.String())
	t.Log("\n" + plain)
	for _, want := range []string{"▲ edit", "main.go", "(+1 -1)", "│ - const VERSION", "│ + const VERSION"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "new_str") {
		t.Fatalf("raw JSON leaked into call line:\n%s", plain)
	}
}

func TestUnknownToolArgsTruncatedButBashFull(t *testing.T) {
	long := strings.Repeat("x", 300)

	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)
	l.OnToolCall("mystery", json.RawMessage(fmt.Sprintf(`{"data":%q}`, long)))
	line := stripANSI(strings.SplitN(buf.String(), "\n", 2)[0])
	if len([]rune(line)) > 110 || !strings.HasSuffix(line, "…") {
		t.Fatalf("unknown-tool args not truncated (%d runes): %s", len([]rune(line)), line)
	}

	buf2 := &bytes.Buffer{}
	l2 := newTestLogger(buf2, true)
	cmd := "docker run --rm -v $(pwd):/app -w /app node:20 sh -c " + strings.Repeat("y", 200)
	l2.OnToolCall("bash", json.RawMessage(fmt.Sprintf(`{"command":%q}`, cmd)))
	squeeze := func(s string) string {
		s = stripANSI(s)
		s = strings.ReplaceAll(s, "\n", "")
		s = strings.ReplaceAll(s, "│", "")
		return strings.ReplaceAll(s, " ", "")
	}
	if !strings.Contains(squeeze(buf2.String()), squeeze(cmd)) {
		t.Fatalf("bash command must render in full:\n%s", stripANSI(buf2.String()))
	}
	if strings.Count(stripANSI(buf2.String()), "│") < 2 {
		t.Fatalf("long command must hard-wrap across gutter lines:\n%s", stripANSI(buf2.String()))
	}
}

func TestDiffBandsUseGitStyleBackgrounds(t *testing.T) {
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)
	l.OnToolCall("edit", json.RawMessage(`{"path":"f.go","old_str":"a","new_str":"b"}`))
	l.OnToolResult("edit", "ok", false, time.Millisecond)
	raw := buf.String()
	for _, want := range []string{"\x1b[48;5;52m", "\x1b[48;5;22m"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing diff band %q:\n%q", want, raw)
		}
	}
}

func TestFeedReasoningCollapsesChoppyNewlines(t *testing.T) {
	l := newTestLogger(&bytes.Buffer{}, true)
	var got strings.Builder
	// choppy upstream: words separated by lone newlines, one real paragraph break
	for _, chunk := range []string{"Now\n", "verify\n", "the\n", "plan:\n", "\n- step one"} {
		got.WriteString(l.feedReasoning(chunk))
	}
	want := "Now verify the plan:\n\n- step one"
	if got.String() != want {
		t.Fatalf("normalization wrong:\n got %q\nwant %q", got.String(), want)
	}
}

func TestRetryDelayBackoff(t *testing.T) {
	prev := time.Duration(0)
	for i := 1; i <= retriesPerProvider; i++ {
		d := retryDelay(i)
		if d < 250*time.Millisecond || d > retryMaxDelay {
			t.Fatalf("attempt %d delay out of range: %v", i, d)
		}
		if i > 1 && d <= prev/2 {
			t.Fatalf("backoff not growing: attempt %d = %v after %v", i, d, prev)
		}
		prev = d
	}
}

// Regression: on a real TTY, finished thinking must swap the raw live stream
// for rendered markdown exactly once — the raw bytes get wiped by the
// cursor-restore + clear before the final block lands.
func TestThinkingRenderOnceOnTTY(t *testing.T) {
	buf := &bytes.Buffer{}
	l := &TerminalLogger{w: buf, debug: false, outTTY: true, showThinking: true}

	stdout := captureStdout(t, func() {
		l.OnThinkingStart()
		l.OnReasoningDelta("Plan:\n- use `pnpm`\n- no glow\n")
		l.OnThinkingEnd(1500 * time.Millisecond)
	})

	for _, want := range []string{"✻ thinking…", "pnpm", "thought for 1.5s"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
	// raw stream + final render = two occurrences; the wipe must sit between
	first := strings.Index(stdout, "pnpm")
	last := strings.LastIndex(stdout, "pnpm")
	if first == last {
		t.Fatalf("expected raw stream then final render:\n%s", stdout)
	}
	if !strings.Contains(stdout[first:last], restoreCursor+clearToEnd) {
		t.Fatalf("raw region not wiped before final render:\n%s", stdout)
	}
	if n := strings.Count(stripANSI(stdout), "✻ thinking…"); n != 2 {
		t.Fatalf("header count = %d, want 2 (live + final):\n%s", n, stdout)
	}
}

// Live assistant text streams raw under a cursor anchor, then the completed
// markdown replaces it in place.
func TestAssistantLiveStreamSwapOnTTY(t *testing.T) {
	buf := &bytes.Buffer{}
	l := &TerminalLogger{w: buf, debug: false, outTTY: true, showThinking: true}

	stdout := captureStdout(t, func() {
		l.OnAssistantTextStart()
		l.OnAssistantDelta("**19 × 27")
		l.OnAssistantDelta(" = 513**")
		l.OnAssistantText("**19 × 27 = 513**")
	})

	for _, want := range []string{"19 × 27 = 513", restoreCursor + clearToEnd} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%q", want, stdout)
		}
	}
	if strings.Count(stdout, "19 × 27") < 2 {
		t.Fatalf("expected raw deltas plus final render:\n%q", stdout)
	}
}

func TestSubagentObserverIndentsAndQuiets(t *testing.T) {
	buf := &bytes.Buffer{}
	parent := &TerminalLogger{w: buf, outTTY: false, showThinking: true}
	child := parent.SubagentObserver()

	child.OnToolCall("read", json.RawMessage(`{"path":"main.go"}`))
	child.OnToolResult("read", "data", false, time.Millisecond)
	child.OnAssistantText("final prose that must stay hidden")

	out := stripANSI(buf.String())
	if !strings.Contains(out, "▲ read") || !strings.Contains(out, "▼ result") {
		t.Fatalf("child tool activity missing:\\n%s", out)
	}
	if !strings.Contains(out, "\\n    ") && !strings.HasPrefix(out, "    ") {
		t.Fatalf("child lines must be indented:\\n%s", out)
	}
	if strings.Contains(out, "final prose") {
		t.Fatalf("quietText must hide sub-agent prose:\\n%s", out)
	}
}

func TestSubagentPromptFramedAtSpawn(t *testing.T) {
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)
	l.OnToolCall("subagent", json.RawMessage(`{"prompt":"refactor the auth middleware and report"}`))

	out := stripANSI(buf.String())
	if !strings.Contains(out, "task · refactor the auth middleware and report") {
		t.Fatalf("full prompt not framed at spawn:\\n%s", out)
	}
	if strings.Contains(out, "…") {
		t.Fatalf("prompt must not be truncated:\\n%s", out)
	}
}

func TestPlanBoardRenderedToUser(t *testing.T) {
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)
	board := "1. [x] scaffold\n2. [~] theme\n3. [ ] wire build"
	l.OnToolCall("plan", json.RawMessage(`{"op":"update","index":1,"status":"in_progress"}`))
	l.OnToolResult("plan", board, false, time.Millisecond)

	out := stripANSI(buf.String())
	var missing []string
	for _, want := range []string{"✓ 1. scaffold", "▶ 2. theme", "▲ plan  update #2→in_progress", "○ 3."} {
		if !strings.Contains(out, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("plan board missing %v:\n%s", missing, out)
	}
}

func TestPlainModeOnlyPrintsResponse(t *testing.T) {
	buf := &bytes.Buffer{}
	l := &TerminalLogger{w: buf, outTTY: false, showThinking: true}
	MakePlain(l)

	stdout := captureStdout(t, func() {
		l.OnBootstrap("v", "p", "m", false)
		l.OnRequestStart()
		l.OnThinkingStart()
		l.OnReasoningDelta("secret reasoning")
		l.OnThinkingEnd(900 * time.Millisecond)
		l.OnToolCall("bash", json.RawMessage(`{"command":"ls"}`))
		l.OnToolResult("bash", "files", false, time.Millisecond)
		l.OnAssistantText("**Just** the answer.")
		l.OnRunComplete(&RunStats{TotalTurns: 1})
	})

	if strings.Contains(stdout, "secret") || strings.Contains(stdout, "✻") ||
		strings.Contains(stdout, "▲") || strings.Contains(stdout, "● Done") {
		t.Fatalf("plain mode leaked chrome:\\n%s", stdout)
	}
	if strings.TrimSpace(stripANSICodes(stdout)) != "Just the answer." {
		t.Fatalf("expected bare response:\\n%q", stdout)
	}
}

func TestShortToolOutputRenderedInline(t *testing.T) {
	buf := &bytes.Buffer{}
	l := newTestLogger(buf, true)
	l.OnToolCall("bash", json.RawMessage(`{"command":"git remote -v"}`))
	l.OnToolResult("bash", "origin\thttps://github.com/x/y.git\n", false, 20*time.Millisecond)

	out := stripANSI(buf.String())
	if !strings.Contains(out, "github.com/x/y.git") || !strings.Contains(out, "│ origin") {
		t.Fatalf("short output must render inline:\\n%s", out)
	}
	if !strings.Contains(out, "(38 bytes") && !strings.Contains(out, "bytes") {
		t.Fatalf("byte summary should stay:\\n%s", out)
	}
}

// Empty-reasoning turns must not strand orphan "✻ thinking…" headers.
func TestThinkingEmptyTurnNoOrphanHeader(t *testing.T) {
	buf := &bytes.Buffer{}
	l := &TerminalLogger{w: buf, outTTY: true, showThinking: true}
	stdout := captureStdout(t, func() {
		l.OnThinkingStart()
		l.OnThinkingEnd(100 * time.Millisecond)
	})
	if strings.Contains(stdout, "thinking") || strings.Contains(buf.String(), "thinking") {
		t.Fatalf("orphan header from empty reasoning turn:\nstdout=%q chrome=%q", stdout, buf.String())
	}
}

// Blocks taller than the screen keep their raw stream: the anchor is gone,
// so a styled re-render would duplicate the answer.
func TestAssistantTallBlockKeepsRaw(t *testing.T) {
	old := swapMaxRows
	swapMaxRows = func() int { return 2 }
	defer func() { swapMaxRows = old }()

	buf := &bytes.Buffer{}
	l := &TerminalLogger{w: buf, outTTY: true, showThinking: true}
	stdout := captureStdout(t, func() {
		l.OnAssistantTextStart()
		for i := 0; i < 10; i++ {
			l.OnAssistantDelta("row of a very tall answer\n")
		}
		l.OnAssistantText("row of a very tall answer\n")
	})
	if n := strings.Count(stdout, "very tall answer"); n != 10 {
		t.Fatalf("raw rows lost/duplicated: %d", n)
	}
	if strings.Contains(stdout, restoreCursor+clearToEnd) {
		t.Fatalf("tall block attempted an in-place swap:\n%q", stdout)
	}
}
