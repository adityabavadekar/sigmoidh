package sigmoidh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	agenttools "github.com/adityabavadekar/sigmoidh/tools"
	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/tview"
	"github.com/voocel/litellm"
)

// Rendering primitives shared by the live TUI and headless observers: every
// agent event becomes one styled card appended to the stream feed.

// feed: append-only styled stream with bottom-follow
const feedMaxLines = 5000

type feed struct {
	tv     *tview.TextView
	mu     sync.Mutex
	pinned bool
	width  int       // last known inner width for band/wrap math
	tail   int       // cached wrapped line count (refreshed on paint/scroll)
	tailAt time.Time // when tail was measured
	vh     int       // cached viewport height
}

func newFeed(tv *tview.TextView) *feed {
	tv.SetDynamicColors(true).
		SetWrap(true).
		SetWordWrap(true).
		SetMaxLines(feedMaxLines).
		SetScrollable(true)
	return &feed{tv: tv, pinned: true, width: 80}
}

// write appends pre-styled content. It never touches scroll state or widget
// geometry: tview's Scroll*/rect APIs are not mutex-protected, so anything
// beyond Write must run on the UI goroutine — the TUI's paint pass follows
// the tail when pinned.
func (f *feed) write(s string) {
	if s == "" {
		return
	}
	f.mu.Lock()
	f.tv.Write([]byte(s))
	f.mu.Unlock()
}

// setTail caches the wrapped line count; call on the UI loop (paint pass).
func (f *feed) setTail(n int) {
	f.mu.Lock()
	f.tail = n
	f.tailAt = time.Now()
	f.mu.Unlock()
}

// tailFresh reports the cached count, re-measuring if older than max.
// Must run on the UI loop (GetWrappedLineCount forces a full parse).
func (f *feed) tailFresh(max time.Duration) int {
	f.mu.Lock()
	stale := time.Since(f.tailAt) > max
	f.mu.Unlock()
	if stale {
		syncWrapWidth(f.tv)
		f.setTail(f.tv.GetWrappedLineCount())
	}
	return f.tailRow()
}

func (f *feed) tailRow() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tail
}

func (f *feed) viewportH() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.vh
}

func (f *feed) setViewportH(h int) {
	f.mu.Lock()
	f.vh = h
	f.mu.Unlock()
}

// noteWidth records the stream's current inner width; call on the UI loop.
func (f *feed) noteWidth(w int) {
	if w <= 8 {
		return
	}
	f.mu.Lock()
	f.width = w
	f.mu.Unlock()
}

func (f *feed) setPinned(p bool) {
	f.mu.Lock()
	f.pinned = p
	f.mu.Unlock()
}

func (f *feed) isPinned() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pinned
}

func (f *feed) innerWidth() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.width
}

// applyIndent nests every line under a gutter prefix (sub-agents).
func applyIndent(s, indent string) string {
	if indent == "" {
		return s
	}
	return indent + strings.ReplaceAll(s, "\n", "\n"+indent)
}

// escTag escapes literal '[' so tview never eats user text as a tag.
func escTag(s string) string { return strings.ReplaceAll(s, "[", "[[") }

// incremental markdown → tview tags
// mdStream styles streaming markdown chunk-by-chunk: state (bold, inline
// code, fences, partial line) survives across Feed calls, so emphasis that
// straddles network chunks still renders. Cost is O(chunk), no re-renders.
type mdStream struct {
	fence   bool
	bold    bool
	inCode  bool
	partial strings.Builder
	table   []string // pipe-table rows held until the table ends
	width   int      // feed width for table layout; 0 = unknown
}

func (m *mdStream) Feed(chunk string) string {
	var out strings.Builder
	for _, r := range chunk {
		if r == '\r' {
			continue
		}
		if r == '\n' {
			line := m.partial.String()
			if !m.fence && strings.HasPrefix(strings.TrimSpace(line), "|") {
				// a table renders only once complete: column widths
				// depend on every row
				m.table = append(m.table, line)
				m.partial.Reset()
				continue
			}
			out.WriteString(m.flushTable())
			out.WriteString(m.flushLine())
			out.WriteByte('\n')
			continue
		}
		m.partial.WriteRune(r)
	}
	return out.String()
}

// Close flushes any unterminated line and resets open inline states.
func (m *mdStream) Close() string {
	var out strings.Builder
	if line := m.partial.String(); !m.fence && strings.HasPrefix(strings.TrimSpace(line), "|") {
		m.table = append(m.table, line)
		m.partial.Reset()
	}
	out.WriteString(m.flushTable())
	if m.partial.Len() > 0 {
		out.WriteString(m.flushLine())
	}
	m.fence, m.bold, m.inCode = false, false, false
	return out.String()
}

