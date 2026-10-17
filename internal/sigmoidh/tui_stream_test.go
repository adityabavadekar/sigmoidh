package sigmoidh

import (
	"github.com/mattn/go-runewidth"
	"strings"
	"testing"
	"time"

	"github.com/rivo/tview"
)

func newDirectFeed(t *testing.T) (*tview.TextView, *tuiCore) {
	t.Helper()
	tv := tview.NewTextView().SetDynamicColors(true)
	core := &tuiCore{feed: newFeed(tv), sink: bareSink{}}
	core.showThinking.Store(true)
	return tv, core
}

// stripTags removes tview color tags so assertions read like rendered text.
func stripTags(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '[' && i+1 < len(s) && s[i+1] == '[':
			b.WriteByte('[')
			i++
		case s[i] == '[':
			for j := i; j < len(s); j++ {
				if s[j] == ']' {
					i = j
					break
				}
			}
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// mdStream: streaming markdown must style incrementally
func TestMdStreamBoldAcrossChunks(t *testing.T) {
	var m mdStream
	m.Feed("this is **bo")
	out := m.Feed("ld** done\n")
	m.Close()
	if !strings.Contains(stripTags(out), "bold done") {
		t.Fatalf("bold split across chunks mangled: %q", stripTags(out))
	}
	if strings.Count(out, tgBold)+strings.Count(out, "[::-]") < 2 {
		t.Fatalf("expected open+close bold tags:\n%s", out)
	}
}

func TestMdStreamHeadingsBulletsFences(t *testing.T) {
	var m mdStream
	out := m.Feed("# Title\n- item one\n```go\ncode()\n```\nafter\n")
	m.Close()
	plain := stripTags(out)
	for _, want := range []string{"Title", "item one", "code()", "after"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in %q", want, plain)
		}
	}
	if strings.Contains(plain, "```") {
		t.Fatalf("fence markers leaked: %q", plain)
	}
}

func TestMdStreamEscapesBrackets(t *testing.T) {
	var m mdStream
	out := stripTags(m.Feed("array [0] kept\n"))
	m.Close()
	if !strings.Contains(out, "[0] kept") {
		t.Fatalf("literal brackets lost: %q", out)
	}
}

func TestMdStreamCloseFlushesPartialLine(t *testing.T) {
	var m mdStream
	m.Feed("partial tail")
	out := m.Close()
	if !strings.Contains(stripTags(out), "partial tail") {
		t.Fatalf("Close lost partial line: %q", out)
	}
}

// diff bands
func TestRenderDiffBandsCapAndMore(t *testing.T) {
	rows := make([]diffRow, 0, maxDiffLines+5)
	for i := 0; i < maxDiffLines+5; i++ {
		rows = append(rows, diffRow{"+", "line", true})
	}
	out := stripANSI(renderDiffBands(rows, 60))
	if got := strings.Count(out, "\n"); got != maxDiffLines+1 {
		t.Fatalf("want %d band rows + more line, got %d", maxDiffLines+1, got)
	}
	if !strings.Contains(out, "5 more line") {
		t.Fatalf("missing truncation notice: %q", out)
	}
}

func TestParseDiffRowsEdit(t *testing.T) {
	rows := parseDiffRows("edit", []byte(`{"path":"a","old_str":"old","new_str":"new"}`))
	if len(rows) != 2 || rows[0].sign != "-" || rows[1].sign != "+" || rows[1].add != true {
		t.Fatalf("edit rows wrong: %+v", rows)
	}
}

// gutter blocks
// Commands are NOT pre-wrapped anymore: the TextView wraps at live width, so
// a long one-liner stays one gutter row here and never splits mid-token.
func TestGutterBlockKeepsSourceLines(t *testing.T) {
	long := `curl -sIL -m 8 https://vewake.me | grep -i '^(HTTP|location)' | awk '{print $2}'`
	out := stripTags(gutterBlock(long))
	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(rows) != 1 {
		t.Fatalf("source line pre-split into %d rows:\n%s", len(rows), out)
	}
	if !strings.HasPrefix(rows[0], "  │ ") || !strings.HasSuffix(rows[0], "print $2}'") {
		t.Fatalf("gutter/contents wrong: %q", rows[0])
	}

	multi := gutterBlock("one\n\ntwo")
	if strings.Count(multi, "\n") != 3 || !strings.Contains(stripTags(multi), "  │\n") {
		t.Fatalf("blank-line gutter handling wrong: %q", multi)
	}
}

