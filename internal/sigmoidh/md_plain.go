package sigmoidh

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// renderPlainMarkdown lays out model markdown as plain terminal text for the
// CLI (answers and the final thinking block). It replaces glamour, whose
// output we stripped of ANSI anyway and which got the layout wrong: wrapped
// list items and quotes fell back to the left margin (no hanging indent),
// quotes left orphan words on their own row, and inline code came out padded
// with spaces ("  main.go  ").
//
// Source line breaks are kept (chat text uses them as real breaks); lines
// longer than the terminal wrap at word boundaries under their own prefix.
// The result starts with a blank line and ends with a blank line, matching
// the spacing the rest of the CLI chrome expects.
func renderPlainMarkdown(text string, width int) string {
	if width < 20 {
		width = 20
	}
	r := &plainMD{maxW: width - 2} // 2-col left margin, 2-col right margin
	r.render(strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n"))
	body := strings.TrimRight(strings.Join(r.out, "\n"), "\n ")
	if strings.TrimSpace(body) == "" {
		return ""
	}
	return "\n" + body + "\n\n"
}

const mdMargin = "  "

var (
	mdListRe    = regexp.MustCompile(`^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$`)
	mdHeadingRe = regexp.MustCompile(`^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)
	mdRuleRe    = regexp.MustCompile(`^\s{0,3}(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)
	mdTableSep  = regexp.MustCompile(`^\s*:?-{1,}:?\s*$`)
)

type plainMD struct {
	maxW int
	out  []string

	listIndents []int  // open list items' marker columns (nesting stack)
	itemRest    string // hanging prefix of the current list item
	itemCol     int    // source column where the current item's text starts
}

func (r *plainMD) emit(lines ...string) { r.out = append(r.out, lines...) }

// blank adds one separating blank line (never two in a row, never leading).
func (r *plainMD) blank() {
	if n := len(r.out); n > 0 && r.out[n-1] != "" {
		r.out = append(r.out, "")
	}
}

func (r *plainMD) endList() {
	r.listIndents, r.itemRest, r.itemCol = nil, "", 0
}

func (r *plainMD) render(lines []string) {
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		trimmed := strings.TrimSpace(ln)

		// fenced code: verbatim, tabs expanded, never wrapped or restyled
		if fence := fenceMarker(trimmed); fence != "" {
			indent := leadingCols(ln)
			prefix := mdMargin + "  "
			if r.itemRest != "" && indent >= r.itemCol {
				prefix = r.itemRest + "  "
			}
			i++
			for ; i < len(lines); i++ {
				if strings.HasPrefix(strings.TrimSpace(lines[i]), fence) {
					break
				}
				code := strings.TrimRight(expandTabs(stripCols(lines[i], indent)), " ")
				if code == "" {
					r.emit("")
				} else {
					r.emit(prefix + code)
				}
			}
			continue
		}

		switch {
		case trimmed == "":
			r.blank()
			continue

		case mdHeadingRe.MatchString(ln):
			r.endList()
			r.blank()
			m := mdHeadingRe.FindStringSubmatch(ln)
			r.emit(r.wrap(inlinePlain(m[2]), mdMargin, mdMargin)...)
			continue

		case mdRuleRe.MatchString(ln):
			r.endList()
			r.emit(mdMargin + strings.Repeat("─", minInt(r.maxW-len(mdMargin), 40)))
			continue

		case strings.HasPrefix(trimmed, "|") && i+1 < len(lines) && isTableSep(lines[i+1]):
			r.endList()
			j := i
			var rows []string
			for ; j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "|"); j++ {
				rows = append(rows, lines[j])
			}
			r.emit(r.table(rows)...)
			i = j - 1
			continue

		case strings.HasPrefix(trimmed, ">"):
			r.endList()
			depth, body := 0, trimmed
			for strings.HasPrefix(body, ">") {
				depth++
				body = strings.TrimLeft(body[1:], " ")
			}
			bar := mdMargin + strings.Repeat("│ ", depth)
			if body == "" {
				r.emit(strings.TrimRight(bar, " "))
			} else {
				r.emit(r.wrap(inlinePlain(body), bar, bar)...)
			}
			continue
		}

		if m := mdListRe.FindStringSubmatch(ln); m != nil {
			col := leadingCols(m[1])
			for len(r.listIndents) > 0 && r.listIndents[len(r.listIndents)-1] > col {
				r.listIndents = r.listIndents[:len(r.listIndents)-1]
			}
			if len(r.listIndents) == 0 || r.listIndents[len(r.listIndents)-1] < col {
				r.listIndents = append(r.listIndents, col)
			}
			level := len(r.listIndents) - 1
			marker := m[2]
			if marker == "-" || marker == "*" || marker == "+" {
				marker = [...]string{"•", "◦", "▪"}[level%3]
			}
			nest := mdMargin + strings.Repeat("  ", level)
			first := nest + marker + " "
			r.itemRest = nest + strings.Repeat(" ", runewidth.StringWidth(marker)+1)
			r.itemCol = col + len(m[2]) + 1
			r.emit(r.wrap(inlinePlain(m[3]), first, r.itemRest)...)
			continue
		}

		// continuation text inside a list item stays under its hanging indent
		if r.itemRest != "" && (leadingCols(ln) > 0 || !r.lastWasBlank()) {
			r.emit(r.wrap(inlinePlain(trimmed), r.itemRest, r.itemRest)...)
			continue
		}
		r.endList()
		r.emit(r.wrap(inlinePlain(trimmed), mdMargin, mdMargin)...)
	}
}