// flushTable lays out held pipe rows as an aligned table (header bold,
// rules dim). Rows that are not a real table render as ordinary lines.
func (m *mdStream) flushTable() string {
	rows := m.table
	m.table = nil
	if len(rows) == 0 {
		return ""
	}
	var out strings.Builder
	if len(rows) < 2 || !isTableSep(rows[1]) {
		for _, ln := range rows {
			out.WriteString(m.inline(ln) + "\n")
		}
		return out.String()
	}
	w := m.width
	if w <= 0 {
		w = 100
	}
	laid := (&plainMD{maxW: w}).table(rows)
	for i, ln := range laid {
		ln = strings.TrimPrefix(ln, mdMargin)
		switch {
		case i == 0:
			out.WriteString(tgBold + escTag(ln) + tagReset)
		case strings.Trim(ln, "─┼ ") == "": // header and row rules
			out.WriteString(tg(pal.dim) + ln + tagReset)
		default:
			out.WriteString(strings.ReplaceAll(escTag(ln), "│", tg(pal.dim)+"│"+tagReset))
		}
		out.WriteString("\n")
	}
	return out.String()
}

func (m *mdStream) flushLine() string {
	line := m.partial.String()
	m.partial.Reset()

	if m.fence {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			m.fence = false
			return ""
		}
		return tg(pal.dim) + "│ " + tg(pal.focus) + escTag(line) + tagReset
	}

	s := strings.TrimLeft(line, " ")
	switch {
	case strings.HasPrefix(s, "```"):
		m.fence = true
		m.bold, m.inCode = false, false
		return ""
	case strings.HasPrefix(s, "#"):
		head := strings.TrimLeft(strings.TrimLeft(s, "#"), " ")
		return tgBold + escTag(head) + tagReset
	case s == "---" || s == "***":
		return tg(pal.chrome) + strings.Repeat("━", 16) + tagReset
	case strings.HasPrefix(s, "> "):
		return tg(pal.dim) + "▎ " + escTag(s[2:]) + tagReset
	case strings.HasPrefix(s, "- "), strings.HasPrefix(s, "* "):
		return tg(pal.dim) + "•" + tagReset + " " + m.inline(s[2:])
	default:
		return m.inline(line)
	}
}

// inline applies bold / inline-code styling to one text run.
func (m *mdStream) inline(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		switch {
		case !m.fence && i+1 < len(rs) && rs[i] == '*' && rs[i+1] == '*':
			if m.bold {
				b.WriteString("[::-]")
			} else {
				b.WriteString(tgBold)
			}
			m.bold = !m.bold
			i++
		case rs[i] == '`':
			if m.inCode {
				b.WriteString("[-]")
			} else {
				b.WriteString(tg(pal.focus))
			}
			m.inCode = !m.inCode
		case rs[i] == '[':
			b.WriteString("[[")
		default:
			b.WriteRune(rs[i])
		}
	}
	return b.String()
}

// diffs
type diffRow struct {
	sign string
	text string
	add  bool
}

// parseDiffRows extracts structured +/- rows from tool args for tools whose
// arguments describe a change (edit/write/apply_patch). Shared shape with the
// terminal view's ANSI preview so both stay in sync.
func parseDiffRows(name string, args json.RawMessage) []diffRow {
	switch name {
	case "edit":
		var a struct {
			Path   string `json:"path"`
			OldStr string `json:"old_str"`
			NewStr string `json:"new_str"`
			Edits  []struct {
				OldStr string `json:"old_str"`
				NewStr string `json:"new_str"`
			} `json:"edits"`
		}
		if json.Unmarshal(args, &a) != nil || a.Path == "" {
			return nil
		}
		var rows []diffRow
		if a.OldStr != "" || len(a.Edits) == 0 {
			rows = lineDiff(a.OldStr, a.NewStr)
		}
		for _, e := range a.Edits { // multi-edit: one hunk per replacement
			if len(rows) > 0 {
				rows = append(rows, diffRow{"@", "⋯", true})
			}
			rows = append(rows, lineDiff(e.OldStr, e.NewStr)...)
		}
		return rows
	case "write":
		var w struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal(args, &w) != nil || w.Path == "" || w.Content == "" {
			return nil
		}
		return addRows(w.Content)
	case "apply_patch":
		var a struct {
			Patch string `json:"patch"`
		}
		if json.Unmarshal(args, &a) != nil || a.Patch == "" {
			return nil
		}
		return patchRows(a.Patch)
	}
	return nil
}