// context meter
func TestCtxMeterThresholds(t *testing.T) {
	if c := ctxMeterColor(10); c != pal.mint {
		t.Fatal("low usage should be mint")
	}
	if c := ctxMeterColor(70); c != pal.amber {
		t.Fatal("70% should be amber")
	}
	if c := ctxMeterColor(95); c != pal.alert {
		t.Fatal("95% should be alert")
	}
	bar, _, pct := ctxMeterBar(32000, 64000)
	if pct != 50 || !strings.Contains(bar, "▰") {
		t.Fatalf("meter wrong: pct=%d bar=%q", pct, bar)
	}
}

// full event flow through the direct observer
func TestDirectObserverFullFlow(t *testing.T) {
	tv, core := newDirectFeed(t)
	core.OnBootstrap("v9", "openrouter", "test/model", false)
	core.OnRequestStart()
	core.OnThinkingStart()
	core.OnReasoningDelta("plan the work")
	core.OnThinkingEnd(1500 * time.Millisecond)
	core.OnAssistantTextStart()
	core.OnAssistantDelta("**Done** fast")
	core.OnAssistantText("**Done** fast")
	core.OnToolCall("bash", []byte(`{"command":"git status --short"}`))
	core.OnToolResult("bash", "M main.go\n", false, 20*time.Millisecond)
	core.OnRunComplete(&RunStats{TotalTurns: 1, TotalToolCalls: 1, TotalTokens: 2048, TotalDuration: 3 * time.Second})

	out := tv.GetText(true)
	for _, want := range []string{
		"SIGMOIDH v9", "openrouter · test/model", "directory",
		"• thinking…", "plan the work", "thought for 1.5s",
		"Done fast", "• bash", "git status --short",
		"M main.go", "└ done", "10 B", "• done", "turns 1", "took 3.0s",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream missing %q:\n%s", want, out)
		}
	}
}

func TestCoreCountersTrackUsage(t *testing.T) {
	_, core := newDirectFeed(t)
	core.addCtxChars(400) // ~100 tokens by chars÷4
	if got := core.estContextTokens(); got < 99 || got > 101 {
		t.Fatalf("char estimate wrong: %d", got)
	}
	core.statMu.Lock()
	core.lastInputTokens = 5000
	core.outCharsSinceUse = 40
	core.statMu.Unlock()
	if got := core.estContextTokens(); got != 5010 {
		t.Fatalf("usage-based estimate wrong: %d", got)
	}
}

func TestSubagentCardsIndentedAndQuiet(t *testing.T) {
	tv, parent := newDirectFeed(t)
	child := parent.SubagentObserver().(*tuiCore)

	child.OnAssistantTextStart()
	child.OnAssistantDelta("secret prose")
	child.OnAssistantText("secret prose that must stay hidden")
	child.OnToolCall("read", []byte(`{"path":"main.go"}`))
	child.OnToolResult("read", "contents", false, time.Millisecond)

	out := tv.GetText(true)
	if strings.Contains(out, "secret prose") {
		t.Fatalf("quiet child leaked prose:\n%s", out)
	}
	if !strings.Contains(out, "• read") || !strings.Contains(out, "main.go") {
		t.Fatalf("child tool card missing:\n%s", out)
	}
	raw := stripTags(tv.GetText(false))
	if !strings.Contains(raw, "    •") {
		t.Fatalf("child card not indented:\n%q", raw)
	}
}

// plan strip
func TestPlanStripRendering(t *testing.T) {
	_, core := newDirectFeed(t)
	core.diffMu.Lock()
	core.planBoard = "1. [x] scaffold\n2. [~] theme\n3. [ ] wire build"
	board := core.planBoard
	core.diffMu.Unlock()

	out := stripTags(core.planBoardText())
	for _, want := range []string{"✓ · 1. scaffold", "▶ · 2. theme", "○ · 3. wire build"} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan missing %q:\n%s", want, out)
		}
	}
	core.diffMu.Lock()
	core.planBoard = ""
	core.diffMu.Unlock()
	if empty := core.planBoardText(); empty != "" {
		t.Fatalf("empty board must render nothing: %q", empty)
	}
	_ = board
}

// prose normalizer: the random-double-spaces regression
func TestTextNormalizerNoDoubleSpaces(t *testing.T) {
	var n textNormalizer
	// chunk splits right after a space, then a soft-wrapped newline
	got := n.normalize("word ") + n.normalize("\n") + n.normalize("next")
	if got != "word next" {
		t.Fatalf("double space across chunks: %q", got)
	}
	if got := n.normalize("a  b\t c"); stripANSI(got) != "a b c" {
		t.Fatalf("space run not collapsed: %q", got)
	}
}

func TestTextNormalizerParagraphsSurvive(t *testing.T) {
	var n textNormalizer
	got := n.normalize("para one\n") + n.normalize("\npara two")
	if !strings.Contains(got, "\n\n") {
		t.Fatalf("paragraph break lost: %q", got)
	}
}

