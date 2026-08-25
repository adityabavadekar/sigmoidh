package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agenttools "github.com/adityabavadekar/sigmoidh/tools"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/voocel/litellm"
)

// Full-screen TUI in the opencode school: one borderless transcript that owns
// the screen, an input box at the bottom, a single quiet statusline. No top
// chrome; panels appear only when they have something to say.

const uiFrameInterval = 66 * time.Millisecond // ~15 fps paint ceiling

// tuiCore: AgentObserver implementation shared by live + headless views
type tuiCore struct {
	feed *feed
	sink uiSink

	cfgMu    sync.RWMutex
	provider string
	model    string
	readOnly bool

	indent string // sub-agent nesting gutter
	quiet  bool   // hide final prose (child answers become the tool result)

	showThinking atomic.Bool
	showUsage    atomic.Bool

	deltaMu          sync.Mutex
	pending          strings.Builder // styled-but-unwritten stream bytes
	md               mdStream        // assistant markdown state
	norm             textNormalizer  // choppy-chunk reasoning prose (aggressive)
	textOpen         bool            // assistant stream currently live
	reasonBOL        bool            // reasoning cursor sits at a line start
	thinkHeaderShown bool            // • thinking… emitted (first delta arrived)

	diffMu       sync.Mutex
	pendingDiffs [][]diffRow
	planBoard    string

	runMu     sync.Mutex
	active    bool // a run is in flight (timer + spinner keyed off this)
	runStart  time.Time
	runFrozen time.Duration
	activity  atomic.Value // what the agent is doing right now (string)

	statMu           sync.Mutex
	turns, tools     int
	tokIn, tokOut    int
	tokTotal         int
	tokReason        int
	subagents        int
	ctxChars         int // char-based context estimate until real usage lands
	lastInputTokens  int
	outCharsSinceUse int
	liveIn           int // mid-stream usage (cumulative for the request in flight)
	liveOut          int
	liveTotal        int
	liveReason       int
}

var _ AgentObserver = (*tuiCore)(nil)

// NewDirectTuiObserver wires an observer straight into an existing TextView —
// used by tests and headless embedding; no application loop required.
func NewDirectTuiObserver(tv *tview.TextView) AgentObserver {
	c := &tuiCore{
		feed: newFeed(tv),
		sink: bareSink{},
	}
	c.showThinking.Store(true)
	return c
}

// SubagentObserver returns a nested view: indented under the parent gutter,
// chatty about tools, silent about prose.
func (c *tuiCore) SubagentObserver() AgentObserver {
	child := &tuiCore{
		feed:   c.feed,
		sink:   bareSink{}, // child events must not drive parent widgets
		indent: c.indent + "    ",
		quiet:  true,
	}
	child.showThinking.Store(true)
	return child
}

func (c *tuiCore) SetThinkingVisible(v bool) { c.showThinking.Store(v) }
func (c *tuiCore) ThinkingVisible() bool     { return c.showThinking.Load() }
func (c *tuiCore) SetUsageVisible(v bool)    { c.showUsage.Store(v) }
func (c *tuiCore) UsageVisible() bool        { return c.showUsage.Load() }

func (c *tuiCore) setProviderModel(provider, model string) {
	c.cfgMu.Lock()
	c.provider, c.model = provider, model
	c.cfgMu.Unlock()
}

func (c *tuiCore) providerModel() (string, string) {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.provider, c.model
}

// modelChip is the statusline identity: model, with provider when it differs
// from the obvious default naming.
func (c *tuiCore) modelChip() string {
	_, m := c.providerModel()
	return m
}

// stream plumbing
// emit writes to the stream feed and wakes the refresh loop.
func (c *tuiCore) emit(s string) {
	c.feed.write(s)
	c.sink.wake()
}

func (c *tuiCore) writeCard(s string) {
	c.emit(applyIndent(s, c.indent))
}

// pushStyled queues styled stream bytes; the refresh loop drains them.
func (c *tuiCore) pushStyled(s string) {
	if s == "" {
		return
	}
	c.deltaMu.Lock()
	c.pending.WriteString(s)
	c.deltaMu.Unlock()
}

// drainPending writes everything buffered so far (flusher + finalizers) and
// returns the drained bytes.
func (c *tuiCore) drainPending() string {
	c.deltaMu.Lock()
	buf := c.pending.String()
	c.pending.Reset()
	c.deltaMu.Unlock()
	if buf != "" {
		c.emit(applyIndent(buf, c.indent))
	}
	return buf
}

func (c *tuiCore) addCtxChars(n int) {
	if n <= 0 {
		return
	}
	c.statMu.Lock()
	c.ctxChars += n
	c.statMu.Unlock()
	c.sink.footerDirty()
}

// estContextTokens estimates current context size: real input tokens once the
// first usage report lands (plus streamed output since), chars÷4 before.
func (c *tuiCore) estContextTokens() int {
	c.statMu.Lock()
	defer c.statMu.Unlock()
	return c.estContextTokensLocked()
}

// estContextTokensLocked requires statMu to be held already.
func (c *tuiCore) estContextTokensLocked() int {
	if c.lastInputTokens > 0 {
		return c.lastInputTokens + c.outCharsSinceUse/4
	}
	return (c.ctxChars + c.outCharsSinceUse) / 4
}

func (c *tuiCore) elapsed() time.Duration {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if !c.active && c.runFrozen > 0 {
		return c.runFrozen
	}
	if c.active {
		return time.Since(c.runStart)
	}
	return 0
}

// setActivity records the live phase for the statusline; empty = generic.
func (c *tuiCore) setActivity(label string) { c.activity.Store(label) }

func (c *tuiCore) currentActivity() string {
	v, _ := c.activity.Load().(string)
	return v
}

func (c *tuiCore) isActive() bool {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	return c.active
}

func (c *tuiCore) setActive(on bool) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	switch {
	case on && !c.active:
		c.active = true
		c.runStart = time.Now()
		c.runFrozen = 0
	case !on && c.active:
		c.active = false
		c.runFrozen = time.Since(c.runStart)
	}
	c.sink.stateDirty()
}

