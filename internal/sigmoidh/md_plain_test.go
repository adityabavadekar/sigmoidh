package sigmoidh

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

func plainRows(md string, width int) []string {
	return strings.Split(strings.Trim(renderPlainMarkdown(md, width), "\n"), "\n")
}

// Wrapped bullets hang under their text, not under the bullet (glamour fell
// back to the margin), and inline code is not padded with spaces.
func TestPlainMarkdownListHangingIndent(t *testing.T) {
	rows := plainRows("- Renamed files under `internal/sigmoidh/` (e.g., `ask_user.go` → `internal/sigmoidh/ask_user.go`, `budget.go` → `internal/sigmoidh/budget.go`)", 50)
	if len(rows) < 3 {
		t.Fatalf("expected wrapping: %q", rows)
	}
	if !strings.HasPrefix(rows[0], "  • Renamed files under internal/sigmoidh/") {
		t.Fatalf("first row %q", rows[0])
	}
	for _, r := range rows[1:] {
		if !strings.HasPrefix(r, "    ") || strings.HasPrefix(r, "     ") {
			t.Fatalf("continuation not hung under the text: %q", r)
		}
	}
	for _, r := range rows {
		if strings.Contains(strings.TrimLeft(r, " "), "  ") || strings.Contains(r, "`") {
			t.Fatalf("inline code padded or unresolved: %q", r)
		}
		if w := runewidth.StringWidth(r); w > 48 {
			t.Fatalf("row %d wide > 48: %q", w, r)
		}
	}
}

func TestPlainMarkdownBlocks(t *testing.T) {
	md := "1. first item that is long enough to wrap past the width\n  - nested\n\n> quoted text that is long enough to wrap past the width\n\n| a | b |\n|---|--:|\n| xx | 1 |\n\n```go\n\tx := a[i] // *not* emphasis\n```\nsnake_case **bold** [docs](https://x.io)"
	out := renderPlainMarkdown(md, 40)
	for _, want := range []string{
		"  1. first item", "\n     ", // numbered item hangs under its text
		"    ◦ nested",
		"  │ quoted text", "\n  │ ",
		"  a  │ b", "──┼──", "  xx │ 1",
		"      x := a[i] // *not* emphasis", // code verbatim, tab expanded
		"snake_case bold docs (https://x.io)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\t") {
		t.Fatalf("raw tab in output:\n%q", out)
	}
}

func TestInlinePlainKeepsLiteralMarkers(t *testing.T) {
	for in, want := range map[string]string{
		"a * b":           "a * b",
		"__init__ method": "init method",
		"my_var_name":     "my_var_name",
		"*em* and _em_":   "em and em",
		"`a  b`":          "a  b",
		"``a`b``":         "a`b",
		`\*literal\*`:     "*literal*",
		"![alt](x.png)":   "alt",
	} {
		if got := inlinePlain(in); got != want {
			t.Errorf("inlinePlain(%q) = %q, want %q", in, got, want)
		}
	}
}

// A table wider than the terminal wraps text inside its columns (and keeps
// every row within the width) instead of collapsing into run-on lines.
func TestPlainMarkdownWideTableWrapsCells(t *testing.T) {
	md := "| Project | Description | Link |\n|---|---|---|\n| BYOB (Build Your Own Botnet) | Educational framework with a C2 server, payload generator and many modules | https://hunt.io/malware-families/byob |\n| other | short | https://example.com/x |\n"
	rows := plainRows(md, 80)
	sepCol := -1
	for _, r := range rows {
		if w := runewidth.StringWidth(r); w > 78 {
			t.Fatalf("row %d wide > 78: %q", w, r)
		}
		if i := strings.Index(r, "│"); i >= 0 {
			if sepCol == -1 {
				sepCol = runewidth.StringWidth(r[:i])
			} else if runewidth.StringWidth(r[:i]) != sepCol {
				t.Fatalf("columns not aligned:\n%s", strings.Join(rows, "\n"))
			}
		}
	}
	if len(rows) < 6 || !strings.Contains(strings.Join(rows, "\n"), "─┼─") {
		t.Fatalf("expected wrapped cells with row rules:\n%s", strings.Join(rows, "\n"))
	}
}
