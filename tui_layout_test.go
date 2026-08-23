package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agenttools "github.com/adityabavadekar/sigmoidh/tools"
	"github.com/gdamore/tcell/v2"
	"github.com/voocel/litellm"
)

// Renders the real widget tree into a simulated terminal and verifies the
// opencode-style layout: borderless transcript owning the screen, contextual
// plan strip, prompt box, and a single quiet statusline.
func TestTUILayoutSimulationSmoke(t *testing.T) {
	tui := NewTUI("openrouter", "test/model", false)
	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(110, 30)
	tui.app.SetScreen(screen)

	runErr := make(chan error, 1)
	go func() { runErr <- tui.Run() }()
	time.Sleep(150 * time.Millisecond)

	// Drive one full event sequence through the observer, ending with a plan
	// so the strip must materialize on demand.
	tui.obs.OnRequestStart()
	tui.obs.OnThinkingStart()
	tui.obs.OnReasoningDelta("weighing options")
	tui.obs.OnThinkingEnd(1200 * time.Millisecond)
	tui.obs.OnToolCall("bash", []byte(`{"command":"git remote -v"}`))
	tui.obs.OnToolResult("bash", "origin\tgit@example.com:x/y.git\n", false, 20*time.Millisecond)
	tui.obs.writeCard(cardUserPrompt("hello world", 104))
	tui.obs.OnToolCall("plan", []byte(`{"op":"update","index":1,"status":"in_progress"}`))
	tui.obs.OnToolResult("plan", "1. [x] scaffold\n2. [~] theme\n3. [ ] wire build", false, 5*time.Millisecond)

	snap := func() string {
		if w, h := screen.Size(); w != 110 || h != 30 {
			screen.SetSize(110, 30)
		}
		screen.Sync()
		w, h := screen.Size()
		var b strings.Builder
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, _, _, _ := screen.GetContent(x, y)
				b.WriteRune(r)
			}
			b.WriteByte('\n')
		}
		return b.String()
	}

	var all string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		all = snap()
		if strings.Contains(all, "▶ · 2. theme") &&
			strings.Contains(all, "thought for") &&
			strings.Contains(all, "describe a task…") { // whole frame committed
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// settle: snapshots race live redraws (plan strip resize), so require a
	// quiescent pair before making any assertion.
	for i := 0; i < 20; i++ {
		time.Sleep(60 * time.Millisecond)
		cur := snap()
		if cur == all {
			break
		}
		all = cur
	}
	t.Log("\n" + all)

	for _, want := range []string{
		"test/model",    // model chip in the statusline
		"turns 0",       // professional counters
		"• bash",        // typographic tool row
		"git remote -v", // command gutter
		"› hello world", // codex-style user prompt
		"• thinking…",
		"thought for 1.2s",
		"describe a task…", // prompt placeholder
		"╭", "╰",           // thin rounded chrome
		"▱", "%", // context meter in the statusline
		"▶ · 2. theme", // plan strip appeared because a plan exists
	} {
		if !strings.Contains(all, want) {
			t.Errorf("screen missing %q", want)
		}
	}
	for _, banned := range []string{"◆", "⚙", "🔔", "⏹", "✻ STREAM", "◈ SESSION", "⌗ CONTEXT", "❯ PROMPT"} {
		if strings.Contains(all, banned) {
			t.Errorf("removed chrome still rendered: %q", banned)
		}
	}
	// result branch line and its inline output must sit on separate rows
	for _, ln := range strings.Split(all, "\n") {
		if strings.Contains(ln, "└ ok") && strings.Contains(ln, "│ origin") {
			t.Errorf("result line / gutter glued on one row: %q", ln)
		}
	}

	tui.app.Stop()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, tcell.ErrTermNotFound) {
			t.Fatalf("run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("application did not stop")
	}
}