func addRows(s string) []diffRow {
	return signRows(s, "+", true)
}

func signRows(s, sign string, add bool) []diffRow {
	if s == "" {
		return nil
	}
	var rows []diffRow
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		rows = append(rows, diffRow{sign, ln, add})
	}
	return rows
}

const (
	diffAddBG = "#12301C"
	diffDelBG = "#33141B"
)

// renderDiffBands paints git-style full-width bands under a dim gutter.
func renderDiffBands(rows []diffRow, width int) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	for i, r := range rows {
		if i >= maxDiffLines {
			b.WriteString(gutterLine(
				tg(pal.dim) + "… " + strconv.Itoa(len(rows)-maxDiffLines) + " more line" + plural(len(rows)-maxDiffLines) + tagReset))
			break
		}
		switch r.sign {
		case "@":
			b.WriteString(gutterLine(tg(pal.focus) + escTag(fitCells(cleanDiffText(r.text), width-2)) + tagReset))
			continue
		case " ":
			b.WriteString(gutterLine(tg(pal.dim) + "  " + escTag(fitCells(cleanDiffText(r.text), width-4)) + tagReset))
			continue
		}
		fgC, bgC := pal.alert, hexColor(diffDelBG)
		if r.add {
			fgC, bgC = pal.mint, hexColor(diffAddBG)
		}
		// cleaned: tabs expanded (tview jumps a tab to the next stop while
		// the pad math counted 0 cells, so rows overflowed and wrapped) and
		// control bytes made visible; cut to the row; escaped so code like
		// a[i] is not a style tag
		band := fitCells(r.sign+" "+cleanDiffText(r.text), width-2)
		pad := width - runewidth.StringWidth(band) - 2
		if pad < 1 {
			pad = 1
		}
		b.WriteString(gutterLine(tgOn(fgC, bgC) + escTag(band) + strings.Repeat(" ", pad) + tagReset))
	}
	return b.String()
}

// gutterLine prefixes one pre-styled row with the stream's │ gutter.
func gutterLine(styled string) string {
	return "  " + tg(pal.dim) + "│" + tagReset + " " + styled + "\n"
}

// wrapPlain hard-wraps plain text to width, returning raw lines.
func wrapPlain(s string, width int) []string {
	if width < 20 {
		width = 20
	}
	var lines []string
	for _, para := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if para == "" {
			lines = append(lines, "")
			continue
		}
		col := 0
		var cur strings.Builder
		for _, r := range para {
			rw := runewidth.RuneWidth(r)
			if col+rw > width {
				lines = append(lines, cur.String())
				cur.Reset()
				col = 0
			}
			cur.WriteRune(r)
			col += rw
		}
		lines = append(lines, cur.String())
	}
	return lines
}