func TestCardQueuedAck(t *testing.T) {
	out := stripTags(cardQueued("fix\nthe bug", 2))
	if !strings.Contains(out, "› queued (#2)") || !strings.Contains(out, "fix") || strings.Contains(out, "the bug") {
		t.Fatalf("queued card wrong: %q", out)
	}
}

// Reproduces the field bug: provider streams word-per-chunk wrapped in
// newlines; reasoning must read as flowing prose, not word-per-paragraph.
func TestReasoningAggressiveWordPerChunk(t *testing.T) {
	var n textNormalizer
	n.Aggressive = true
	got := ""
	for _, c := range []string{"The\n", "\n", "user\n\n", "asks\n", "\nwhat  typ\n", ".ing\n\nis . Let"} {
		got += n.normalize(c)
	}
	for _, bad := range []string{"\n\n", "  ", " ."} {
		if strings.Contains(got, bad) {
			t.Fatalf("artifact %q survived: %q", bad, got)
		}
	}
	if !strings.Contains(got, "The user asks") {
		t.Fatalf("prose not flowing: %q", got)
	}
	if !strings.Contains(got, "typ.ing") {
		t.Fatalf("punct space not absorbed: %q", got)
	}
}

// Structural markers still earn real paragraph breaks in aggressive mode.
func TestReasoningAggressiveKeepsLists(t *testing.T) {
	var n textNormalizer
	n.Aggressive = true
	got := n.normalize("plan:\n\n- step one\n- step two")
	if !strings.Contains(got, "\n\n- step one") {
		t.Fatalf("list break lost: %q", got)
	}
}

// User prompts word-wrap at the live width: breaks land between words and
// continuation lines carry the two-space indent (regression: "hiding" split
// as "hi / ding" at a hardcoded width).
func TestCardUserPromptWordWrap(t *testing.T) {
	long := "yup. here i am github/vewake lets take a challege ive tried to be best at hiding things from you can you still find me?"
	out := stripTags(cardUserPrompt(long, 104))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrap, got %q", out)
	}
	if !strings.HasPrefix(lines[0], "› ") || !strings.HasPrefix(lines[1], "  ") {
		t.Fatalf("prefix/indent wrong: %q / %q", lines[0], lines[1])
	}
	for _, ln := range lines {
		if len([]rune(ln)) > 104 {
			t.Fatalf("line exceeds width: %q", ln)
		}
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "hi\nding") {
		t.Fatalf("mid-word break: %q", joined)
	}
}

func TestCardUserPromptKeepsBlankLines(t *testing.T) {
	out := stripTags(cardUserPrompt("one\n\ntwo", 80))
	if !strings.Contains(out, "one\n\n") {
		t.Fatalf("blank line lost: %q", out)
	}
}

// Emojis (incl. ZWJ sequences and variation selectors) must survive every
// text path: normalizer, markdown styler, tag escaping, measurement.
func TestEmojiPassthrough(t *testing.T) {
	var n textNormalizer
	n.Aggressive = true
	in := "storm 🌦️ incoming — ship 🚢✅ it 👨‍👩‍👧‍👦"
	if got := n.normalize("storm 🌦️\nincoming\n"); !strings.Contains(got, "🌦️") {
		t.Fatalf("normalizer ate emoji: %q", got)
	}
	var m mdStream
	out := stripTags(m.Feed(in + "\n"))
	m.Close()
	if !strings.Contains(out, "🚢✅") || !strings.Contains(out, "👨‍👩‍👧‍👦") {
		t.Fatalf("mdStream ate emoji: %q", out)
	}
	if got := escTag("🎉 [party] 🎉"); !strings.Contains(stripTags(got), "🎉") {
		t.Fatalf("escTag broke emoji: %q", got)
	}
	if w := plainWidth("👍👍"); w != 4 {
		t.Fatalf("emoji width mis-measured: %d", w)
	}
	if got := truncateRunes("🎉🎉🎉", 5); !strings.Contains(got, "🎉") {
		t.Fatalf("truncate broke emoji: %q", got)
	}
}

// TUI: empty-reasoning turns emit no header/ender pair.
func TestTuiEmptyThinkingSilent(t *testing.T) {
	tv, core := newDirectFeed(t)
	core.OnThinkingStart()
	core.OnThinkingEnd(100 * time.Millisecond)
	if out := tv.GetText(true); strings.Contains(out, "thinking") {
		t.Fatalf("orphan thinking cards:\n%s", out)
	}
}