// Typing a slash prefix must surface the command palette; clearing the
// input must drop it. Mouse capture stays off so native text selection works.
func TestSlashPopupLifecycle(t *testing.T) {
	tui := NewTUI("openrouter", "test/model", false)
	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(110, 30)
	tui.app.SetScreen(screen)

	runErr := make(chan error, 1)
	go func() { runErr <- tui.Run() }()

	snap := func() string {
		if w, h := screen.Size(); w != 110 || h != 30 {
			screen.SetSize(110, 30)
		}
		screen.Sync()
		w, h := screen.Size()
		var b strings.Builder
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, _, _, _ := screen.GetContent(x, y)
				b.WriteRune(r)
			}
			b.WriteByte('\n')
		}
		return b.String()
	}

	waitFor := func(substr string) string {
		var s string
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			s = snap()
			if strings.Contains(s, substr) {
				return s
			}
			time.Sleep(40 * time.Millisecond)
		}
		return s
	}

	// type "/h" as real key events so the changed-hook pipeline runs
	for _, r := range "/h" {
		tui.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	withPopup := waitFor("commands")
	var ptxt string
	var px, py, pw, ph, piw, pih int
	var nItems int
	tui.app.QueueUpdateDraw(func() {
		ptxt = tui.popup.GetText(true)
		px, py, pw, ph = tui.popup.GetRect()
		_, _, piw, pih = tui.popup.GetInnerRect()
		nItems = tui.root.GetItemCount()
	})
	t.Logf("popup text=%q rect=%d,%d %dx%d inner=%dx%d items=%d", ptxt, px, py, pw, ph, piw, pih, nItems)
	if !strings.Contains(withPopup, "/help") {
		t.Errorf("popup missing /help row:\n%s", withPopup)
	}

	// Esc clears input and the palette through the same pipeline
	tui.app.QueueEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	var gone string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		gone = snap()
		if !strings.Contains(gone, "commands") {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if strings.Contains(gone, "commands") {
		t.Errorf("popup did not hide after clearing:\n%s", gone)
	}

	tui.app.Stop()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, tcell.ErrTermNotFound) {
			t.Fatalf("run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("application did not stop")
	}
}

func TestScrollKeysMoveViewport(t *testing.T) {
	tui := NewTUI("openrouter", "test/model", false)
	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(110, 30)
	tui.app.SetScreen(screen)
	runErr := make(chan error, 1)
	go func() { runErr <- tui.Run() }()

	key := func(k tcell.Key, r rune) {
		tui.app.QueueEvent(tcell.NewEventKey(k, r, tcell.ModNone))
	}
	offset := func() int {
		row := -1
		tui.app.QueueUpdateDraw(func() { row, _ = tui.streamTV.GetScrollOffset() })
		return row
	}

	// fill the transcript with enough content to overflow several screens
	for i := 0; i < 60; i++ {
		tui.obs.writeCard(fmt.Sprintf("line %02d — filler for scrolling", i))
	}
	time.Sleep(300 * time.Millisecond)
	select {
	case e := <-runErr:
		t.Fatalf("app exited early: %v", e)
	default:
	}

	atBottom := offset()
	for i := 0; i < 4; i++ {
		key(tcell.KeyPgUp, 0)
		time.Sleep(30 * time.Millisecond)
	}
	up := offset()
	if up >= atBottom {
		t.Fatalf("PgUp did not scroll up: bottom=%d now=%d", atBottom, up)
	}

	for i := 0; i < 6; i++ {
		key(tcell.KeyPgDn, 0)
		time.Sleep(30 * time.Millisecond)
	}
	down := offset()
	if down <= up {
		t.Fatalf("PgDn did not scroll down: before=%d after=%d", up, down)
	}
	if !tui.core.feed.isPinned() {
		var row, vh, tail int
		tui.app.QueueUpdateDraw(func() {
			row, _ = tui.streamTV.GetScrollOffset()
			_, _, _, vh = tui.streamTV.GetInnerRect()
			tail = tui.streamTV.GetWrappedLineCount()
		})
		t.Fatalf("scrolling back to the tail should re-pin follow mode (row=%d vh=%d tail=%d pinned=%v)", row, vh, tail, tui.core.feed.isPinned())
	}

	tui.app.Stop()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, tcell.ErrTermNotFound) {
			t.Fatalf("run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("application did not stop")
	}
}

// Esc while a run is active must cancel it; idle Esc only clears input.
func TestEscCancelsActiveRun(t *testing.T) {
	tui := NewTUI("openrouter", "test/model", false)
	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(110, 30)
	tui.app.SetScreen(screen)
	runErr := make(chan error, 1)
	go func() { runErr <- tui.Run() }()
	time.Sleep(120 * time.Millisecond)

	if !tui.tryBeginRun() {
		t.Fatal("expected run to start")
	}
	cancelled := make(chan struct{})
	base := tui.cancelRun
	tui.cancelRun = func() { base(); close(cancelled) }

	tui.app.QueueEvent(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("esc did not cancel the active run")
	}

	tui.app.Stop()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, tcell.ErrTermNotFound) {
			t.Fatalf("run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("application did not stop")
	}
}

// The statusline right cluster must hug the right edge: gap math measures
// visible width, not tag bytes (regression: everything crammed left).
func TestFooterRightAlignment(t *testing.T) {
	tui := NewTUI("openrouter", "test/model", false)
	line := tui.renderFooter(110)
	vis := plainWidth(line)
	if vis != 110 {
		t.Fatalf("footer visible width %d, want 110: %q", vis, line)
	}
	if idx := strings.LastIndex(line, "esc stop"); idx >= 0 {
		t.Fatalf("esc stop shown while idle: %q", line)
	}
	if idx := strings.LastIndex(line, "0%"); idx < 90 {
		t.Fatalf("meter not right-aligned (idx %d): %q", idx, line)
	}
}

// Mouse wheel scrolls the transcript from anywhere on screen.
func TestWheelScrolls(t *testing.T) {
	tui := NewTUI("openrouter", "test/model", false)
	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(110, 30)
	tui.app.SetScreen(screen)
	runErr := make(chan error, 1)
	go func() { runErr <- tui.Run() }()
	time.Sleep(120 * time.Millisecond)

	for i := 0; i < 60; i++ {
		tui.obs.writeCard(fmt.Sprintf("filler %02d — enough text to wrap a little", i) + "\n")
	}
	time.Sleep(200 * time.Millisecond)

	offset := func() int {
		row := -1
		tui.app.QueueUpdateDraw(func() { row, _ = tui.streamTV.GetScrollOffset() })
		return row
	}
	atBottom := offset()
	var tail, vh int
	var pinned bool
	tui.app.QueueUpdateDraw(func() {
		tail = tui.core.feed.tailRow()
		vh = tui.core.feed.viewportH()
		pinned = tui.core.feed.isPinned()
	})
	t.Logf("pre-wheel: bottom=%d tail=%d vh=%d pinned=%v", atBottom, tail, vh, pinned)
	for i := 0; i < 5; i++ {
		tui.app.QueueEvent(tcell.NewEventMouse(50, 10, tcell.WheelUp, tcell.ModNone))
		time.Sleep(25 * time.Millisecond)
	}
	if up := offset(); up >= atBottom {
		t.Fatalf("wheel up did not scroll (bottom=%d now=%d tail=%d vh=%d pinned=%v)", atBottom, up, tail, vh, pinned)
	}

	tui.app.Stop()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, tcell.ErrTermNotFound) {
			t.Fatalf("run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("application did not stop")
	}
}

// Resumed sessions replay their transcript into the stream on startup.
func TestRenderHistory(t *testing.T) {
	tui := NewTUI("openrouter", "test/model", false)
	tui.agent = &Sigmoidh{
		messages: []litellm.Message{
			litellm.System("sys"),
			litellm.UserText("what changed?"),
			litellm.Assistant(
				litellm.ReasoningBlock{Text: "scan the diff first\nthen report"},
				litellm.TextBlock{Text: "I refactored the parser."},
				litellm.ToolUseBlock{ID: "t1", Name: "bash", Arguments: []byte(`{"command":"go test ./..."}`)},
			),
			{Role: litellm.RoleTool, Blocks: []litellm.Block{
				litellm.ToolResultBlock{ToolUseID: "t1", Content: []litellm.Block{litellm.Text("ok\n")}},
			}},
			litellm.UserText("thanks"),
		},
	}

	tui.renderHistory()
	out := tui.streamTV.GetText(true)
	for _, want := range []string{
		"› what changed?",
		"• thinking…",
		"▎ scan the diff first",
		"▎ then report",
		"I refactored the parser.",
		"• bash", "go test ./...",
		"└ done", "3 B",
		"› thanks",
		"· resumed 4 messages",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("history missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sys") {
		t.Fatalf("system prompt leaked into view:\n%s", out)
	}
}

// WIRING check: Run() itself must replay bound history (regression: the
// renderHistory call silently vanished from Run during a refactor).
func TestRunReplaysBoundHistory(t *testing.T) {
	tui := NewTUI("openrouter", "test/model", false)
	tui.agent = &Sigmoidh{
		messages: []litellm.Message{
			litellm.System("sys"),
			litellm.UserText("earlier question"),
			litellm.Assistant(litellm.TextBlock{Text: "earlier answer"}),
		},
	}
	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(110, 30)
	tui.app.SetScreen(screen)
	runErr := make(chan error, 1)
	go func() { runErr <- tui.Run() }()

	var all string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		screen.Sync()
		w, h := screen.Size()
		var b strings.Builder
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, _, _, _ := screen.GetContent(x, y)
				b.WriteRune(r)
			}
			b.WriteByte('\n')
		}
		all = b.String()
		if strings.Contains(all, "earlier answer") && strings.Contains(all, "resumed 2 messages") {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !strings.Contains(all, "› earlier question") || !strings.Contains(all, "earlier answer") {
		t.Fatalf("Run did not replay history:\n%s", all)
	}
	tui.app.Stop()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, tcell.ErrTermNotFound) {
			t.Fatalf("run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("application did not stop")
	}
}

// '?' from an empty idle prompt opens the shortcuts overlay; the next key
// closes it and types through. Footer no longer shows the tools count.
func TestShortcutsOverlay(t *testing.T) {
	tui := NewTUI("openrouter", "test/model", false)
	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(110, 30)
	tui.app.SetScreen(screen)
	runErr := make(chan error, 1)
	go func() { runErr <- tui.Run() }()
	time.Sleep(120 * time.Millisecond)

	snap := func() string {
		screen.Sync()
		w, h := screen.Size()
		var b strings.Builder
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, _, _, _ := screen.GetContent(x, y)
				b.WriteRune(r)
			}
			b.WriteByte('\n')
		}
		return b.String()
	}

	tui.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, '?', tcell.ModNone))
	var withOverlay string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		withOverlay = snap()
		if strings.Contains(withOverlay, "shortcuts") && strings.Contains(withOverlay, "alt+m") {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !strings.Contains(withOverlay, "toggle mouse wheel capture") {
		t.Fatalf("shortcuts overlay missing rows:\n%s", withOverlay)
	}

	// '?' also lands in the input (never swallowed)
	if tui.prompt.GetText() != "?" {
		t.Fatalf("? was swallowed: %q", tui.prompt.GetText())
	}

	// next key closes the overlay and continues the message
	tui.app.QueueEvent(tcell.NewEventKey(tcell.KeyRune, 'h', tcell.ModNone))
	time.Sleep(120 * time.Millisecond)
	after := snap()
	if strings.Contains(after, "toggle mouse wheel capture") {
		t.Fatalf("overlay did not close:\n%s", after)
	}
	if tui.prompt.GetText() != "?h" {
		t.Fatalf("typing through broken: %q", tui.prompt.GetText())
	}

	foot := tui.renderFooter(110)
	if strings.Contains(foot, "tools") {
		t.Fatalf("tools count still in statusline: %q", foot)
	}

	tui.app.Stop()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, tcell.ErrTermNotFound) {
			t.Fatalf("run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("application did not stop")
	}
}