// statsSnapshot is a consistent copy for widget renderers.
type statsSnapshot struct {
	turns, tools       int
	tokIn, tokOut      int
	tokTotal, tokR     int
	subagents          int
	ctxEst             int
	lastInputTokens    int
	outCharsSinceUsage int
	liveIn, liveOut    int // in-flight request usage (zero when idle)
	liveTotal, liveR   int
}

func (c *tuiCore) snapshot() statsSnapshot {
	c.statMu.Lock()
	defer c.statMu.Unlock()
	return statsSnapshot{
		turns: c.turns, tools: c.tools,
		tokIn: c.tokIn, tokOut: c.tokOut,
		tokTotal: c.tokTotal, tokR: c.tokReason,
		subagents:          c.subagents,
		ctxEst:             c.estContextTokensLocked(),
		lastInputTokens:    c.lastInputTokens,
		outCharsSinceUsage: c.outCharsSinceUse,
		liveIn:             c.liveIn, liveOut: c.liveOut,
		liveTotal: c.liveTotal, liveR: c.liveReason,
	}
}

// observer events
func (c *tuiCore) OnBootstrap(version, provider, model string, readOnly bool) {
	c.cfgMu.Lock()
	c.provider, c.model, c.readOnly = provider, model, readOnly
	c.cfgMu.Unlock()
	c.writeCard(cardBoot(version, provider, model, readOnly))
	c.sink.footerDirty()
}

func (c *tuiCore) OnRequestStart() {
	c.statMu.Lock()
	c.liveIn, c.liveOut, c.liveTotal, c.liveReason = 0, 0, 0, 0
	c.statMu.Unlock()
	c.setActivity("")
	c.setActive(true)
}

func (c *tuiCore) OnRequestDone() {}

func (c *tuiCore) OnThinkingStart() {
	c.setActivity("thinking")
	c.reasonBOL = true
	c.thinkHeaderShown = false
	// header waits for the first delta: empty-reasoning turns print nothing
}

func (c *tuiCore) OnReasoningDelta(text string) {
	if !c.showThinking.Load() || c.quiet {
		return
	}
	if !c.thinkHeaderShown && !c.quiet && c.showThinking.Load() {
		c.writeCard(cardThinkingStart())
		c.thinkHeaderShown = true
	}
	norm := c.norm.normalize(text)
	// quiet left rail: reasoning reads as a contained quote block
	rail := "  " + tg(pal.dim) + "▎" + tagReset + " "
	styled := ""
	if c.reasonBOL && norm != "" {
		styled = rail
		c.reasonBOL = false
	}
	styled += tg(pal.violet) + escTag(norm) + tagReset
	if strings.HasSuffix(norm, "\n") {
		styled += "\n"
		c.reasonBOL = true
	}
	c.pushStyled(styled)
}

func (c *tuiCore) OnThinkingEnd(took time.Duration) {
	if !c.thinkHeaderShown || c.quiet || !c.showThinking.Load() {
		return // empty-reasoning turn: nothing was opened, nothing to close
	}
	if buf := c.drainPending(); buf != "" && !strings.HasSuffix(buf, "\n") {
		c.emit("\n")
	}
	c.writeCard(cardThinkingEnd(took))
}

func (c *tuiCore) OnAssistantTextStart() {
	c.setActivity("responding")
	if c.quiet {
		return
	}
	c.textOpen = true
}

func (c *tuiCore) OnAssistantDelta(text string) {
	if c.quiet || !c.textOpen {
		return
	}
	c.pushStyled(c.md.Feed(text))
}

