package sigmoidh

import (
	"strings"
	"unicode"

	"github.com/mattn/go-runewidth"
)

// Diff rows shared by the CLI and TUI previews. diffRow.sign is "+" / "-"
// for changed lines, " " for unchanged context and "@" for hunk headers and
// elision markers.

// diffContext is how many unchanged lines stay visible around a change.
const diffContext = 2

// lineDiff turns an edit's old_str/new_str into a line-level diff. Showing
// the whole old block as removed and the whole new block as added made every
// unchanged line appear twice (a one-line doc comment rendered as -6 +8).
func lineDiff(oldStr, newStr string) []diffRow {
	a, b := diffLines(oldStr), diffLines(newStr)
	if len(a)*len(b) > 250_000 { // LCS table too big: plain replace
		return append(signRows(oldStr, "-", false), signRows(newStr, "+", true)...)
	}
	// lcs[i][j] = LCS length of a[i:] and b[j:]
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var rows []diffRow
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			rows = append(rows, diffRow{" ", a[i], true})
			i, j = i+1, j+1
		case j < len(b) && (i == len(a) || lcs[i][j+1] > lcs[i+1][j]):
			rows = append(rows, diffRow{"+", b[j], true})
			j++
		default:
			rows = append(rows, diffRow{"-", a[i], false})
			i++
		}
	}
	return groupChanges(trimContext(rows))
}

func diffLines(s string) []string {
	s = strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// trimContext keeps diffContext unchanged lines around each change; longer
// interior runs collapse into one "⋯" row, outer runs are dropped.
func trimContext(rows []diffRow) []diffRow {
	near := make([]bool, len(rows))
	changed := false
	for i, r := range rows {
		if r.sign == " " {
			continue
		}
		changed = true
		for k := max(0, i-diffContext); k <= min(len(rows)-1, i+diffContext); k++ {
			near[k] = true
		}
	}
	if !changed {
		return nil // identical: nothing to preview
	}
	var out []diffRow
	gap := false
	for i, r := range rows {
		if near[i] {
			if gap && len(out) > 0 {
				out = append(out, diffRow{"@", "⋯", true})
			}
			out = append(out, r)
			gap = false
			continue
		}
		gap = true
	}
	return out
}

// groupChanges orders each run of changes as all removals, then all
// additions, the way unified diffs read.
func groupChanges(rows []diffRow) []diffRow {
	out := make([]diffRow, 0, len(rows))
	for i := 0; i < len(rows); {
		if rows[i].sign != "+" && rows[i].sign != "-" {
			out = append(out, rows[i])
			i++
			continue
		}
		j := i
		for j < len(rows) && (rows[j].sign == "+" || rows[j].sign == "-") {
			j++
		}
		for _, r := range rows[i:j] {
			if r.sign == "-" {
				out = append(out, r)
			}
		}
		for _, r := range rows[i:j] {
			if r.sign == "+" {
				out = append(out, r)
			}
		}
		i = j
	}
	return out
}

// patchRows parses a unified diff. File headers (---/+++) only exist before
// the first hunk, so inside a hunk "--- x" is a removed line "-- x".
func patchRows(patch string) []diffRow {
	var rows []diffRow
	inHunk := false
	for _, ln := range diffLines(patch) {
		switch {
		case strings.HasPrefix(ln, "@@"):
			inHunk = true
			rows = append(rows, diffRow{"@", ln, true})
		case !inHunk && (strings.HasPrefix(ln, "---") || strings.HasPrefix(ln, "+++") ||
			strings.HasPrefix(ln, "diff ") || strings.HasPrefix(ln, "index ")):
			continue
		case strings.HasPrefix(ln, `\`): // "\ No newline at end of file"
			continue
		case strings.HasPrefix(ln, "+"):
			rows = append(rows, diffRow{"+", ln[1:], true})
		case strings.HasPrefix(ln, "-"):
			rows = append(rows, diffRow{"-", ln[1:], false})
		default:
			rows = append(rows, diffRow{" ", strings.TrimPrefix(ln, " "), true})
		}
	}
	return rows
}

// diffCounts tallies added and removed lines.
func diffCounts(rows []diffRow) (added, removed int) {
	for _, r := range rows {
		switch r.sign {
		case "+":
			added++
		case "-":
			removed++
		}
	}
	return added, removed
}

// cleanDiffText makes file content safe to paint: tabs expand to 4-column
// stops (a raw tab skips cells without painting the band background), and
// control characters — \r, ESC and friends from the file — become visible
// placeholders instead of moving the cursor or restyling the terminal.
func cleanDiffText(s string) string {
	var b strings.Builder
	col := 0
	for _, r := range s {
		switch {
		case r == '\t':
			n := 4 - col%4
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case r == '\r':
			// dropped: CRLF files would otherwise rewind to column 0
		case unicode.IsControl(r):
			b.WriteRune('�')
			col++
		default:
			b.WriteRune(r)
			col += runewidth.RuneWidth(r)
		}
	}
	return b.String()
}

// fitCells truncates s to at most w cells, marking the cut with "…", so a
// long line stays one band row instead of wrapping without its background.
func fitCells(s string, w int) string {
	if w <= 1 || runewidth.StringWidth(s) <= w {
		return s
	}
	return runewidth.Truncate(s, w, "…")
}