func (r *plainMD) lastWasBlank() bool {
	return len(r.out) > 0 && r.out[len(r.out)-1] == ""
}

// wrap word-wraps text so the first row starts with first and every other
// row with rest; words wider than a whole row are hard-split.
func (r *plainMD) wrap(text, first, rest string) []string {
	var rows []string
	cur, prefix := "", first
	avail := func() int { return maxInt(8, r.maxW-runewidth.StringWidth(prefix)) }
	flush := func() {
		rows = append(rows, strings.TrimRight(prefix+cur, " "))
		cur, prefix = "", rest
	}
	for _, word := range strings.Fields(text) {
		for word != "" {
			ww := runewidth.StringWidth(word)
			switch {
			case cur == "" && ww <= avail():
				cur, word = word, ""
			case cur != "" && runewidth.StringWidth(cur)+1+ww <= avail():
				cur, word = cur+" "+word, ""
			case cur != "":
				flush()
			default: // longer than a full row
				head := runewidth.Truncate(word, avail(), "")
				if head == "" {
					_, n := utf8.DecodeRuneInString(word)
					head = word[:n]
				}
				cur, word = head, word[len(head):]
				flush()
			}
		}
	}
	if cur != "" || len(rows) == 0 {
		flush()
	}
	return rows
}

// table renders a pipe table with aligned columns; if it cannot fit the
// terminal, rows degrade to "a │ b" lines that wrap like text.
func (r *plainMD) table(src []string) []string {
	var rows [][]string
	var aligns []byte
	for i, ln := range src {
		cells := splitTableRow(ln)
		if i == 1 && isTableSep(ln) {
			for _, c := range cells {
				c = strings.TrimSpace(c)
				switch {
				case strings.HasSuffix(c, ":") && !strings.HasPrefix(c, ":"):
					aligns = append(aligns, 'r')
				default:
					aligns = append(aligns, 'l')
				}
			}
			continue
		}
		for j := range cells {
			cells[j] = inlinePlain(strings.TrimSpace(cells[j]))
		}
		rows = append(rows, cells)
	}
	cols := 0
	for _, row := range rows {
		cols = maxInt(cols, len(row))
	}
	widths := make([]int, cols)
	for _, row := range rows {
		for j, c := range row {
			widths[j] = maxInt(widths[j], runewidth.StringWidth(c))
		}
	}
	// too wide: shrink the widest columns and wrap text inside cells, so it
	// stays a readable table instead of degrading into run-on lines
	avail := r.maxW - len(mdMargin) - 3*(cols-1)
	sum := 0
	for _, w := range widths {
		sum += w
	}
	const minCol = 8 // wrap() never goes narrower
	for sum > avail {
		widest := 0
		for j := range widths {
			if widths[j] > widths[widest] {
				widest = j
			}
		}
		if widths[widest] <= minCol {
			break
		}
		widths[widest]--
		sum--
	}
	var out []string
	if sum > avail { // too many columns for even narrow ones
		for _, row := range rows {
			out = append(out, r.wrap(strings.Join(row, " │ "), mdMargin, mdMargin+"  ")...)
		}
		return out
	}

	rule := func(joint string) string {
		parts := make([]string, cols)
		for j, w := range widths {
			parts[j] = strings.Repeat("─", w)
		}
		return mdMargin + strings.Join(parts, joint)
	}
	wrapped := false
	cellLines := make([][][]string, len(rows))
	for i, row := range rows {
		cellLines[i] = make([][]string, cols)
		for j := 0; j < cols; j++ {
			c := ""
			if j < len(row) {
				c = row[j]
			}
			cellLines[i][j] = wrapCell(c, widths[j])
			if len(cellLines[i][j]) > 1 {
				wrapped = true
			}
		}
	}
	for i := range rows {
		height := 1
		for _, cl := range cellLines[i] {
			height = max(height, len(cl))
		}
		for k := 0; k < height; k++ {
			cells := make([]string, cols)
			for j := range cells {
				c := ""
				if k < len(cellLines[i][j]) {
					c = cellLines[i][j][k]
				}
				pad := strings.Repeat(" ", max(0, widths[j]-runewidth.StringWidth(c)))
				if j < len(aligns) && aligns[j] == 'r' {
					cells[j] = pad + c
				} else {
					cells[j] = c + pad
				}
			}
			out = append(out, strings.TrimRight(mdMargin+strings.Join(cells, " │ "), " "))
		}
		switch {
		case i == 0:
			out = append(out, rule("─┼─"))
		case wrapped && i < len(rows)-1:
			out = append(out, rule("─┼─")) // multi-line rows need a visible boundary
		}
	}
	return out
}