// OnAssistantText finalizes the answer block. Deltas already rendered the
// body live, so this only closes open markdown state (fences, emphasis).
func (c *tuiCore) OnAssistantText(text string) {
	if c.quiet {
		return
	}
	c.addCtxChars(len(text))
	if c.textOpen {
		c.textOpen = false
		c.drainPending()
		c.emit(c.md.Close())
		if text != "" {
			c.emit("\n")
		}
		return
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	var m mdStream
	c.emit(m.Feed(strings.TrimRight(text, "\n") + "\n"))
	c.emit(m.Close())
}

func (c *tuiCore) OnToolCall(name string, args json.RawMessage) {
	target, counts := toolCallSummary(name, args)
	isCmd := name == "bash" || name == "exec"

	c.setActivity("running " + name)

	c.statMu.Lock()
	c.tools++
	c.statMu.Unlock()

	c.diffMu.Lock()
	c.pendingDiffs = append(c.pendingDiffs, parseDiffRows(name, args))
	c.diffMu.Unlock()

	c.writeCard(cardToolCall(name, target, counts, isCmd))
	switch {
	case isCmd && target != "":
		c.writeCard(gutterBlock(target))
	case name == "subagent":
		var sa subagentArgs
		if json.Unmarshal(args, &sa) == nil && strings.TrimSpace(sa.Prompt) != "" {
			c.writeCard(gutterBlock("task · " + strings.TrimSpace(sa.Prompt)))
		}
	}
	c.sink.stateDirty()
}

func (c *tuiCore) OnToolResult(name, out string, isErr bool, took time.Duration) {
	var rows []diffRow
	c.diffMu.Lock()
	if len(c.pendingDiffs) > 0 {
		rows, c.pendingDiffs = c.pendingDiffs[0], c.pendingDiffs[1:]
	}
	if name == "plan" {
		c.planBoard = out
	}
	c.diffMu.Unlock()

	c.setActivity("") // tool settled; next event names the phase
	c.addCtxChars(len(out))
	if !isErr && len(rows) > 0 {
		c.emit(renderDiffBands(rows, maxInt(20, c.feed.innerWidth()-6)))
	}
	c.emit(cardToolResultLine(name, isErr, len(out), took))
	switch {
	case isErr:
		c.emit(gutterBlock(out) + "\n")
	case name == "plan":
		c.sink.planDirty()
	case isShortOutput(out):
		c.emit(gutterBlock(out) + "\n")
	default:
		c.emit("\n")
	}
	c.sink.footerDirty()
}

func (c *tuiCore) OnRetryWait(provider string, attempt int, delay time.Duration) {
	c.setActivity("retrying")
	c.writeCard(cardRetry(provider, attempt, delay))
}

func (c *tuiCore) OnProviderFallback(provider, model string) {
	c.setProviderModel(provider, model)
	c.writeCard(cardFallback(provider, model))
	c.sink.footerDirty()
}

func (c *tuiCore) OnTriggerFired(message string) {
	c.writeCard(cardTrigger(message))
}

// OnStreamUsage receives cumulative mid-stream usage for the request in
// flight (Anthropic reports input at message_start, then output deltas on
// every message_delta). Counters tick live; OnTurnUsage reconciles the final
// numbers so nothing is counted twice.
func (c *tuiCore) OnStreamUsage(usage litellm.Usage) {
	c.statMu.Lock()
	c.liveIn = usage.InputTokens
	c.liveOut = usage.OutputTokens
	c.liveTotal = usage.TotalTokens
	c.liveReason = usage.ReasoningTokens
	if usage.InputTokens > 0 && c.lastInputTokens == 0 {
		// real context size lands with message_start, long before turn end —
		// the context meter gets accurate immediately instead of afterwards
		c.lastInputTokens = usage.InputTokens
	}
	c.statMu.Unlock()
	c.sink.footerDirty()
}

func (c *tuiCore) OnTurnUsage(usage litellm.Usage) {
	c.statMu.Lock()
	c.turns++
	c.tokIn += usage.InputTokens
	c.tokOut += usage.OutputTokens
	c.tokTotal += usage.TotalTokens
	c.tokReason += usage.ReasoningTokens
	c.lastInputTokens = usage.InputTokens
	c.outCharsSinceUse = 0
	c.liveIn, c.liveOut, c.liveTotal, c.liveReason = 0, 0, 0, 0 // folded in above
	c.statMu.Unlock()

	if c.showUsage.Load() && usage.HasTokens() {
		c.writeCard(cardStatsLine(usage))
	}
	c.sink.footerDirty()
}

func (c *tuiCore) OnRunComplete(stats *RunStats) {
	c.setActive(false)
	c.writeCard(cardDone(stats))
}

func (c *tuiCore) OnPrintUsageStats(stats *RunStats) {
	c.writeCard(cardUsageGrid(stats))
}

// plan strip (appears only when a plan exists)
// planBoardText returns the styled board or "" when there is nothing to show.
func (c *tuiCore) planBoardText() string {
	c.diffMu.Lock()
	board := c.planBoard
	c.diffMu.Unlock()
	stripped := strings.TrimSpace(stripANSICodes(board))
	if stripped == "" || strings.Contains(stripped, "(plan empty)") {
		return ""
	}
	var b strings.Builder
	b.WriteString(" " + tgBold + tg(pal.dim) + "plan" + tagReset + "\n")
	for _, ln := range strings.Split(strings.TrimRight(stripANSICodes(board), "\n"), "\n") {
		num, label, done, active := parsePlanRow(ln)
		row := strings.Join(strings.Fields(num+" "+label), " ")
		if row == "" {
			continue
		}
		glyphC, glyph := pal.dim, "○"
		switch {
		case done:
			glyphC, glyph = pal.mint, "✓"
		case active:
			glyphC, glyph = pal.focus, "▶"
		}
		b.WriteString(" " + tg(glyphC) + glyph + tagReset + tg(pal.dim) + " · " + tagReset + escTag(row) + "\n")
	}
	return b.String()
}

// statusline
// One quiet line: left = model chip · cwd, right = activity · counters · meter.

func (t *TUI) renderFooter(width int) string {
	left := tg(pal.focus) + t.core.modelChip() + tagReset
	if t.core.readOnlyRO() {
		left += " " + tg(pal.amber) + "[ro]" + tagReset
	}
	if !agenttools.SandboxEnabled() {
		left += " " + tg(pal.amber) + "[no-sandbox]" + tagReset
	}
	left += tg(pal.dim) + "  " + shortCwd() + tagReset

	var groups []string
	switch {
	case t.core.isActive():
		frame := spinnerFrames[int(t.core.elapsed().Milliseconds()/90)%len(spinnerFrames)]
		label := t.core.currentActivity()
		if label == "" {
			label = "waiting" // request in flight, first token not here yet
		}
		groups = append(groups, tg(pal.brand)+frame+" "+label+
			fmt.Sprintf(" (%.0fs)", t.core.elapsed().Seconds())+tagReset)
	default:
		groups = append(groups, tg(pal.dim)+"○ idle"+tagReset)
	}
	if !t.core.isActive() {
		groups = append(groups, tg(pal.dim)+"? shortcuts"+tagReset)
	}

	snap := t.core.snapshot()
	tokTotal := snap.tokTotal + snap.liveTotal // live ticks during streaming
	groups = append(groups, tg(pal.dim)+
		fmt.Sprintf("turns %d · %s tok", snap.turns, humanNumber(tokTotal))+tagReset)

	bar, _, pct := ctxMeterBar(snap.ctxEst, maxTokens)
	groups = append(groups, bar+" "+strconv.Itoa(pct)+"%")

	if t.mouseOn {
		groups = append(groups, tg(pal.dim)+"mouse"+tagReset)
	} else if t.scrollable() {
		// content overflows the viewport: advertise the keys, since the
		// wheel is deliberately silent while selection-first mode is on
		groups = append(groups, tg(pal.dim)+"pgup/pgdn scroll · alt+m wheel"+tagReset)
	}
	if t.core.isActive() {
		groups = append(groups, tg(pal.dim)+"esc stop"+tagReset)
	}
	if n := t.queuedCount(); n > 0 {
		groups = append(groups, tg(pal.focus)+fmt.Sprintf("%d queued", n)+tagReset)
	}

	right := strings.Join(groups, "   ")
	gap := width - plainWidth(left) - plainWidth(right) - 2
	if gap < 2 {
		gap = 2
	}
	return " " + left + strings.Repeat(" ", gap) + right + " "
}

// scrollable reports whether the transcript currently overflows its viewport.
func (t *TUI) scrollable() bool {
	tail := t.core.feed.tailRow()
	vh := t.core.feed.viewportH()
	return tail > 0 && vh > 0 && tail > vh
}

func (t *TUI) queuedCount() int {
	t.queueMu.Lock()
	defer t.queueMu.Unlock()
	return len(t.queue)
}

// readOnlyRO is a race-free read of the read-only flag.
func (c *tuiCore) readOnlyRO() bool {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.readOnly
}

func shortCwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return "?"
	}
	if home := homeDir(); home != "." && strings.HasPrefix(dir, home) {
		dir = "~" + strings.TrimPrefix(dir, home)
	}
	return dir
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// dirty flags + wake channel
type dirtyFlags struct {
	mu     sync.Mutex
	footer bool
	plan   bool
}

