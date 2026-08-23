package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/glamour"
	"github.com/common-nighthawk/go-figure"
	"github.com/mattn/go-runewidth"
	"github.com/voocel/litellm"
	"golang.org/x/term"
)

// ANSI terminal color
const (
	ansiReset   = "\033[0m"
	ansiBold    = "\033[1m"
	ansiDim     = "\033[2m"
	ansiCyan    = "\033[36m"
	ansiGreen   = "\033[32m"
	ansiYellow  = "\033[33m"
	ansiRed     = "\033[31m"
	ansiMagenta = "\033[35m"

	clearBelow   = "\r\033[J"
	slowToolTime = 100 * time.Millisecond

	// git-style diff bands (256-color backgrounds, full-row fill)
	bgDiffAdd = "\x1b[48;5;22m"
	bgDiffDel = "\x1b[48;5;52m"
	fgDiffAdd = "\x1b[38;5;114m"
	fgDiffDel = "\x1b[38;5;203m"
)

type TerminalLogger struct {
	w         io.Writer
	debug     bool
	outTTY    bool
	quietText bool // child agents: hide their final prose (it becomes the tool result)
	plain     bool // non-interactive filter mode: response text only

	showThinking bool
	showUsage    bool

	thinking    bool
	reasonBuf   strings.Builder
	thinkLW     *liveWriter
	thinkHeader bool           // header printed (first delta arrived)
	thinkRows   int            // raw rows streamed (for the swap budget)
	norm        textNormalizer // choppy-chunk reasoning prose (aggressive)

	pendingDiffs []string

	spin *spinner

	textLW *liveWriter
	mdBuf  strings.Builder
}

// indentWriter prefixes every line with n spaces — used to nest sub-agent UI.
type indentWriter struct {
	w   io.Writer
	n   int
	bol bool
}

func (iw *indentWriter) Write(p []byte) (int, error) {
	written := 0
	for _, b := range p {
		if iw.bol {
			pad := []byte(strings.Repeat(" ", iw.n))
			if _, err := iw.w.Write(pad); err != nil {
				return written, err
			}
			iw.bol = false
		}
		if _, err := iw.w.Write([]byte{b}); err != nil {
			return written, err
		}
		written++
		if b == '\n' {
			iw.bol = true
		}
	}
	return written, nil
}

func (l *TerminalLogger) SubagentObserver() AgentObserver {
	return &TerminalLogger{
		w:            &indentWriter{w: l.w, n: 4, bol: true},
		debug:        l.debug,
		outTTY:       false,
		showThinking: true,
		quietText:    true,
	}
}

func NewTerminalLogger(w io.Writer, debug bool) *TerminalLogger {
	return &TerminalLogger{
		w:            w,
		debug:        debug,
		outTTY:       term.IsTerminal(int(os.Stdout.Fd())),
		showThinking: true,
	}
}

// MakePlain switches to non-interactive filter mode: all events no-op except
// the final assistant text, which prints as clean plain text on stdout.
func MakePlain(l *TerminalLogger) {
	l.plain = true
	l.outTTY = false
	l.showThinking = false
}

func (l *TerminalLogger) SetThinkingVisible(visible bool) {
	l.showThinking = visible
}

func (l *TerminalLogger) ThinkingVisible() bool {
	return l.showThinking
}

func (l *TerminalLogger) SetUsageVisible(visible bool) {
	l.showUsage = visible
}

func (l *TerminalLogger) UsageVisible() bool {
	return l.showUsage
}

// OnTurnUsage prints the optional per-turn token line (off by default).
func (l *TerminalLogger) OnTurnUsage(usage litellm.Usage) {
	if !l.showUsage || !usage.HasTokens() {
		return
	}
	fmt.Fprintf(l.w, "  %s· %s tok (%s in · %s out · %s r)%s\n",
		ansiDim,
		humanNumber(usage.TotalTokens),
		humanNumber(usage.InputTokens),
		humanNumber(usage.OutputTokens),
		humanNumber(usage.ReasoningTokens),
		ansiReset,
	)
}

func (l *TerminalLogger) OnBootstrap(version, provider, model string, readOnly bool) {
	if l.plain {
		return
	}
	printDynamicBanner(provider, model, readOnly)
}

// swapMaxRows caps in-place raw→rendered swaps: a terminal cannot erase
// scrollback, so blocks taller than the screen must keep their raw stream.
var swapMaxRows = func() int {
	_, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || h <= 0 {
		return 20
	}
	return h - 4
}

// spinnerFrames are braille frames for activity indicators (REPL + TUI).
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinner shows activity while waiting for the provider's first token.
type spinner struct {
	stop  chan struct{}
	done  chan struct{}
	start time.Time
}

func (l *TerminalLogger) OnRequestStart() {
	l.startSpinner("working…")
}

