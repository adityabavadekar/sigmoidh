package sigmoidh

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

	pendingCalls []pendingCall // announced calls awaiting results, in order
	batchSize    int           // calls announced together (results get labels when > 1)

	spin *spinner

	textLW   *liveWriter
	mdBuf    strings.Builder
	blockBuf strings.Builder // raw text of the markdown block being streamed
	lineBuf  strings.Builder // current partial line (fence/blank detection)
	inFence  bool
}

// pendingCall remembers what OnToolCall announced so the matching result can
// show its diff preview and, inside a parallel batch, say which call it closes.
type pendingCall struct {
	diff  string
	label string
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
				fmt.Fprintf(os.Stdout, "\r  %s%s%s %s%s (%s)%s\033[K",
					ansiMagenta, spinnerFrames[i], ansiReset, ansiDim, label,
					fmtDuration(time.Since(s.start)), ansiReset)
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
	Aggressive    bool
	pendingNL     int
	pendingIndent int  // spaces seen after a held newline (list nesting)
	pendingSpace  bool // a collapsible space is held, not yet written
	lastWS        bool
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

		case n.pendingNL > 0 && (r == ' ' || r == '\t'):
			// indentation after a newline: hold it until the first real
			// character shows whether this line starts a (nested) list item
			n.pendingIndent++
			continue

		case n.pendingNL > 0:
			// resolve the held newline run now that content arrived
			if n.Aggressive {
				if structural(r) {
					// a list item / heading / quote starts a line, even after a
					// single newline ("1. a\n2. b" must not become one line)
					if n.pendingNL >= 2 {
						b.WriteString("\n\n")
					} else {
						b.WriteString("\n")
					}
					b.WriteString(strings.Repeat(" ", min(n.pendingIndent, 8)))
					n.lastWS = true
				} else if !n.lastWS {
					n.pendingSpace = true // held: punctuation may absorb it
				}
				n.pendingIndent = 0
			} else {
				switch {
				case n.pendingNL >= 2:
					b.WriteString("\n\n")
					b.WriteString(strings.Repeat(" ", min(n.pendingIndent, 8)))
					n.lastWS = true
				case structural(r): // list item after a single newline
					b.WriteString("\n")
					b.WriteString(strings.Repeat(" ", min(n.pendingIndent, 8)))
					n.lastWS = true
				case !n.lastWS:
					b.WriteByte(' ')
					n.lastWS = true
				}
				n.pendingIndent = 0
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
	// header row + the streamed rows below it (cursor sits on the last one)
	fmt.Fprint(os.Stdout, ansiReset+rewindRows(thinkRows))
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
	l.blockBuf.Reset()
	l.lineBuf.Reset()
	l.inFence = false
	l.textLW = nil
	if !l.outTTY {
		return
	}
	l.textLW = newLiveWriter(os.Stdout, getStdoutWidth(), "")
}

// OnAssistantDelta streams raw text live and renders it one markdown block
// at a time: when a block ends (a blank line outside a code fence) its raw
// rows are rewound and replaced by the rendered block. A block is almost
// always shorter than the screen, so the swap works even for long answers —
// swapping the whole answer at the end failed once it had scrolled, leaving
// raw markdown (tables as pipes) on screen. Tables, lists and code blocks
// render whole because a block is only rendered once complete.
func (l *TerminalLogger) OnAssistantDelta(text string) {
	if l.plain {
		return
	}
	l.mdBuf.WriteString(text)
	if !l.outTTY || l.textLW == nil {
		return
	}
	for _, r := range text {
		l.blockBuf.WriteRune(r)
		l.textLW.WriteString(string(r))
		if r != '\n' {
			l.lineBuf.WriteRune(r)
			continue
		}
		line := strings.TrimSpace(l.lineBuf.String())
		l.lineBuf.Reset()
		if fenceMarker(line) != "" {
			l.inFence = !l.inFence
		}
		if line == "" && !l.inFence && strings.TrimSpace(l.blockBuf.String()) != "" {
			l.finishBlock()
		}
	}
}

// finishBlock swaps the current block's raw rows for its rendering.
func (l *TerminalLogger) finishBlock() {
	raw := l.blockBuf.String()
	l.blockBuf.Reset()
	rows := l.textLW.rows
	if strings.TrimSpace(raw) != "" {
		rendered := strings.TrimRight(renderMarkdownWidth(stripANSICodes(raw), getStdoutWidth()), "\n")
		if rendered != "" && rows-1 <= swapMaxRows() {
			fmt.Fprint(os.Stdout, rewindRows(rows-1))
			fmt.Fprint(os.Stdout, normalizeRendered(rendered))
		} else {
			fmt.Fprintln(os.Stdout) // too tall to rewind: keep the raw rows
		}
	}
	l.textLW = newLiveWriter(os.Stdout, getStdoutWidth(), "")
}

func (l *TerminalLogger) OnAssistantText(text string) {
	live := l.outTTY && l.textLW != nil
	clean := stripANSICodes(strings.TrimSpace(text))
	if l.plain || clean == "" {
		if l.plain && clean != "" {
			fmt.Println(strings.NewReplacer("**", "", "__", "", "`", "").Replace(clean))
		}
		l.textLW = nil
		return
	}
	if live && l.mdBuf.Len() > 0 {
		// earlier blocks are already rendered; finish the last one
		if strings.TrimSpace(l.blockBuf.String()) != "" {
			l.finishBlock()
		}
		l.textLW = nil
		l.mdBuf.Reset()
		return
	}
	l.textLW = nil
	// not streamed (or no TTY): render the whole answer at once
	if rendered := normalizeRendered(renderMarkdown(clean)); rendered != "" {
		fmt.Print(rendered)
	} else {
		fmt.Println(clean)
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
	if len(l.pendingCalls) == 0 {
		l.batchSize = 0
	}
	l.batchSize++
	label := name
	if target != "" && !isCmd {
		label += " " + truncateRunes(target, 48)
	}
	l.pendingCalls = append(l.pendingCalls, pendingCall{diff: diffPreviewFor(name, args), label: label})
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
			Path string `json:"path"`
		}
		if json.Unmarshal(args, &e) == nil {
			// counted on the line diff: unchanged lines are not changes,
			// and a trailing newline is not an extra line
			added, removed := diffCounts(parseDiffRows("edit", args))
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
				extra = fmt.Sprintf(" #%d→%s", p.Index, p.Status)
			case "write":
				extra = fmt.Sprintf(" (%d items)", len(p.Items))
			}
			return op + extra, ""
		}
	case "code_nav":
		var c struct{ Op, Path, Symbol string }
		if json.Unmarshal(args, &c) == nil {
			t := c.Op + " " + c.Symbol
			if c.Path != "" {
				t += " " + displayPath(c.Path)
			}
			return strings.Join(strings.Fields(t), " "), ""
		}
	case "run_tests":
		var r struct{ Command string }
		if json.Unmarshal(args, &r) == nil {
			return r.Command, ""
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
	var pc pendingCall
	if len(l.pendingCalls) > 0 {
		pc, l.pendingCalls = l.pendingCalls[0], l.pendingCalls[1:]
	}
	if !isErr && pc.diff != "" && !l.debug {
		fmt.Fprint(l.w, pc.diff)
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
	// parallel calls are all announced before any result lands, so each
	// result names the call it belongs to
	if l.batchSize > 1 && pc.label != "" {
		statusTag += fmt.Sprintf(" %s%s%s", ansiBold, pc.label, ansiReset)
	}

	trimmed := strings.TrimRight(out, "\r\n")
	byteCount := len(out)

	if !l.debug {
		fmt.Fprintf(l.w, "  %s %s(%d bytes%s)%s\n", statusTag, ansiDim, byteCount, durInfo, ansiReset)
		switch {
		case isErr && trimmed != "":
			// failures are the one thing worth reading inline
			lines := strings.Split(trimmed, "\n")
			if len(lines) > 6 {
				lines = append(lines[:6], fmt.Sprintf("… %d more lines", len(lines)-6))
			}
			fmt.Fprint(l.w, ansiRed+frameLinesPlain(truncateRunes(strings.Join(lines, "\n"), 600))+ansiReset)
		case name == "plan":
			fmt.Fprint(l.w, framePlan(out))
		case isMutatingTool(name):
			// the snippet in the result is for the model; the user already
			// saw the diff preview, so show just the summary line
			fmt.Fprint(l.w, frameLinesPlain(firstLine(out, 200)))
		case isShortOutput(out):
			fmt.Fprint(l.w, frameLinesPlain(out))
		}
		if i := strings.Index(out, "checks found problems"); i >= 0 && !isErr {
			fmt.Fprint(l.w, ansiYellow+frameLinesPlain(clip(out[i:], 900))+ansiReset)
		}
		return
	}

	fmt.Fprintf(l.w, "  %s %s(%d bytes · %s)%s\n", statusTag, ansiDim, byteCount, fmtDuration(took), ansiReset)
	for _, line := range strings.Split(trimmed, "\n") {
		fmt.Fprintf(l.w, "  %s│%s %s\n", ansiDim, ansiReset, line)
	}
	fmt.Fprintln(l.w)
}

func isMutatingTool(name string) bool {
	return name == "edit" || name == "write" || name == "apply_patch"
}

// OnNotice prints a short informational line (compaction, undo, …).
func (l *TerminalLogger) OnNotice(text string) {
	if l.plain {
		return
	}
	l.stopSpinner()
	fmt.Fprintf(l.w, "  %s↻ %s%s\n", ansiDim, text, ansiReset)
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

// fmtDuration renders a duration for display: sub-second precision while
// short, whole seconds under a minute, then humanized units — anything past
// 60s reads as "1m 05s", "2h 13m", never a raw "4500s".
func fmtDuration(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d >= 10*time.Second:
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
	return renderPlainMarkdown(text, width)
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
	return renderDiffANSI(parseDiffRows(name, args), getStdoutWidth()-6)
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

// renderDiffANSI paints diff rows under the call's │ gutter: changes as
// full-width git-style bands, context dim, hunk headers cyan. Shares row
// parsing with the TUI (parseDiffRows), capped at maxDiffLines.
func renderDiffANSI(rows []diffRow, width int) string {
	var b strings.Builder
	for i, r := range rows {
		if i >= maxDiffLines {
			n := len(rows) - maxDiffLines
			fmt.Fprintf(&b, "  %s│%s %s… %d more line%s%s\n", ansiDim, ansiReset, ansiDim, n, plural(n), ansiReset)
			break
		}
		switch r.sign {
		case "+":
			b.WriteString(paintBand(fgDiffAdd, bgDiffAdd, "+", r.text, width))
		case "-":
			b.WriteString(paintBand(fgDiffDel, bgDiffDel, "-", r.text, width))
		case "@":
			fmt.Fprintf(&b, "  %s│%s %s%s%s\n", ansiDim, ansiReset, ansiCyan, fitCells(cleanDiffText(r.text), width-1), ansiReset)
		default:
			fmt.Fprintf(&b, "  %s│%s %s  %s%s\n", ansiDim, ansiReset, ansiDim, fitCells(cleanDiffText(r.text), width-3), ansiReset)
		}
	}
	return b.String()
}

// paintBand renders one full-width diff row. Text is cleaned (tabs expanded:
// a raw tab skips cells without painting the background; control bytes from
// the file made visible) and cut to the row so it never wraps bare. Width is
// measured on the plain text, not the escapes.
func paintBand(markerFg, bg, sign, text string, width int) string {
	plain := fitCells(sign+" "+cleanDiffText(text), width-1)
	pad := width - runewidth.StringWidth(plain) - 1
	if pad < 1 {
		pad = 1
	}
	return fmt.Sprintf("  %s│%s %s%s%s%s%s\n", ansiDim, ansiReset, bg, markerFg, plain, strings.Repeat(" ", pad), ansiReset)
}

// parsePlanRow splits one rendered board line ("1. [x] scaffold") into its
// number prefix, label text, and status flags. Shared by every view.
func parsePlanRow(ln string) (num, label string, done, active bool) {
	num, label = "", ln
	if i := strings.Index(ln, ". "); i > 0 && i < 4 {
		num, label = ln[:i+1], strings.TrimSpace(ln[i+2:])
	}
	done = strings.Contains(ln, "[x]")
	active = strings.Contains(ln, "[>]") || strings.Contains(ln, "[~]") // [~]: older sessions
	label = strings.TrimSpace(strings.NewReplacer("[x]", "", "[>]", "", "[~]", "", "[ ]", "").Replace(label))
	return num, label, done, active
}

// framePlan renders the agent's checklist with status glyphs and colors.
func framePlan(board string) string {
	var b strings.Builder
	for _, ln := range strings.Split(strings.TrimRight(board, "\n"), "\n") {
		if !planRowShape.MatchString(ln) {
			continue // "plan 1/3 done:" header and other non-row lines
		}
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

const clearToEnd = "\x1b[J"

// rewindRows moves the cursor to column 0, n rows up, and clears to the end
// of the screen. Streaming sections swap raw text for the final render this
// way instead of ESC7/ESC8: a saved cursor is a fixed screen position, so once
// the stream scrolls the terminal it restores too low and leaves the top of
// the raw text behind (the final answer then shows up twice). Relative
// movement follows the content as it scrolls; liveWriter counts the rows.
func rewindRows(n int) string {
	if n <= 0 {
		return "\r" + clearToEnd
	}
	return fmt.Sprintf("\r\x1b[%dA", n) + clearToEnd
}

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