// drain swaps the pending flags for a snapshot; safe from any goroutine.
func (d *dirtyFlags) drain() (footer, plan bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	footer, plan = d.footer, d.plan
	d.footer, d.plan = false, false
	return
}

func (d *dirtyFlags) set(f *bool) {
	d.mu.Lock()
	*f = true
	d.mu.Unlock()
}

type uiSink interface {
	footerDirty()
	planDirty()
	stateDirty()
	wake()
}

type bareSink struct{}

func (bareSink) footerDirty() {}
func (bareSink) planDirty()   {}
func (bareSink) stateDirty()  {}
func (bareSink) wake()        {}

type liveSink struct {
	flags dirtyFlags
	dirty atomic.Bool
}

func (l *liveSink) footerDirty() { l.flags.set(&l.flags.footer) }
func (l *liveSink) planDirty()   { l.flags.set(&l.flags.plan) }
func (l *liveSink) stateDirty()  { l.flags.set(&l.flags.footer); l.wake() }

// wake marks work pending; the frame ticker does the actual painting so
// redraw rate never exceeds uiFrameInterval no matter how hot events run.
func (l *liveSink) wake() { l.dirty.Store(true) }

// TUI
type TUI struct {
	core *tuiCore
	obs  *tuiCore // observer handed to the agent (main.go wiring)
	app  *tview.Application

	streamTV    *tview.TextView
	planStrip   *tview.TextView
	popup       *tview.TextView
	lastPopupH  int
	shortcuts   *tview.TextView
	shortcutsUp bool
	footer      *tview.TextView
	popupUp     bool
	prompt      *tview.TextArea
	root        *tview.Flex

	mouseOn    bool     // mouse capture: wheel scrolling vs native text selection
	queue      []string // prompts waiting for the active run to finish
	queueMu    sync.Mutex
	accel      int // scroll multiplier from rapid keypresses
	lastScroll time.Time
	lastDir    int
	pendG      time.Time // waiting for the second 'g' of gg (zero = no)

	agent     *Sigmoidh
	baseCtx   context.Context
	runCtx    context.Context
	cancelRun context.CancelFunc
	stopTick  chan struct{}
	uiActive  atomic.Bool // true while the tview event loop is serving

	lastFooter string
}

// NewTUI builds the full widget tree; call Run to start the event loop.
func NewTUI(provider, model string, readOnly bool) *TUI {
	applyTheme()
	app := tview.NewApplication()

	t := &TUI{app: app, baseCtx: context.Background(), stopTick: make(chan struct{})}
	t.core = &tuiCore{sink: &liveSink{}}
	t.core.setProviderModel(provider, model)
	t.core.readOnly = readOnly
	t.core.showThinking.Store(true)
	t.obs = t.core
	t.buildLayout()
	return t
}

// NewHeadlessTUI constructs the same widgets without starting the app loop —
// for tests and embedded rendering.
func NewHeadlessTUI(provider, model string, readOnly bool) *TUI {
	return NewTUI(provider, model, readOnly)
}

func (t *TUI) BindAgent(smh *Sigmoidh) { t.agent = smh }

func (t *TUI) buildLayout() {
	textView := func(dynamic bool, bg tcell.Color) *tview.TextView {
		tv := tview.NewTextView()
		tv.SetDynamicColors(dynamic)
		tv.SetBackgroundColor(bg)
		return tv
	}

	t.streamTV = textView(true, pal.panel)
	t.core.feed = newFeed(t.streamTV)
	// The transcript is uncaged: no border, just breathing room. Diffs and
	// code blocks get the full width.
	t.streamTV.SetBorderPadding(0, 0, 3, 2)

	t.planStrip = textView(true, pal.panel)
	t.popup = textView(true, pal.panel)
	t.popup.SetBorder(true)
	t.shortcuts = textView(true, pal.panel)
	t.shortcuts.SetBorder(true)
	t.footer = textView(true, pal.void)
	t.footer.SetBorderPadding(0, 0, 3, 0) // align with stream text

	body := t.streamTV

	t.prompt = tview.NewTextArea().
		SetPlaceholder("describe a task…").
		SetWrap(false)
	t.prompt.SetPlaceholderStyle(tcell.StyleDefault.Background(pal.panel).Foreground(pal.dim))
	t.prompt.SetTextStyle(tcell.StyleDefault.Background(pal.panel).Foreground(pal.text))
	t.prompt.SetBackgroundColor(pal.panel)
	t.prompt.SetBorderPadding(0, 0, 2, 1) // text floats inside the border
	t.prompt.SetBorder(true).
		SetTitleAlign(tview.AlignLeft).
		SetBorderColor(pal.chrome)

	t.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(body, 0, 1, false).        // 0: transcript owns the screen
		AddItem(t.planStrip, 0, 0, false). // hidden until a plan exists
		AddItem(t.popup, 0, 0, false).     // slash palette, hidden by default
		AddItem(t.shortcuts, 0, 0, false). // ? overlay
		AddItem(t.prompt, 3, 0, false).
		AddItem(t.footer, 1, 0, false)

	t.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		screen.SetStyle(tcell.StyleDefault.Background(pal.void).Foreground(pal.text))
		screen.Clear()
		return false
	})
}