// wrapCell word-wraps cell text to width w, hard-splitting long words
// (URLs) so a cell never exceeds its column.
func wrapCell(text string, w int) []string {
	r := &plainMD{maxW: w}
	lines := r.wrap(text, "", "")
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func splitTableRow(ln string) []string {
	s := strings.TrimSpace(ln)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	return strings.Split(s, "|")
}

func isTableSep(ln string) bool {
	if !strings.Contains(ln, "-") || !strings.HasPrefix(strings.TrimSpace(ln), "|") {
		return false
	}
	for _, c := range splitTableRow(ln) {
		if !mdTableSep.MatchString(c) {
			return false
		}
	}
	return true
}

func fenceMarker(trimmed string) string {
	for _, f := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, f) {
			return f
		}
	}
	return ""
}

// leadingCols measures leading whitespace in columns (tab = 4).
func leadingCols(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4 - n%4
		default:
			return n
		}
	}
	return n
}

// stripCols removes up to n columns of leading whitespace.
func stripCols(s string, n int) string {
	for n > 0 && s != "" {
		switch s[0] {
		case ' ':
			n--
		case '\t':
			n -= 4
		default:
			return s
		}
		s = s[1:]
	}
	return s
}

// expandTabs turns tabs into spaces at 4-column stops: a raw tab jumps the
// cursor without regard to our margin, misaligning indented code.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := 4 - col%4
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col += runewidth.RuneWidth(r)
	}
	return b.String()
}

// inlinePlain resolves inline markdown to plain text: code spans keep their
// content verbatim (no padding), links become "text (url)", and emphasis
// markers are dropped — but only where they act as markers, so snake_case
// and a * b survive.
func inlinePlain(s string) string {
	rs := []rune(s)
	var b strings.Builder
	isWord := func(i int) bool {
		return i >= 0 && i < len(rs) && (unicode.IsLetter(rs[i]) || unicode.IsDigit(rs[i]))
	}
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\' && i+1 < len(rs) && strings.ContainsRune("\\`*_{}[]()#+-.!|~<>", rs[i+1]):
			b.WriteRune(rs[i+1])
			i++

		case c == '`':
			run := 1
			for i+run < len(rs) && rs[i+run] == '`' {
				run++
			}
			ticks := strings.Repeat("`", run)
			rest := string(rs[i+run:])
			end := strings.Index(rest, ticks)
			if end < 0 {
				b.WriteString(ticks) // unmatched: literal
				i += run - 1
				continue
			}
			code := rest[:end]
			if len(code) > 1 && code[0] == ' ' && code[len(code)-1] == ' ' {
				code = code[1 : len(code)-1]
			}
			b.WriteString(code)
			i += run + utf8.RuneCountInString(rest[:end]) + run - 1

		case c == '!' && i+1 < len(rs) && rs[i+1] == '[':
			if text, _, n, ok := mdLink(rs[i+1:]); ok {
				b.WriteString(inlinePlain(text))
				i += n
				continue
			}
			b.WriteRune(c)

		case c == '[':
			if text, url, n, ok := mdLink(rs[i:]); ok {
				label := inlinePlain(text)
				b.WriteString(label)
				if url != "" && url != label && !strings.HasPrefix(url, "#") {
					b.WriteString(" (" + url + ")")
				}
				i += n - 1
				continue
			}
			b.WriteRune(c)

		case c == '<' && strings.HasPrefix(string(rs[i:]), "<http"):
			end := i + 1
			for end < len(rs) && rs[end] != '>' && rs[end] != ' ' {
				end++
			}
			if end < len(rs) && rs[end] == '>' {
				b.WriteString(string(rs[i+1 : end]))
				i = end
				continue
			}
			b.WriteRune(c)

		case (c == '*' || c == '~') && i+1 < len(rs) && rs[i+1] == c:
			i++ // ** / ~~ are always markers

		case c == '*' || c == '_':
			n := 1
			for i+n < len(rs) && rs[i+n] == c {
				n++
			}
			// a marker touches a word on exactly one side; snake_case,
			// __init__ inside words, and lone " * " are text
			if isWord(i-1) != isWord(i+n) && !(c == '_' && isWord(i-1) && isWord(i+n)) {
				i += n - 1
				continue
			}
			b.WriteString(string(rs[i : i+n]))
			i += n - 1

		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// mdLink parses "[text](url)" at the start of rs, returning the parts and
// how many runes it spans.
func mdLink(rs []rune) (text, url string, n int, ok bool) {
	depth := 0
	for i, c := range rs {
		switch c {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				if i+1 >= len(rs) || rs[i+1] != '(' {
					return "", "", 0, false
				}
				for j := i + 2; j < len(rs); j++ {
					if rs[j] == ')' {
						// (url "title"): keep only the url
						fields := strings.Fields(string(rs[i+2 : j]))
						if len(fields) == 0 {
							fields = []string{""}
						}
						return string(rs[1:i]), fields[0], j + 1, true
					}
				}
				return "", "", 0, false
			}
		}
	}
	return "", "", 0, false
}