// --no-sandbox surfaces in the TUI: warning card + persistent statusline chip.
func TestNoSandboxWarning(t *testing.T) {
	agenttools.SetSandboxEnabled(false)
	defer agenttools.SetSandboxEnabled(true)

	tui := NewTUI("openrouter", "test/model", false)
	tui.core.writeCard(cardBoot(VERSION, "openrouter", "test/model", false))
	tui.core.writeCard(cardNotice("sandbox disabled — bash/exec run with full host access"))

	out := tui.streamTV.GetText(true)
	if !strings.Contains(out, "sandbox disabled — bash/exec run with full host access") {
		t.Fatalf("warning card missing:\n%s", out)
	}
	if foot := tui.renderFooter(110); !strings.Contains(foot, "[no-sandbox]") {
		t.Fatalf("statusline chip missing: %q", foot)
	}
}

// nvim navigation: j/k lines, ctrl+d/u half pages, gg top, G bottom — all
// gated to an empty prompt (with text, j/k must type through instead).
func TestNvimScrollKeys(t *testing.T) {
	tui := NewTUI("openrouter", "test/model", false)
	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(110, 30)
	tui.app.SetScreen(screen)
	runErr := make(chan error, 1)
	go func() { runErr <- tui.Run() }()
	time.Sleep(120 * time.Millisecond)

	for i := 0; i < 60; i++ {
		tui.obs.writeCard(fmt.Sprintf("row %02d — nvim scroll filler", i) + "\n")
	}
	time.Sleep(200 * time.Millisecond)

	key := func(k tcell.Key, r rune) { tui.app.QueueEvent(tcell.NewEventKey(k, r, tcell.ModNone)) }
	offset := func() int {
		row := -1
		tui.app.QueueUpdateDraw(func() { row, _ = tui.streamTV.GetScrollOffset() })
		return row
	}
	tui.scrollBottom()
	bottom := offset()

	key(tcell.KeyRune, 'k')
	key(tcell.KeyRune, 'k')
	time.Sleep(80 * time.Millisecond)
	posK := offset()
	if posK >= bottom {
		t.Fatalf("k did not scroll up: bottom=%d now=%d", bottom, posK)
	}

	key(tcell.KeyRune, 'j')
	time.Sleep(60 * time.Millisecond)
	mid := offset()
	if mid <= posK {
		t.Fatalf("j did not scroll down: posK=%d mid=%d", posK, mid)
	}

	key(tcell.KeyCtrlU, 0)
	time.Sleep(60 * time.Millisecond)
	halfUp := offset()
	if halfUp >= mid {
		t.Fatalf("ctrl+u did not page up: %d", halfUp)
	}

	key(tcell.KeyRune, 'g')
	time.Sleep(30 * time.Millisecond)
	key(tcell.KeyRune, 'g')
	time.Sleep(80 * time.Millisecond)
	if top := offset(); top != 0 {
		t.Fatalf("gg did not reach top: %d", top)
	}
	if tui.core.feed.isPinned() {
		t.Fatal("gg should unpin follow mode")
	}

	key(tcell.KeyRune, 'G')
	time.Sleep(80 * time.Millisecond)
	if off := offset(); off < bottom-1 {
		t.Fatalf("G did not reach bottom: %d (want >= %d)", off, bottom-1)
	}
	if !tui.core.feed.isPinned() {
		t.Fatal("G should re-pin follow mode")
	}

	// with text in the prompt, j must type through, not scroll
	tui.prompt.SetText("j", true)
	before := offset()
	key(tcell.KeyRune, 'j')
	time.Sleep(80 * time.Millisecond)
	if got := tui.prompt.GetText(); got != "jj" {
		t.Fatalf("j typed through broken: %q", got)
	}
	if after := offset(); after != before {
		t.Fatal("j scrolled although prompt had text")
	}

	tui.app.Stop()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, tcell.ErrTermNotFound) {
			t.Fatalf("run error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("application did not stop")
	}
}

// Resumed answers must render exactly once — no raw/markdown doubles.
func TestRenderHistoryNoDuplicates(t *testing.T) {
	tui := NewTUI("openrouter", "test/model", false)
	tui.agent = &Sigmoidh{messages: []litellm.Message{
		litellm.UserText("summarize"),
		litellm.Assistant(litellm.TextBlock{Text: "**Sigmoidh** is a terminal agent."}),
	}}
	tui.renderHistory()
	out := tui.streamTV.GetText(true)
	if n := strings.Count(out, "Sigmoidh"); n != 1 {
		t.Fatalf("answer rendered %d times:\n%s", n, out)
	}
	if strings.Contains(out, "**") {
		t.Fatalf("raw markdown markers leaked:\n%s", out)
	}
}