// paint applies one consistent snapshot of core state to the widgets. It is
// the ONLY place widgets mutate off the event goroutine (via QueueUpdateDraw),
// which also makes it the safe spot for tview's unlocked scroll APIs.
func (t *TUI) paint(footer, plan bool) {
	if !t.uiActive.Load() {
		return
	}
	t.app.QueueUpdateDraw(func() {
		x, y, w, h := t.streamTV.GetInnerRect()
		_ = x
		_ = y
		t.core.feed.noteWidth(w)
		t.core.feed.setViewportH(h)
		t.core.feed.setTail(t.streamTV.GetWrappedLineCount())
		if plan {
			if txt := t.core.planBoardText(); txt != "" {
				rows := strings.Count(txt, "\n")
				t.planStrip.SetText(txt)
				t.root.ResizeItem(t.planStrip, minInt(rows+2, 7), 0)
			} else {
				t.root.ResizeItem(t.planStrip, 0, 0)
			}
		}
		if footer {
			fw := 80
			if _, _, w2, _ := t.footer.GetInnerRect(); w2 > 8 {
				fw = w2
			}
			next := t.renderFooter(fw)
			if next != t.lastFooter { // skip identical frames: less cell churn
				t.lastFooter = next
				t.footer.SetText(next)
			}
		}
		if t.core.feed.isPinned() {
			t.streamTV.ScrollToEnd()
		}
	})
}

// tickLoop coalesces every screen mutation behind a fixed paint budget:
// dirty bits from observer events plus the animation clock, at ~15 fps max.
func (t *TUI) tickLoop() {
	ticker := time.NewTicker(uiFrameInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.stopTick:
			return
		case <-ticker.C:
			t.refresh()
		}
	}
}

// refresh drains buffers and repaints whatever changed this frame.
func (t *TUI) refresh() {
	t.core.drainPending()
	sink := t.core.sink.(*liveSink)
	footer, plan := sink.flags.drain()
	if sink.dirty.Swap(false) || t.core.isActive() {
		footer = true
	}
	if footer || plan {
		t.paint(footer, plan)
	}
}

// interaction
func (t *TUI) installInputHandlers() {
	t.app.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyCtrlD:
			if t.prompt.GetText() == "" { // nvim: half page down
				t.scrollFeed(1)
				return nil
			}
		case tcell.KeyCtrlU:
			if t.prompt.GetText() == "" { // nvim: half page up
				t.scrollFeed(-1)
				return nil
			}
		case tcell.KeyRune:
			if t.prompt.GetText() == "" && !t.core.isActive() {
				switch ev.Rune() {
				case 'j': // nvim: line down
					t.scrollLines(3)
					return nil
				case 'k': // nvim: line up
					t.scrollLines(-3)
					return nil
				case 'G': // nvim: bottom
					t.scrollBottom()
					return nil
				case 'g':
					if !t.pendG.IsZero() && time.Since(t.pendG) < 400*time.Millisecond {
						t.pendG = time.Time{}
						t.scrollTop()
						return nil
					}
					t.pendG = time.Now() // swallow first 'g', nvim-style
					return nil
				}
			}
			if ev.Rune() == '?' && !t.shortcutsUp && !t.core.isActive() && t.prompt.GetText() == "" {
				t.showShortcuts() // preview only; the '?' still types below
			} else if t.shortcutsUp {
				t.hideShortcuts()
			}
			return ev
		case tcell.KeyEscape:
			if t.shortcutsUp {
				t.hideShortcuts()
				return nil
			}
			if t.core.isActive() { // esc interrupts the running agent
				if c := t.cancelRun; c != nil {
					c()
				}
				return nil
			}
			if t.prompt.GetText() != "" {
				t.prompt.SetText("", true)
				t.updateSlashPopup("") // SetText bypasses the changed hook
			}
			return nil

		case tcell.KeyCtrlC:
			if t.core.isActive() {
				if t.cancelRun != nil {
					t.cancelRun()
				}
				return nil
			}
			t.app.Stop()
			return nil
		case tcell.KeyTab:
			t.completeSlash()
			return nil
		case tcell.KeyPgUp:
			t.scrollFeed(-10)
			return nil
		case tcell.KeyPgDn:
			t.scrollFeed(10)
			return nil
		case tcell.KeyHome:
			t.scrollTop()
			return nil
		case tcell.KeyEnd:
			t.scrollBottom()
			return nil
		case tcell.KeyUp:
			if strings.Contains(t.prompt.GetText(), "\n") {
				return ev // cursor movement inside a multiline prompt
			}
			t.scrollLines(-3)
			return nil
		case tcell.KeyDown:
			if strings.Contains(t.prompt.GetText(), "\n") {
				return ev
			}
			t.scrollLines(3)
			return nil
		}
		return ev
	})

	t.prompt.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEnter && ev.Modifiers()&tcell.ModAlt == 0 {
			t.submit()
			return nil
		}
		return ev // alt+enter falls through: TextArea inserts the newline
	})
	t.prompt.SetChangedFunc(func() {
		txt := t.prompt.GetText()
		if t.shortcutsUp && (len(txt) > 1 || !strings.HasPrefix(txt, "?")) {
			t.hideShortcuts() // kept typing past the preview '?'
		}
		t.updateSlashPopup(txt)
	})
}

// scrollTop / scrollBottom jump the transcript; direct UI-loop mutation.
func (t *TUI) scrollTop() {
	t.core.feed.setPinned(false)
	t.streamTV.ScrollToBeginning()
}