// gutterBlock renders multi-line content under the │ gutter WITHOUT
// pre-wrapping: the TextView wraps rows at its live width (and re-wraps on
// resize). Pre-wrapping against a cached width is what split commands
// mid-token while half the screen sat empty.
func gutterBlock(content string) string {
	var b strings.Builder
	gutter := tg(pal.dim) + "│" + tagReset
	for _, seg := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		if seg == "" {
			b.WriteString("  " + gutter)
		} else {
			b.WriteString("  " + gutter + " " + escTag(seg))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// event cards
// cardBoot renders the session header as pure typography: brand mark,
// version accent, tagline, and connection metadata. No frame — the
// borderless transcript owns the screen.
func cardBoot(version, provider, model2 string, readOnly bool) string {
	mode := tg(pal.dim) + "read-write" + tagReset
	if readOnly {
		mode = tg(pal.amber) + "read-only" + tagReset
	}
	if agenttools.SandboxEnabled() {
		mode += tg(pal.dim) + " · sandboxed" + tagReset
	} else {
		mode += tg(pal.amber) + " · no-sandbox" + tagReset
	}
	label := func(s string) string { return fmt.Sprintf("  %-10s ", s) }

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("  " + tg(pal.brand) + "✻" + tagReset + " " + tgBold + "SIGMOIDH" + tagReset +
		" " + tg(pal.focus) + tgBold + version + tagReset + "\n")
	b.WriteString("  " + tg(pal.dim) + "terminal coding agent" + tagReset + "\n")
	b.WriteString("\n")
	b.WriteString(label("model") + tg(pal.text) + escTag(provider+" · "+model2) + tagReset + "\n")
	b.WriteString(label("directory") + tg(pal.text) + shortCwd() + tagReset + "\n")
	b.WriteString(label("mode") + mode + "\n")
	b.WriteString("\n")
	return b.String()
}

// cardUserPrompt uses the codex convention: a dim bold '›' prefix and a
// two-space continuation indent, blank line after. Word-wrapped at the live
// stream width so lines break between words, never mid-word.
func cardUserPrompt(line string, width int) string {
	const indent = "  "
	w := maxInt(20, width-4)
	var b strings.Builder
	first := true
	for _, para := range strings.Split(line, "\n") {
		segs := wrapWords(para, w)
		if len(segs) == 0 { // blank line inside a multi-line paste
			b.WriteString("\n")
			continue
		}
		for _, seg := range segs {
			if first {
				b.WriteString(tg(pal.dim) + tgBold + "›" + tagReset + " ")
				first = false
			} else {
				b.WriteString(indent)
			}
			b.WriteString(escTag(seg) + "\n")
		}
	}
	return b.String() + "\n"
}

func cardThinkingStart() string {
	return tg(pal.dim) + "• thinking…" + tagReset + "\n"
}

func cardThinkingEnd(took time.Duration) string {
	return tg(pal.dim) + "• thought for " + fmtDuration(took) + tagReset + "\n\n"
}

// cardToolCall renders a typographic bullet row: name then dim target.
func cardToolCall(name, target, counts string, isCmd bool) string {
	s := tg(pal.text) + "•" + tagReset + " " + tgBold + name + tagReset
	if target != "" && !isCmd {
		s += "  " + tg(pal.dim) + truncateRunes(target, 88) + tagReset
	}
	if counts != "" {
		s += "  " + tg(pal.dim) + counts + tagReset
	}
	return s + "\n"
}

// cardToolResultLine closes a call with a tree branch: success is quiet,
// failures are the only loud thing in the stream.
func cardToolResultLine(name string, isErr bool, n int, took time.Duration) string {
	return cardToolResultLineFor(name, "", isErr, n, took)
}

// cardToolResultLineFor also names the call (label) — used when several calls
// ran together, since all of them are announced before any result arrives.
func cardToolResultLineFor(name, call string, isErr bool, n int, took time.Duration) string {
	meta := humanSize(n)
	if isErr || took >= slowToolTime {
		meta += " · " + fmtDuration(took)
	}
	label := "done"
	c := pal.dim
	if isErr {
		label, c = "failed", pal.alert
	} else if name == "plan" {
		label = "plan updated"
	}
	which := ""
	if call != "" {
		which = tg(pal.text) + " · " + escTag(call) + tagReset
	}
	return tg(c) + "└ " + label + tagReset + which + tg(pal.dim) + " " + meta + tagReset + "\n"
}

// callLabel names a call compactly: "web DOGE data breach 2026".
func callLabel(name string, args json.RawMessage) string {
	target, _ := toolCallSummary(name, args)
	if name == "bash" || name == "exec" {
		target = firstLine(target, 40)
	}
	return strings.TrimSpace(name + " " + truncateRunes(target, 48))
}

func cardRetry(provider string, attempt int, delay time.Duration) string {
	return tg(pal.amber) + "• rate-limited — retry " + strconv.Itoa(attempt) + "/" +
		strconv.Itoa(retriesPerProvider) + " in " + fmtDuration(delay) + tagReset + "\n\n"
}

func cardFallback(provider, model string) string {
	return tg(pal.amber) + "• " + escTag(provider) + " unavailable — falling back to " + escTag(model) + tagReset + "\n\n"
}

func cardTrigger(message string) string {
	return tg(pal.mint) + "• notified" + tagReset + tg(pal.dim) + " · " + message + tagReset + "\n\n"
}

func cardNotice(text string) string {
	return tg(pal.amber) + "• " + escTag(text) + "\n\n"
}

func cardError(err error) string {
	if err == nil {
		return ""
	}
	if isContextCancel(err) {
		return tg(pal.amber) + "• cancelled" + tagReset + "\n\n"
	}
	return tg(pal.alert) + "• " + escTag(err.Error()) + "\n\n"
}

func isContextCancel(err error) bool {
	return errors.Is(err, context.Canceled)
}

func cardDone(stats *RunStats) string {
	if stats == nil {
		return tg(pal.mint) + "• done" + tagReset + "\n\n"
	}
	meta := "turns " + strconv.Itoa(stats.TotalTurns) + " · tools " + strconv.Itoa(stats.TotalToolCalls)
	if stats.TotalTokens > 0 {
		meta += " · " + humanNumber(stats.TotalTokens) + " tok"
	}
	if stats.SubagentRuns > 0 {
		meta += " · " + strconv.Itoa(stats.SubagentRuns) + " sub-agent" + plural(stats.SubagentRuns)
	}
	meta += " · took " + fmtDuration(stats.TotalDuration)
	return tg(pal.mint) + "• done" + tagReset + tg(pal.dim) + " · " + meta + tagReset + "\n\n"
}

// cardStatsLine is the opt-in per-turn token line (/statsline on).
func cardStatsLine(u litellm.Usage) string {
	return tg(pal.dim) + "· " + humanNumber(u.TotalTokens) + " tok (" +
		humanNumber(u.InputTokens) + " in · " + humanNumber(u.OutputTokens) + " out · " +
		humanNumber(u.ReasoningTokens) + " r)" + tagReset + "\n"
}

// cardUsageGrid renders the /usage summary.
func cardUsageGrid(stats *RunStats) string {
	if stats == nil {
		return tg(pal.dim) + "no session statistics yet" + tagReset + "\n\n"
	}
	var b strings.Builder
	b.WriteString(tgBold + "usage" + tagReset + "\n")
	row := func(label, val string) {
		b.WriteString(tg(pal.dim) + label + tagReset + " " + val + "\n")
	}
	row("turns", strconv.Itoa(stats.TotalTurns))
	row("tools", strconv.Itoa(stats.TotalToolCalls))
	row("in", humanNumber(stats.TotalInputTokens)+" · out "+humanNumber(stats.TotalOutputTokens)+
		" · reasoning "+humanNumber(stats.TotalReasoningTokens))
	if stats.TotalCacheReadTokens > 0 || stats.TotalCacheWriteTokens > 0 {
		row("cache", humanNumber(stats.TotalCacheReadTokens)+" read · "+humanNumber(stats.TotalCacheWriteTokens)+" write")
	}
	if stats.SubagentRuns > 0 {
		row("sub-agents", strconv.Itoa(stats.SubagentRuns))
	}
	row("total", tgBold+humanNumber(stats.TotalTokens)+" tok"+tagReset)
	return b.String() + "\n"
}

// cardDimBlock renders captured REPL-handler output as a quiet block.
func cardDimBlock(body string) string {
	body = stripANSICodes(strings.TrimRight(body, "\n"))
	if body == "" {
		return ""
	}
	return gutterBlock(body)
}

// context meter
const ctxMeterCells = 14

func ctxMeterColor(pct float64) tcell.Color {
	switch {
	case pct >= 85:
		return pal.alert
	case pct >= 60:
		return pal.amber
	default:
		return pal.mint
	}
}

func ctxMeterBar(est, budget int) (string, tcell.Color, int) {
	if budget <= 0 {
		budget = 1
	}
	pct := 100 * float64(est) / float64(budget)
	if pct > 100 {
		pct = 100
	}
	filled := int(float64(ctxMeterCells) * pct / 100)
	c := ctxMeterColor(pct)
	bar := tg(c) + strings.Repeat("▰", filled) + tg(pal.chrome) + strings.Repeat("▱", ctxMeterCells-filled) + tagReset
	return bar, c, int(pct + 0.5)
}

// cardQueued acknowledges a prompt parked behind the active run.
func cardQueued(line string, position int) string {
	first := strings.TrimSpace(strings.SplitN(line, "\n", 2)[0])
	if len(first) > 64 {
		first = truncateRunes(first, 64)
	}
	return tg(pal.focus) + "› queued" + tagReset + tg(pal.dim) +
		fmt.Sprintf(" (#%d) · ", position) + escTag(first) + tagReset + "\n\n"
}

// cardResumed marks where replayed history ends and the live session begins.
func cardResumed(n int) string {
	return tg(pal.dim) + "· resumed " + strconv.Itoa(n) + " messages" + tagReset + "\n\n"
}

// stripTVTags removes tview dynamic-color tags (and resolves [[ escapes) so
// styled strings can be measured with runewidth.
func stripTVTags(s string) string {
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

// plainWidth is the visible cell width of a styled string.
func plainWidth(s string) int { return runewidth.StringWidth(stripTVTags(s)) }