// Tab-indented code fills exactly one band row, and brackets are escaped so
// tview does not swallow them as style tags.
func TestRenderDiffBandsTabsAndBrackets(t *testing.T) {
	out := renderDiffBands([]diffRow{{"+", "\t\treturn a[i]", true}}, 40)
	if strings.Contains(out, "\t") {
		t.Fatalf("raw tab left in band: %q", out)
	}
	if !strings.Contains(out, "a[[i]") {
		t.Fatalf("bracket not escaped: %q", out)
	}
	// a tab-indented row fills exactly the same width as a plain one
	plain := renderDiffBands([]diffRow{{"+", "x", true}}, 40)
	if w, want := plainWidth(strings.TrimRight(out, "\n")), plainWidth(strings.TrimRight(plain, "\n")); w != want {
		t.Fatalf("tab row %d cells wide, plain row %d: %q", w, want, out)
	}
}

// Long reasoning wraps under the rail: every visual row keeps the ▎ prefix
// instead of falling back to column 0, even when words span deltas.
func TestReasoningWrapsUnderRail(t *testing.T) {
	tv, core := newDirectFeed(t)
	core.feed.noteWidth(30)
	core.OnThinkingStart()
	for _, d := range []string{"The edit failed because the old", "_str wasn't found. Let me check ", "the exact content of the file again."} {
		core.OnReasoningDelta(d)
	}
	core.OnThinkingEnd(200 * time.Millisecond)

	out := tv.GetText(true)
	var rows []string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "thinking…") || strings.Contains(ln, "thought for") || strings.TrimSpace(ln) == "" {
			continue
		}
		rows = append(rows, ln)
	}
	if len(rows) < 3 {
		t.Fatalf("expected the reasoning to wrap, got %d rows:\n%s", len(rows), out)
	}
	for _, r := range rows {
		if !strings.HasPrefix(r, "  ▎ ") {
			t.Fatalf("row lost the rail: %q\n%s", r, out)
		}
		if w := runewidth.StringWidth(r); w > 30 {
			t.Fatalf("row %d cells wide, want <= 30: %q", w, r)
		}
	}
	if !strings.Contains(out, "old_str") {
		t.Fatalf("word split across deltas was broken apart:\n%s", out)
	}
}

// An edit that only adds a line shows that line as the change, with the
// unchanged lines as context — not the whole block removed and re-added.
func TestLineDiffShowsOnlyChanges(t *testing.T) {
	rows := lineDiff("func f() {\n\treturn 1\n}", "// f returns one.\nfunc f() {\n\treturn 1\n}")
	if a, r := diffCounts(rows); a != 1 || r != 0 {
		t.Fatalf("counts +%d -%d, want +1 -0: %+v", a, r, rows)
	}
	if rows[0].sign != "+" || rows[1].sign != " " {
		t.Fatalf("rows %+v", rows)
	}
	if len(lineDiff("x\n", "x")) != 0 {
		t.Fatal("trailing newline alone counted as a change")
	}
	far := lineDiff("a\nb\nc\nd\ne\nf\ng\nh", "A\nb\nc\nd\ne\nf\ng\nH")
	var elided bool
	for _, r := range far {
		elided = elided || (r.sign == "@" && r.text == "⋯")
	}
	if !elided {
		t.Fatalf("distant changes not separated by an elision row: %+v", far)
	}
}

// Inside a hunk, "--- x" is a removed line, not a file header.
func TestPatchRowsHunkAware(t *testing.T) {
	rows := patchRows("--- a\n+++ b\n@@ -1 +1 @@\n ctx\n--- gone\n+++ added\n\\ No newline at end of file\n")
	want := []diffRow{{"@", "@@ -1 +1 @@", true}, {" ", "ctx", true}, {"-", "-- gone", false}, {"+", "++ added", true}}
	if len(rows) != len(want) {
		t.Fatalf("rows %+v", rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

// Control bytes from file content never reach the terminal raw, and long
// lines are cut to the band instead of wrapping.
func TestDiffTextCleanedAndFitted(t *testing.T) {
	got := cleanDiffText("a\r\x1b[31mb\tc")
	if strings.ContainsAny(got, "\r\x1b\t") {
		t.Fatalf("unsafe bytes left: %q", got)
	}
	if w := runewidth.StringWidth(fitCells(strings.Repeat("x", 100), 20)); w != 20 {
		t.Fatalf("fitCells width %d", w)
	}
	out := stripANSI(renderDiffANSI([]diffRow{{"+", strings.Repeat("y", 200), true}}, 40))
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("long line wrapped: %q", out)
	}
}

// Pipe tables render as aligned columns once complete, not as raw pipes.
func TestMdStreamRendersTables(t *testing.T) {
	m := mdStream{width: 80}
	out := m.Feed("Results:\n| tool | calls |\n|---|--:|\n| read | 3 |\n| edit | 12 |\n")
	out += m.Feed("after\n") + m.Close()
	plain := stripTVTags(out)
	for _, want := range []string{"tool │ calls", "─────┼──────", "read │     3", "edit │    12", "after"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q in:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "|---") {
		t.Fatalf("raw separator leaked:\n%s", plain)
	}
}