func (t *TUI) scrollBottom() {
	t.core.feed.setPinned(true)
	t.streamTV.ScrollToEnd()
}

// toggleMouse flips wheel capture. Off (default) keeps native text
// selection; on, the wheel scrolls the transcript (shift+drag selects).
func (t *TUI) toggleMouse() {
	t.mouseOn = !t.mouseOn
	t.app.EnableMouse(t.mouseOn)
	if t.mouseOn {
		t.core.writeCard(cardNotice("mouse capture on — wheel scrolls · shift+drag selects"))
	} else {
		t.core.writeCard(cardNotice("mouse capture off — text selects natively · pgup/pgdn scrolls"))
	}
}

// scrollFeed pages by half a screen with momentum; arrow keys use the
// fixed-step variant. Repeated presses in one direction accelerate up to 6×.
func (t *TUI) scrollFeed(dir int) {
	t.scroll(dir, func(h int) int { return maxInt(3, h/2) })
}

func (t *TUI) scrollLines(n int) {
	if n == 0 {
		return
	}
	dir := 1
	if n < 0 {
		dir = -1
	}
	t.scroll(dir, func(int) int { return maxInt(1, n*dir) * dir * dir }) // |n|
}

// scroll moves the viewport and re-pins follow mode when the tail clamps —
// bottom detection via clamp means no full line-index rebuild per keypress.
// Runs directly on the UI goroutine: QueueUpdateDraw from inside a key
// handler would wait on the loop that is currently dispatching us (deadlock).
func (t *TUI) scroll(dir int, stepFn func(height int) int) {
	_, _, w, _ := t.streamTV.GetInnerRect()
	t.core.feed.noteWidth(w)
	_, _, _, h := t.streamTV.GetInnerRect()
	step := stepFn(h)

	now := time.Now()
	if now.Sub(t.lastScroll) < 350*time.Millisecond && t.lastDir == dir {
		t.accel = minInt(t.accel*2, 6)
	} else {
		t.accel = 1
	}
	t.lastScroll, t.lastDir = now, dir
	step *= t.accel

	row, _ := t.streamTV.GetScrollOffset()
	target := row + dir*step
	if target < 0 {
		target = 0
	}
	t.streamTV.ScrollTo(target, 0)

	// Re-pin follow mode when a downward scroll reaches the tail. The tail
	// height comes from a 200ms-stale cache: GetWrappedLineCount forces a
	// full line-index parse, so held-key bursts must not pay it per press.
	switch {
	case dir > 0:
		tail := t.core.feed.tailFresh(200 * time.Millisecond)
		atTail := tail > 0 && target+t.core.feed.viewportH() >= tail
		t.core.feed.setPinned(atTail)
	case dir < 0:
		t.core.feed.setPinned(false)
	}
}

func (t *TUI) updateSlashPopup(text string) {
	line := strings.TrimSpace(text)
	matches := slashMatches(line)
	want := strings.HasPrefix(line, "/") && len(matches) > 0
	if want {
		var b strings.Builder
		b.WriteString(tg(pal.dim) + " commands" + tagReset + "\n")
		for _, m := range matches {
			b.WriteString(" " + tg(pal.focus) + m.name + tagReset + tg(pal.dim) + "  " + m.desc + tagReset + "\n")
		}
		t.popup.SetText(b.String())
	}
	// content rows + header, framed by the popup's own border
	h := 0
	if want {
		h = minInt(len(matches)+1+2, 10)
	}
	if (want && t.popupUp && t.lastPopupH == h) || (!want && !t.popupUp) {
		return // nothing changed worth a relayout
	}
	t.root.ResizeItem(t.popup, h, 0)
	t.popupUp = want
	t.lastPopupH = h
}

// showShortcuts lists the keybindings in an overlay above the prompt.
func (t *TUI) showShortcuts() {
	rows := []string{
		"enter", "send prompt",
		"alt+enter", "newline in prompt",
		"tab", "complete command",
		"esc", "stop run · clear input · close",
		"ctrl+c", "stop run · quit when idle",
		"pgup/pgdn", "scroll half page (accelerates)",
		"up/down", "scroll 3 lines",
		"home/end", "top / bottom (re-pins follow)",
		"j/k · gg/G", "nvim scroll lines · top/bottom",
		"ctrl+d/u", "nvim half page down/up (empty prompt)",
		"alt+m", "toggle mouse wheel capture",
	}
	var b strings.Builder
	b.WriteString(" " + tgBold + tg(pal.dim) + "shortcuts" + tagReset + "\n")
	for i := 0; i < len(rows); i += 2 {
		b.WriteString(" " + tg(pal.focus) + rows[i] + tagReset +
			tg(pal.dim) + "  " + rows[i+1] + tagReset + "\n")
	}
	t.shortcuts.SetText(b.String())
	t.root.ResizeItem(t.shortcuts, minInt(len(rows)+3, 14), 0)
	t.shortcutsUp = true
}

func (t *TUI) hideShortcuts() {
	if t.shortcutsUp {
		t.root.ResizeItem(t.shortcuts, 0, 0)
		t.shortcutsUp = false
	}
}

func (t *TUI) hideSlashPopup() {
	if t.popupUp {
		t.root.ResizeItem(t.popup, 0, 0)
		t.popupUp = false
	}
}

// completeSlash expands the shared prefix on Tab; discovery lives in the
// popup now, so ambiguous sets no longer print menu cards into the stream.
func (t *TUI) completeSlash() {
	line := strings.TrimSpace(t.prompt.GetText())
	if !strings.HasPrefix(line, "/") {
		return
	}
	matches := slashMatches(line)
	if len(matches) == 0 {
		return
	}
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.name
	}
	completed := longestCommonPrefix(names)
	if len(matches) == 1 {
		completed = names[0]
	}
	if completed != line {
		t.prompt.SetText(completed+" ", true)
	}
}

// queued prompts
// Prompts parked behind an active run are also appended to a sidecar file, so
// a crash (or quit) never silently eats them: on the next start they reload
// into the queue. The file mirrors t.queue exactly — append on enqueue,
// rewrite on dequeue, gone when empty.