// startSpinner shows a live activity line; only one spinner runs at a time.
func (l *TerminalLogger) startSpinner(label string) {
	if !l.outTTY || l.spin != nil {
		return
	}
	s := &spinner{stop: make(chan struct{}), done: make(chan struct{}), start: time.Now()}
	l.spin = s
	go func() {
		defer close(s.done)
		tick := time.NewTicker(90 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i = (i + 1) % len(spinnerFrames) {
			select {
			case <-s.stop:
				return
			case <-tick.C:
				fmt.Fprintf(os.Stdout, "\r  %s%s%s %s%s (%.1fs)%s\033[K",
					ansiMagenta, spinnerFrames[i], ansiReset, ansiDim, label,
					time.Since(s.start).Seconds(), ansiReset)
			}
		}
	}()
}

// stopSpinner tears down the pending-state spinner, if running.
func (l *TerminalLogger) stopSpinner() {
	if l.spin == nil {
		return
	}
	s := l.spin
	l.spin = nil
	close(s.stop)
	<-s.done
	fmt.Fprint(os.Stdout, "\r\033[K")
}

func (l *TerminalLogger) OnRequestDone() {
	l.stopSpinner()
}

func (l *TerminalLogger) OnThinkingStart() {
	if l.plain {
		return
	}
	l.stopSpinner()
	l.thinking = true
	l.reasonBuf.Reset()
	l.norm = textNormalizer{Aggressive: true}
	l.thinkHeader = false
	l.thinkLW = nil
	// header waits for the first delta: empty-reasoning turns print nothing
}

// feedReasoning collapses lone newlines into spaces: some providers emit
// reasoning one word per event with hard breaks, which rendered as
// one-word-per-line. Blank lines (paragraph breaks) are preserved.
func (l *TerminalLogger) feedReasoning(chunk string) string {
	return l.norm.normalize(chunk)
}

// textNormalizer streams chunked model text into clean prose. Providers
// often emit one word (or token) per event with newlines wrapped around it,
// which used to render as word-per-line with random double spaces.
//
// Soft strategy (assistant markdown): lone newlines → space, blank lines
// stay paragraph breaks, space runs collapse to one.
// Aggressive strategy (reasoning): newline runs collapse to a space unless
// the next character starts markdown structure (- * # > ` digits), and a
// space hanging before punctuation is dropped ("site ." → "site.").
type textNormalizer struct {
	Aggressive   bool
	pendingNL    int
	pendingSpace bool // a collapsible space is held, not yet written
	lastWS       bool
}

func (n *textNormalizer) normalize(chunk string) string {
	var b strings.Builder
	flushSpace := func(r rune) {
		if n.pendingSpace {
			if isPunct(r) && n.Aggressive {
				n.pendingSpace = false // "site ." → "site."
			} else {
				b.WriteByte(' ')
			}
			n.pendingSpace = false
		}
	}
	structural := func(r rune) bool {
		switch {
		case r == '-' || r == '*' || r == '#' || r == '>' || r == '`':
			return true
		case r >= '0' && r <= '9':
			return true
		}
		return false
	}

	for _, r := range chunk {
		switch {
		case r == '\n' || r == '\r':
			// a held space before a newline survives only as one space
			if n.pendingSpace && !n.lastWS {
				n.pendingSpace = false
				b.WriteByte(' ')
				n.lastWS = true
			} else {
				n.pendingSpace = false
			}
			n.pendingNL++
			continue

		case n.pendingNL > 0:
			// resolve the held newline run now that content arrived
			if n.Aggressive {
				if n.pendingNL >= 2 && structural(r) {
					b.WriteString("\n\n")
					n.lastWS = true
				} else if !n.lastWS {
					n.pendingSpace = true // held: punctuation may absorb it
				}
			} else {
				switch {
				case n.pendingNL >= 2:
					b.WriteString("\n\n")
					n.lastWS = true
				case n.pendingNL == 1 && !n.lastWS:
					b.WriteByte(' ')
					n.lastWS = true
				}
			}
			n.pendingNL = 0

		case r == ' ' || r == '\t':
			if !n.lastWS && !n.pendingSpace {
				n.pendingSpace = true
			}
			continue

		default:
			flushSpace(r)
			n.lastWS = false
			b.WriteRune(r)
			continue
		}
		// newline-resolution paths land here only when they emitted breaks;
		// content rune r still needs writing.
		flushSpace(r)
		n.lastWS = false
		b.WriteRune(r)
	}
	return b.String()
}

func isPunct(r rune) bool {
	return r == '.' || r == ',' || r == '!' || r == '?' || r == ':' || r == ';' || r == ')' || r == ']'
}

func (l *TerminalLogger) OnReasoningDelta(text string) {
	norm := l.feedReasoning(text)
	l.reasonBuf.WriteString(norm)
	if !l.outTTY || !l.showThinking {
		return
	}
	if !l.thinkHeader {
		fmt.Fprint(os.Stdout, saveCursor)
		fmt.Fprintf(os.Stdout, "  %s✻ thinking…%s\n", ansiMagenta, ansiReset)
		fmt.Fprint(os.Stdout, ansiDim)
		l.thinkLW = newLiveWriter(os.Stdout, getStdoutWidth()-4, "  ")
		l.thinkHeader = true
	}
	l.thinkLW.WriteString(norm) // live dim stream under the ✻ header
}

func (l *TerminalLogger) OnThinkingEnd(took time.Duration) {
	if l.plain || !l.thinking {
		return
	}
	l.thinking = false
	live := l.outTTY && l.thinkLW != nil
	thinkRows := 0
	if l.thinkLW != nil {
		thinkRows = l.thinkLW.rows
	}
	l.thinkLW = nil

	text := strings.TrimSpace(l.reasonBuf.String())
	if !live {
		// non-TTY / hidden: single-shot chrome render as before
		if text == "" || !l.showThinking {
			return
		}
		out := io.Writer(l.w)
		width := getStdoutWidth() - 4
		fmt.Fprintf(out, "  %s✻ thinking…%s\n", ansiMagenta, ansiReset)
		rendered := renderMarkdownWidth(text, width)
		if rendered == "" {
			for _, ln := range wrapWords(text, width) {
				fmt.Fprintf(out, "%s%s\n", ansiDim, ln)
			}
		} else {
			fmt.Fprint(out, dimAnsi(stripANSICodes(rendered)))
		}
		fmt.Fprintf(out, "  %s✻ thought for %s%s\n", ansiMagenta, fmtDuration(took), ansiReset)
		return
	}

	// live: wipe the raw streamed region and lay down the final render —
	// but only if it never scrolled past the anchor (terminals cannot erase
	// scrollback; a tall block keeps its raw stream instead of duplicating)
	if !live || !l.thinkHeader {
		return
	}
	if rows := thinkRows; rows > swapMaxRows() {
		fmt.Fprintf(os.Stdout, "\n  %s✻ thought for %s%s\n", ansiMagenta, fmtDuration(took), ansiReset)
		return
	}
	fmt.Fprint(os.Stdout, ansiReset+restoreCursor+clearToEnd)
	if text == "" || !l.showThinking {
		return
	}
	width := getStdoutWidth() - 4
	fmt.Fprintf(os.Stdout, "  %s✻ thinking…%s\n", ansiMagenta, ansiReset)
	rendered := renderMarkdownWidth(text, width)
	if rendered == "" {
		for _, ln := range wrapWords(text, width) {
			fmt.Fprintf(os.Stdout, "%s%s\n", ansiDim, ln)
		}
	} else {
		fmt.Fprint(os.Stdout, dimAnsi(stripANSICodes(rendered)))
	}
	fmt.Fprintf(os.Stdout, "  %s✻ thought for %s%s\n", ansiMagenta, fmtDuration(took), ansiReset)
}

func (l *TerminalLogger) OnAssistantTextStart() {
	l.stopSpinner()
	l.mdBuf.Reset()
	l.textLW = nil
	if !l.outTTY {
		return
	}
	fmt.Fprint(os.Stdout, saveCursor)
	l.textLW = newLiveWriter(os.Stdout, getStdoutWidth(), "")
}

// OnAssistantDelta streams raw text live through the anchored region; the
// final markdown render swaps in once on completion — per-line re-rendering
// fights PTY realities (cursor probes + deferred autowrap) and drifts across
// emulators, so raw-until-done is the stable contract.
func (l *TerminalLogger) OnAssistantDelta(text string) {
	if l.plain {
		return
	}
	l.mdBuf.WriteString(text)
	if l.outTTY && l.textLW != nil {
		l.textLW.WriteString(text)
	}
}

func (l *TerminalLogger) OnAssistantText(text string) {
	live := l.outTTY && l.textLW != nil
	textRows := 0
	if l.textLW != nil {
		textRows = l.textLW.rows
	}
	l.textLW = nil

	clean := strings.NewReplacer("**", "", "__", "", "`", "").Replace(stripANSICodes(strings.TrimSpace(text)))
	if l.plain || clean == "" {
		if l.plain && clean != "" {
			fmt.Println(clean)
		}
		return
	}
	rendered := normalizeRendered(renderMarkdown(clean))
	fallback := rendered == ""
	if fallback {
		clean = strings.TrimSpace(clean)
	}
	if live {
		if textRows > swapMaxRows() {
			// block scrolled: keep the raw stream, do not duplicate
			fmt.Fprintln(os.Stdout)
			return
		}
		// wipe the raw streamed region, print the final block in place
		fmt.Fprint(os.Stdout, restoreCursor+clearToEnd)
		switch {
		case fallback:
			fmt.Println(clean)
		default:
			fmt.Print(rendered)
		}
		return
	}
	if fallback {
		fmt.Println(clean)
	} else {
		fmt.Print(rendered)
	}
	l.mdBuf.Reset()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (l *TerminalLogger) OnToolCall(name string, args json.RawMessage) {
	if l.plain {
		return
	}
	l.stopSpinner()
	target, counts := toolCallSummary(name, args)

	isCmd := name == "bash" || name == "exec"
	line := fmt.Sprintf("  %s▲%s %s%s%s", ansiYellow, ansiReset, ansiBold, name, ansiReset)
	if target != "" && !isCmd {
		line += fmt.Sprintf("  %s%s%s", ansiDim, truncateRunes(target, 88), ansiReset)
	}
	if counts != "" {
		line += fmt.Sprintf("  %s%s%s", ansiDim, counts, ansiReset)
	}
	fmt.Fprintln(l.w, line)

	if isCmd && target != "" {
		fmt.Fprint(l.w, frameCommand(target))
	}
	if name == "subagent" {
		var sa subagentArgs
		if json.Unmarshal(args, &sa) == nil && strings.TrimSpace(sa.Prompt) != "" {
			fmt.Fprint(l.w, frameCommand("task · "+strings.TrimSpace(sa.Prompt)))
		}
	}
	l.pendingDiffs = append(l.pendingDiffs, diffPreviewFor(name, args))
	l.startSpinner("running " + name + "…")
}

// frameCommand renders the complete shell command, hard-wrapped, under the
// │ gutter — the command IS the action, so it is never truncated.
func frameCommand(cmd string) string {
	width := getTerminalWidth() - 8
	var out strings.Builder
	lw := newLiveWriter(&out, width+4, "  │ ")
	for i, seg := range strings.Split(strings.TrimRight(cmd, "\n"), "\n") {
		if i > 0 {
			lw.WriteString("\n")
		}
		if seg == "" {
			out.WriteString("  " + ansiDim + "│" + ansiReset)
			lw.col = lw.prefixWidth
			continue
		}
		lw.WriteString(seg)
	}
	out.WriteString("\n")
	return out.String()
}

// toolCallSummary extracts a human target (file path, command, query…) and an
// optional plain "+n -m"/"(n bytes)" change summary instead of dumping raw
// JSON. Counts are unstyled; each view applies its own coloring.
func toolCallSummary(name string, args json.RawMessage) (target, counts string) {
	var a struct {
		Path      string `json:"path"`
		Command   string `json:"command"`
		Query     string `json:"query"`
		URL       string `json:"url"`
		Prompt    string `json:"prompt"`
		Key       string `json:"key"`
		Op        string `json:"op"`
		When      string `json:"when"`
		SessionID int    `json:"session_id"`
	}
	_ = json.Unmarshal(args, &a)

	switch name {
	case "edit":
		var e struct {
			Path   string `json:"path"`
			OldStr string `json:"old_str"`
			NewStr string `json:"new_str"`
		}
		if json.Unmarshal(args, &e) == nil {
			added := strings.Count(e.NewStr, "\n") + btoi2(e.NewStr != "")
			removed := strings.Count(e.OldStr, "\n") + btoi2(e.OldStr != "")
			counts = fmt.Sprintf("(+%d -%d)", added, removed)
			return displayPath(e.Path), counts
		}
	case "write":
		var w struct {
			Content string `json:"content"`
		}
		n := 0
		if json.Unmarshal(args, &w) == nil {
			n = len(w.Content)
		}
		return displayPath(a.Path), fmt.Sprintf("(%s bytes)", humanNumber(n))
	case "read", "list_files":
		return displayPath(a.Path), ""
	case "bash":
		return a.Command, ""
	case "exec":
		return a.Command, ""
	case "search":
		return a.Query, ""
	case "web":
		if a.URL != "" {
			return a.URL, ""
		}
		return a.Query, ""
	case "subagent":
		return "", ""
	case "memory":
		t := a.Op
		if t == "" {
			t = "list"
		}
		if a.Key != "" {
			t += " " + a.Key
		}
		return t, ""
	case "plan":
		var p planArgs
		if json.Unmarshal(args, &p) == nil {
			op := p.Op
			if op == "" {
				op = "list"
			}
			extra := ""
			switch op {
			case "update":
				extra = fmt.Sprintf(" #%d→%s", p.Index+1, p.Status)
			case "write":
				extra = fmt.Sprintf(" (%d items)", len(p.Items))
			}
			return op + extra, ""
		}
	case "trigger":
		t := a.When
		if a.SessionID > 0 {
			t = fmt.Sprintf("%s · session %d", t, a.SessionID)
		}
		return t, ""
	default:
		if len(args) > 0 {
			var compact bytes.Buffer
			if json.Compact(&compact, args) == nil {
				return compact.String(), ""
			}
		}
	}
	return "", ""
}

func btoi2(b bool) int {
	if b {
		return 1
	}
	return 0
}

// displayPath shortens absolute paths under the working directory.
func displayPath(p string) string {
	if p == "" {
		return ""
	}
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return p
}

func truncateRunes(s string, max int) string {
	if runewidth.StringWidth(s) <= max {
		return s
	}
	out := ""
	for _, r := range s {
		if runewidth.StringWidth(out)+runewidth.RuneWidth(r) > max-1 {
			break
		}
		out += string(r)
	}
	return out + "…"
}

func (l *TerminalLogger) OnToolResult(name, out string, isErr bool, took time.Duration) {
	if l.plain {
		return
	}
	l.stopSpinner()
	var preview string
	if len(l.pendingDiffs) > 0 {
		preview, l.pendingDiffs = l.pendingDiffs[0], l.pendingDiffs[1:]
	}
	if !isErr && preview != "" && !l.debug {
		fmt.Fprint(l.w, preview)
	}
	if !l.debug && !isErr {
		switch {
		case name == "plan":
			fmt.Fprint(l.w, framePlan(out))
		case isShortOutput(out):
			fmt.Fprint(l.w, frameLinesPlain(out))
		}
	}
	statusTag := fmt.Sprintf("%s▼ result%s", ansiGreen, ansiReset)
	durInfo := ""
	if took >= slowToolTime {
		durInfo = fmt.Sprintf(" · %s", fmtDuration(took))
	}
	if isErr {
		statusTag = fmt.Sprintf("%s▼ error%s", ansiRed, ansiReset)
		durInfo = fmt.Sprintf(" · %s", fmtDuration(took))
	}

	trimmed := strings.TrimRight(out, "\r\n")
	byteCount := len(out)

	if !l.debug {
		fmt.Fprintf(l.w, "  %s %s(%d bytes%s)%s\n", statusTag, ansiDim, byteCount, durInfo, ansiReset)
		return
	}

	fmt.Fprintf(l.w, "  %s %s(%d bytes · %s)%s\n", statusTag, ansiDim, byteCount, fmtDuration(took), ansiReset)
	for _, line := range strings.Split(trimmed, "\n") {
		fmt.Fprintf(l.w, "  %s│%s %s\n", ansiDim, ansiReset, line)
	}
	fmt.Fprintln(l.w)
}

func (l *TerminalLogger) OnRetryWait(provider string, attempt int, delay time.Duration) {
	l.stopSpinner()
	fmt.Fprintf(l.w, "  %s⧗ %s rate-limited — retry %d/%d in %s%s\n",
		ansiYellow, provider, attempt, retriesPerProvider, fmtDuration(delay), ansiReset)
}

func (l *TerminalLogger) OnProviderFallback(provider, model string) {
	l.stopSpinner()
	fmt.Fprintf(l.w, "  %s⚠ %s unavailable — falling back to %s%s%s\n",
		ansiYellow, provider, ansiBold, model, ansiReset)
}

func (l *TerminalLogger) OnTriggerFired(message string) {
	if l.plain {
		return
	}
	fmt.Fprintf(l.w, "  %s🔔 notified: %s%s\n", ansiDim, message, ansiReset)
}

func (l *TerminalLogger) OnRunComplete(stats *RunStats) {
	if l.plain {
		return
	}
	l.PrintSummaryLine(stats)
	fmt.Fprintf(l.w, "%sGoodbye!%s\n", ansiDim, ansiReset)
}

func (l *TerminalLogger) OnPrintUsageStats(stats *RunStats) {
	if l.plain {
		return
	}
	l.PrintDetailedUsage(stats)
}

func humanNumber(n int) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func fmtDuration(d time.Duration) string {
	switch {
	case d >= time.Minute:
		return fmt.Sprintf("%.1fm", d.Minutes())
	case d >= 100*time.Second:
		return fmt.Sprintf("%.0fs", d.Seconds())
	default:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
}

func (l *TerminalLogger) PrintSummaryLine(stats *RunStats) {
	if l.plain || stats == nil {
		return
	}

	cacheInfo := ""
	if stats.TotalCacheReadTokens > 0 || stats.TotalCacheWriteTokens > 0 {
		cacheInfo = fmt.Sprintf(" · cache %s/%s",
			humanNumber(stats.TotalCacheReadTokens),
			humanNumber(stats.TotalCacheWriteTokens),
		)
	}
	agentsInfo := ""
	if stats.SubagentRuns > 0 {
		agentsInfo = fmt.Sprintf(" · %d sub-agent%s", stats.SubagentRuns, plural(stats.SubagentRuns))
	}

	fmt.Fprintf(l.w, "\n%s%s● Done%s  %s%s turns · %s tools · %s total tokens (%s in · %s out · %s r) · took %s%s%s%s\n\n",
		ansiBold, ansiGreen, ansiReset,
		ansiDim,
		humanNumber(stats.TotalTurns),
		humanNumber(stats.TotalToolCalls),
		humanNumber(stats.TotalTokens),
		humanNumber(stats.TotalInputTokens),
		humanNumber(stats.TotalOutputTokens),
		humanNumber(stats.TotalReasoningTokens),
		fmtDuration(stats.TotalDuration),
		cacheInfo,
		agentsInfo,
		ansiReset,
	)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// prints a grid for when the user types /usage.
func (l *TerminalLogger) PrintDetailedUsage(stats *RunStats) {
	if stats == nil {
		fmt.Fprintf(l.w, "%sNo active session statistics available.%s\n", ansiDim, ansiReset)
		return
	}

	fmt.Fprintf(l.w, "\n%s%s● Usage%s\n\n", ansiDim, ansiCyan, ansiReset)

	fmt.Fprintf(l.w, "  %sTurns%s         %-8s    %sInput Tokens%s     %s\n",
		ansiDim, ansiReset, humanNumber(stats.TotalTurns),
		ansiDim, ansiReset, humanNumber(stats.TotalInputTokens),
	)
	fmt.Fprintf(l.w, "  %sTool Calls%s    %-8s    %sOutput Tokens%s    %s\n",
		ansiDim, ansiReset, humanNumber(stats.TotalToolCalls),
		ansiDim, ansiReset, humanNumber(stats.TotalOutputTokens),
	)
	fmt.Fprintf(l.w, "  %sCache Read%s    %-8s    %sReasoning%s        %s\n",
		ansiDim, ansiReset, humanNumber(stats.TotalCacheReadTokens),
		ansiDim, ansiReset, humanNumber(stats.TotalReasoningTokens),
	)
	fmt.Fprintf(l.w, "  %sCache Write%s   %-8s    %sTotal Tokens%s     %s%s%s\n",
		ansiDim, ansiReset, humanNumber(stats.TotalCacheWriteTokens),
		ansiDim, ansiReset, ansiBold, humanNumber(stats.TotalTokens), ansiReset,
	)
	if stats.SubagentRuns > 0 {
		fmt.Fprintf(l.w, "  %sSub-agents%s     %d\n", ansiDim, ansiReset, stats.SubagentRuns)
	}
	fmt.Fprintln(l.w)
}

var bannerNoSandbox bool

func printDynamicBanner(provider, model string, readOnly bool) {
	myFigure := figure.NewColorFigure("SigmoidH", "slant", "cyan", true)
	myFigure.Print()

	modeBadge := ""
	if readOnly {
		modeBadge = fmt.Sprintf("  %s%s[read-only]%s", ansiYellow, ansiBold, ansiReset)
	}
	if bannerNoSandbox {
		modeBadge += fmt.Sprintf("  %s%s[no-sandbox]%s", ansiRed, ansiBold, ansiReset)
	}

	fmt.Fprintf(os.Stderr, "\n  %s%sSigmoidh %s%s  %s%s · %s%s%s\n",
		ansiBold, ansiReset, VERSION, ansiReset,
		ansiDim, provider, model, ansiReset,
		modeBadge,
	)
	fmt.Fprintf(os.Stderr, "%s  type /help for commands · Ctrl+D to exit · Ctrl+C to stop a run%s\n\n", ansiDim, ansiReset)
}

// getTerminalWidth returns active terminal columns for stderr (fallback to 80).
func getTerminalWidth() int {
	width, _, err := term.GetSize(int(os.Stderr.Fd()))
	if err != nil || width <= 0 {
		return 80
	}
	return width
}

// getStdoutWidth returns active terminal columns for stdout (fallback to stderr's, then 80).
func getStdoutWidth() int {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		return getTerminalWidth()
	}
	return width
}

func renderMarkdown(text string) string {
	return renderMarkdownWidth(text, getStdoutWidth())
}

// renderMarkdownWidth renders markdown for a specific column width.
func renderMarkdownWidth(text string, width int) string {
	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return ""
	}
	rendered, err := renderer.Render(text)
	if err != nil {
		return ""
	}
	return rendered
}

// liveWriter streams text into the terminal while tracking how many physical
// rows were consumed, so streamed output can be erased and re-rendered.
// Every physical line is indented with prefix (counted against width).
type liveWriter struct {
	w           io.Writer
	width       int // total usable columns, prefix included
	prefix      string
	prefixWidth int
	col         int
	rows        int // physical rows occupied, including the in-progress one (starts at 1)
}

func newLiveWriter(w io.Writer, width int, prefix string) *liveWriter {
	if width < 20 {
		width = 20
	}
	pw := runewidth.StringWidth(prefix)
	return &liveWriter{w: w, width: width - pw, prefix: prefix, prefixWidth: pw, rows: 1}
}

func (lw *liveWriter) WriteString(s string) {
	for _, r := range s {
		switch r {
		case '\n':
			fmt.Fprint(lw.w, "\n")
			lw.rows++
			lw.col = 0
			continue
		case '\r':
			continue
		case '\t':
			n := 4 - lw.col%4 // advance to the next 4-column tab stop
			for i := 0; i < n; i++ {
				lw.writeRune(' ')
			}
			continue
		}
		if lw.col == 0 && lw.prefix != "" {
			fmt.Fprint(lw.w, lw.prefix)
			lw.col = lw.prefixWidth
		}
		lw.writeRune(r)
	}
}

func (lw *liveWriter) writeRune(r rune) {
	rw := runewidth.RuneWidth(r)
	if rw == 0 {
		return // skip zero-width/control runes to keep column accounting exact
	}
	if lw.col+rw > lw.width {
		fmt.Fprint(lw.w, "\n"+lw.prefix)
		lw.rows++
		lw.col = lw.prefixWidth
	}
	fmt.Fprint(lw.w, string(r))
	lw.col += rw
}

// wrapWords splits a paragraph by whitespace so each line fits inside maxWidth.
func wrapWords(text string, maxWidth int) []string {
	if maxWidth < 20 {
		maxWidth = 20
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	var lines []string
	var current strings.Builder
	currentLen := 0

	for _, w := range words {
		wLen := utf8.RuneCountInString(w)
		if currentLen == 0 {
			current.WriteString(w)
			currentLen = wLen
		} else if currentLen+1+wLen <= maxWidth {
			current.WriteString(" ")
			current.WriteString(w)
			currentLen += 1 + wLen
		} else {
			lines = append(lines, current.String())
			current.Reset()
			current.WriteString(w)
			currentLen = wLen
		}
	}

	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

const maxDiffLines = 12

// diffPreviewFor builds a colorized preview for tools whose args describe a
// change (edit/apply_patch). Returned strings are pre-styled and printed
// verbatim before the tool result line.
func diffPreviewFor(name string, args json.RawMessage) string {
	switch name {
	case "edit":
		var a struct {
			Path   string `json:"path"`
			OldStr string `json:"old_str"`
			NewStr string `json:"new_str"`
		}
		if json.Unmarshal(args, &a) != nil || a.Path == "" {
			return ""
		}
		var b strings.Builder
		b.WriteString(colorLines(a.OldStr, fgDiffDel, bgDiffDel, "-"))
		b.WriteString(colorLines(a.NewStr, fgDiffAdd, bgDiffAdd, "+"))
		return b.String()
	case "write":
		var w struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal(args, &w) != nil || w.Path == "" || w.Content == "" {
			return ""
		}
		return colorLines(w.Content, fgDiffAdd, bgDiffAdd, "+")
	case "apply_patch":
		var a struct {
			Patch string `json:"patch"`
		}
		if json.Unmarshal(args, &a) != nil || a.Patch == "" {
			return ""
		}
		return colorizePatch(a.Patch, 24)
	default:
		return ""
	}
}

// frameLines re-indents preview body lines under the call's │ gutter.
func frameLines(preview string) string {
	if preview == "" {
		return ""
	}
	var out strings.Builder
	for _, ln := range strings.Split(strings.TrimRight(preview, "\n"), "\n") {
		trimmed := strings.TrimPrefix(ln, "  ")
		out.WriteString("  " + ansiDim + "│" + ansiReset + " " + trimmed + "\n")
	}
	return out.String()
}

// colorLines renders each line as a full-width diff band (background fill like
// git/GitHub), prefixed with the app's │ gutter.
func colorLines(s, markerFg, bg, sign string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	width := getStdoutWidth() - 6 // gutter + margins
	var b strings.Builder
	for i, ln := range lines {
		if i >= maxDiffLines {
			fmt.Fprintf(&b, "  %s│%s %s… %d more line%s%s\n",
				ansiDim, ansiReset, ansiDim, len(lines)-maxDiffLines, plural(len(lines)-maxDiffLines), ansiReset)
			break
		}
		inner := fmt.Sprintf("%s%s %s", markerFg, sign, ln)
		pad := width - runewidth.StringWidth(inner) - 1
		if pad < 1 {
			pad = 1
		}
		fmt.Fprintf(&b, "  %s│%s %s%s%s%s\n", ansiDim, ansiReset, bg, inner, strings.Repeat(" ", pad), ansiReset)
	}
	return b.String()
}

func colorizePatch(patch string, maxLines int) string {
	lines := strings.Split(strings.TrimRight(patch, "\n"), "\n")
	var b strings.Builder
	shown := 0
	for _, ln := range lines {
		if shown >= maxLines {
			fmt.Fprintf(&b, "  %s… %d more line%s%s\n",
				ansiDim, len(lines)-shown, plural(len(lines)-shown), ansiReset)
			break
		}
		switch {
		case strings.HasPrefix(ln, "@@"):
			fmt.Fprintf(&b, "  %s│%s %s%s%s\n", ansiDim, ansiReset, ansiCyan, ln, ansiReset)
		case strings.HasPrefix(ln, "diff "), strings.HasPrefix(ln, "index "),
			strings.HasPrefix(ln, "--- "), strings.HasPrefix(ln, "+++ "):
			fmt.Fprintf(&b, "  %s│%s %s%s%s\n", ansiDim, ansiReset, ansiDim, ln, ansiReset)
		case strings.HasPrefix(ln, "+"):
			b.WriteString(bandLine(fgDiffAdd, bgDiffAdd, ln))
		case strings.HasPrefix(ln, "-"):
			b.WriteString(bandLine(fgDiffDel, bgDiffDel, ln))
		default:
			fmt.Fprintf(&b, "  %s│%s %s\n", ansiDim, ansiReset, ln)
		}
		shown++
	}
	return b.String()
}

// bandLine paints one full-width diff band inside the gutter.
func bandLine(markerFg, bg, ln string) string {
	sign := ln[:1]
	rest := ""
	if len(ln) > 1 {
		rest = ln[1:]
	}
	width := getStdoutWidth() - 6
	inner := fmt.Sprintf("%s%s %s", markerFg, sign, rest)
	pad := width - runewidth.StringWidth(inner) - 1
	if pad < 1 {
		pad = 1
	}
	return fmt.Sprintf("  %s│%s %s%s%s%s\n", ansiDim, ansiReset, bg, inner, strings.Repeat(" ", pad), ansiReset)
}

// parsePlanRow splits one rendered board line ("1. [x] scaffold") into its
// number prefix, label text, and status flags. Shared by every view.
func parsePlanRow(ln string) (num, label string, done, active bool) {
	num, label = "", ln
	if i := strings.Index(ln, ". "); i > 0 && i < 4 {
		num, label = ln[:i+1], strings.TrimSpace(ln[i+2:])
	}
	done = strings.Contains(ln, "[x]")
	active = strings.Contains(ln, "[~]")
	label = strings.TrimSpace(strings.NewReplacer("[x]", "", "[~]", "", "[ ]", "").Replace(label))
	return num, label, done, active
}

// framePlan renders the agent's checklist with status glyphs and colors.
func framePlan(board string) string {
	var b strings.Builder
	for _, ln := range strings.Split(strings.TrimRight(board, "\n"), "\n") {
		num, rest, done, active := parsePlanRow(ln)
		glyph, color := "○", ansiDim
		switch {
		case done:
			glyph, color = "✓", ansiGreen
		case active:
			glyph, color = "▶", ansiCyan
		}
		rest = strings.Join(strings.Fields(num+" "+rest), " ")
		fmt.Fprintf(&b, "  %s│%s %s%s%s %s\n", ansiDim, ansiReset, color, glyph, ansiReset, rest)
	}
	return b.String()
}

// normalizeRendered strips ANSI/OSC noise and guarantees exactly one trailing
// newline, so physical row counting stays exact across re-renders.
func normalizeRendered(s string) string {
	s = stripANSICodes(s)
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t") // kill full-width padding: avoids deferred autowrap row drift
	}
	s = strings.TrimRight(strings.Join(lines, "\n"), "\n")
	if s == "" {
		return ""
	}
	return s + "\n"
}

// dimAnsi strips any embedded styling first, then applies one persistent dim
// attribute — immune to terminals that drop dim when an fg color follows.
func dimAnsi(s string) string {
	return ansiDim + stripANSICodes(s) + ansiReset
}

// stripANSICodes removes ANSI CSI sequences and OSC (terminal query) blobs
// without regex, so pattern escaping can never bite us again.
func stripANSICodes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		if c != 0x1b {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == ']' { // OSC … BEL | ESC \
			j := i + 2
			for j < len(s) {
				if s[j] == 0x07 {
					j++
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j
			continue
		}
		if i+1 < len(s) && s[i+1] == '[' { // CSI … final byte A-Z / a-z
			j := i + 2
			for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		i += 1 // lone ESC
	}
	return b.String()
}

// isShortOutput: small enough to inline under the call line.
func isShortOutput(out string) bool {
	return out != "" && len(out) <= 400 && strings.Count(out, "\n") <= 5
}

// frameLinesPlain renders short tool output in the │ gutter.
func frameLinesPlain(out string) string {
	var b strings.Builder
	lw := newLiveWriter(&b, getTerminalWidth(), "  │ ")
	for i, seg := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if i > 0 {
			lw.WriteString("\n")
		}
		lw.WriteString(seg)
	}
	b.WriteString("\n")
	return b.String()
}

// Terminal-saved cursor anchors: streaming sections mark their start with
// ESC7 and swap via ESC8 + clear-to-end. The terminal owns the position, so
// no row-count arithmetic can drift across emulators.
const (
	saveCursor    = "\x1b7"
	restoreCursor = "\x1b8"
	clearToEnd    = "\x1b[J"
)

// humanSize renders byte counts the way humans read them: 512 B, 3.2 KB…
func humanSize(n int) string {
	const unit = 1024
	if n < unit {
		return strconv.Itoa(n) + " B"
	}
	div, exp := int64(unit), 0
	for m := int64(n) / unit; m >= unit && exp < 2; m /= unit {
		div *= unit
		exp++
	}
	s := fmt.Sprintf("%.1f", float64(n)/float64(div))
	s = strings.TrimSuffix(s, ".0")
	return s + " " + [...]string{"KB", "MB", "GB"}[exp]
}