type pendingPrompt struct {
	Ts   time.Time `json:"ts"`
	Text string    `json:"text"`
}

func pendingPromptsPath() string {
	if p := os.Getenv("SIGMOIDH_PENDING_PROMPTS"); p != "" {
		return p // test hook
	}
	return filepath.Join(homeDir(), ".sigmoidh", "pending_prompts.jsonl")
}

func (t *TUI) persistQueueAppend(line string) {
	f, err := os.OpenFile(pendingPromptsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logWarn("queue persist failed", "op", "append", "err", err)
		return
	}
	defer f.Close()
	data, err := json.Marshal(pendingPrompt{Ts: time.Now(), Text: line})
	if err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err != nil {
		logWarn("queue persist failed", "op", "append", "err", err)
	}
	logInfo("prompt queued", "position", len(t.queue), "chars", len(line))
}

func (t *TUI) persistQueueRewrite() {
	t.queueMu.Lock()
	remaining := make([]string, len(t.queue))
	copy(remaining, t.queue)
	t.queueMu.Unlock()

	path := pendingPromptsPath()
	if len(remaining) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			logWarn("queue persist failed", "op", "clear", "err", err)
		}
		return
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		logWarn("queue persist failed", "op", "rewrite", "err", err)
		return
	}
	enc := json.NewEncoder(f)
	for _, p := range remaining {
		if err := enc.Encode(pendingPrompt{Ts: time.Now(), Text: p}); err != nil {
			break
		}
	}
	f.Close()
	if err := os.Rename(tmp, path); err != nil {
		logWarn("queue persist failed", "op", "rewrite", "err", err)
	}
}

// loadPendingPrompts recovers prompts persisted by a previous process.
func loadPendingPrompts() []string {
	f, err := os.Open(pendingPromptsPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := json.NewDecoder(f)
	for {
		var p pendingPrompt
		if err := sc.Decode(&p); err != nil || strings.TrimSpace(p.Text) == "" {
			break
		}
		out = append(out, p.Text)
	}
	logInfo("recovered queued prompts from disk", "count", len(out))
	return out
}

// submit routes enter: exit words quit, slash commands share the REPL
// handler, and prompts run one at a time — anything submitted while a run
// is active is queued and fires automatically when the run finishes.
func (t *TUI) submit() {
	line := strings.TrimSpace(t.prompt.GetText())
	if line == "" {
		return
	}
	t.prompt.SetText("", true)
	t.hideSlashPopup()
	t.hideShortcuts()
	t.core.feed.setPinned(true)

	if bareExitWords[line] {
		t.quit()
		return
	}
	if strings.HasPrefix(line, "/") {
		if line == "/mouse" || strings.HasPrefix(line, "/mouse ") {
			switch strings.TrimSpace(strings.TrimPrefix(line, "/mouse")) {
			case "on":
				if !t.mouseOn {
					t.toggleMouse()
				}
			case "off":
				if t.mouseOn {
					t.toggleMouse()
				}
			default:
				t.toggleMouse()
			}
			return
		}
		t.runSlash(line)
		return
	}
	if !t.tryBeginRun() {
		t.queueMu.Lock()
		t.queue = append(t.queue, line)
		n := len(t.queue)
		t.queueMu.Unlock()
		t.persistQueueAppend(line)
		t.core.writeCard(cardQueued(line, n))
		t.core.sink.footerDirty()
		return
	}
	t.startRun(line)
}

// startRun echoes the prompt and hands it to the agent on a worker.
func (t *TUI) startRun(line string) {
	t.core.writeCard(cardUserPrompt(line, t.core.feed.innerWidth()))
	t.core.addCtxChars(len(line))
	if t.agent != nil {
		notifyUserInput(t.agent.observer, line)
	}
	go t.runAgent(line)
}

func (t *TUI) tryBeginRun() bool {
	t.core.runMu.Lock()
	defer t.core.runMu.Unlock()
	if t.core.active {
		return false
	}
	t.core.active = true
	t.core.runStart = time.Now()
	t.core.runFrozen = 0
	ctx, cancel := context.WithCancel(t.baseCtx)
	t.runCtx, t.cancelRun = ctx, cancel
	return true
}

func (t *TUI) endRun(err error) {
	if t.cancelRun != nil {
		t.cancelRun()
		t.cancelRun = nil
	}
	t.core.drainPending()
	if err != nil {
		t.core.writeCard(cardError(err))
	} else if t.agent != nil {
		snapshot := *t.agent.stats
		t.core.writeCard(cardDone(&snapshot))
	}
	t.core.setActive(false)
	t.core.sink.footerDirty()

	t.queueMu.Lock()
	var next string
	if len(t.queue) > 0 {
		next = t.queue[0]
		t.queue = t.queue[1:]
	}
	t.queueMu.Unlock()
	t.persistQueueRewrite() // keep the sidecar in sync after every drain
	if next != "" {
		if t.tryBeginRun() { // serial: one queued prompt per finished run
			t.startRun(next)
		}
	}
}

// runAgent drives one prompt through the agent off the UI goroutine.
func (t *TUI) runAgent(line string) {
	var err error
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: %v", r)
		}
		t.endRun(err)
	}()
	if t.agent == nil {
		err = errors.New("no agent bound")
		return
	}
	err = t.agent.Run(t.runCtx, line)
	if err == nil {
		t.agent.fireRunCompleteTriggers()
	}
}

// runSlash executes REPL slash commands unchanged, capturing their stdout /
// stderr chatter so it renders as a quiet card instead of trashing the screen.
func (t *TUI) runSlash(line string) {
	if t.agent == nil {
		t.core.writeCard(cardError(errors.New("no agent bound")))
		return
	}
	out, err := captureOutput(func() error {
		return handleSlashCommand(t.baseCtx, t.agent, line)
	})
	if errors.Is(err, errExitRequest) {
		t.quit()
		return
	}
	if err != nil {
		t.core.writeCard(cardError(err))
		return
	}
	if card := cardDimBlock(out); card != "" {
		t.core.writeCard(card)
	}
}

// captureOutput swaps the process std streams around fn and returns whatever
// was printed; the swap is restored even when fn panics. Safe here: TUI mode
// has no TerminalLogger writing concurrently, and slash output is small.
func captureOutput(fn func() error) (out string, err error) {
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		return "", fn()
	}
	os.Stdout, os.Stderr = w, w
	defer func() {
		w.Close()
		os.Stdout, os.Stderr = oldOut, oldErr
	}()
	runErr := fn()
	w.Close()
	data, _ := io.ReadAll(r)
	r.Close()
	return string(data), runErr
}

func (t *TUI) quit() {
	select {
	case <-t.stopTick:
	default:
		close(t.stopTick)
	}
	t.app.Stop()
}

// Run starts the interface; it blocks until the user exits.
func (t *TUI) Run() error {
	t.installInputHandlers()

	// Paint the initial frame directly — QueueUpdateDraw before app.Run
	// would block waiting for the event loop.
	t.footer.SetText(t.renderFooter(80))
	t.core.writeCard(cardBoot(VERSION, t.core.provider, t.core.model, t.core.readOnly))
	if !agenttools.SandboxEnabled() {
		t.core.writeCard(cardNotice("sandbox disabled — bash/exec run with full host access"))
	}
	if leftovers := loadPendingPrompts(); len(leftovers) > 0 {
		t.queueMu.Lock()
		t.queue = append(t.queue, leftovers...)
		n := len(t.queue)
		t.queueMu.Unlock()
		t.core.writeCard(cardNotice(fmt.Sprintf("%d queued prompt%s recovered from the previous session",
			len(leftovers), plural(len(leftovers)))))
		logInfo("queue restored", "recovered", len(leftovers), "total", n)
	}
	t.renderHistory() // replay resumed sessions before the live feed starts

	go t.tickLoop()

	bounce := func() { t.app.SetFocus(t.prompt) }
	for _, p := range []tview.Primitive{t.streamTV, t.planStrip} {
		p.(*tview.TextView).SetFocusFunc(bounce)
	}

	err := func() error {
		t.uiActive.Store(true)
		defer t.uiActive.Store(false)
		return t.app.SetRoot(t.root, true).SetFocus(t.prompt).
			EnableMouse(false). // selection first; alt+m or /mouse enables wheel
			SetMouseCapture(func(ev *tcell.EventMouse, action tview.MouseAction) (*tcell.EventMouse, tview.MouseAction) {
				// wheel scrolls the transcript from anywhere, with the same
				// acceleration as the keys; other mouse events pass through.
				if ev == nil { // tview may synthesize nil events
					return ev, action
				}
				switch ev.Buttons() {
				case tcell.WheelUp:
					t.scrollLines(-3)
					return nil, action
				case tcell.WheelDown:
					t.scrollLines(3)
					return nil, action
				}
				return ev, action
			}).Run()
	}()

	select {
	case <-t.stopTick:
	default:
		close(t.stopTick)
	}

	if t.agent != nil {
		if name := t.agent.CurrentSessionName(); name != "" {
			fmt.Fprintf(os.Stderr, "%s  ↳ resume this chat: sigmoidh -session %s%s\n", ansiDim, name, ansiReset)
		}
	}
	return err
}

// renderHistory replays a resumed session into the stream: prompts, answers,
// tool rows and result branches — bodies skipped so old transcripts load fast.
func (t *TUI) renderHistory() {
	if t.agent == nil {
		return
	}
	msgs := t.agent.messages
	count := 0
	for _, m := range msgs {
		if m.Role != litellm.RoleSystem {
			count++
		}
	}
	if count == 0 {
		return
	}
	for _, m := range msgs {
		switch m.Role {
		case litellm.RoleUser:
			var text strings.Builder
			for _, blk := range m.Blocks {
				if tb, ok := blk.(litellm.TextBlock); ok {
					text.WriteString(tb.Text)
				}
			}
			if s := strings.TrimSpace(text.String()); s != "" {
				t.core.writeCard(cardUserPrompt(s, t.core.feed.innerWidth()))
			}

		case litellm.RoleAssistant:
			for _, blk := range m.Blocks {
				switch b := blk.(type) {
				case litellm.ReasoningBlock:
					if s := strings.TrimSpace(b.Text); s != "" {
						t.core.writeCard(cardThinkingStart())
						rail := "  " + tg(pal.dim) + "▎" + tagReset + " "
						for _, ln := range strings.Split(s, "\n") {
							t.core.emit(rail + tg(pal.violet) + escTag(ln) + tagReset + "\n")
						}
						t.core.emit("\n")
					}
				case litellm.TextBlock:
					if s := strings.TrimSpace(b.Text); s != "" {
						var md mdStream
						t.core.emit(md.Feed(strings.TrimRight(s, "\n") + "\n"))
						t.core.emit(md.Close() + "\n")
					}
				case litellm.ToolUseBlock:
					target, counts := toolCallSummary(b.Name, b.Arguments)
					isCmd := b.Name == "bash" || b.Name == "exec"
					t.core.writeCard(cardToolCall(b.Name, target, counts, isCmd))
					if isCmd && target != "" {
						t.core.writeCard(gutterBlock(target))
					}
				}
			}

		case litellm.RoleTool:
			for _, blk := range m.Blocks {
				if rb, ok := blk.(litellm.ToolResultBlock); ok {
					var size int
					var errFlag bool
					for _, cb := range rb.Content {
						if tb, ok := cb.(litellm.TextBlock); ok {
							size += len(tb.Text)
						}
					}
					errFlag = rb.IsError
					t.core.emit(cardToolResultLine("", errFlag, size, 0))
					if errFlag {
						t.core.emit("\n")
					}
				}
			}
		}
	}
	t.core.writeCard(cardResumed(count))
}
