package sigmoidh

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Single source of truth for REPL slash commands: drives /help rendering,
// Tab autocompletion, classification, and keeps copy consistent everywhere.

type slashCommand struct {
	name string
	desc string
}

var slashCommands = []slashCommand{
	{"/help", "show available commands"},
	{"/usage", "session token/tool statistics"},
	{"/thinking", "model thinking display [on|off] (default: on)"},
	{"/statsline", "per-turn usage line [on|off] (default: off)"},
	{"/sandbox", "bash sandbox isolation [on|off]"},
	{"/mouse", "wheel capture [on|off] (alt+m)"},
	{"/resume", "resume a previous session [n | list]"},
	{"/copy", "copy responses to clipboard [all | last | num n]"},
	{"/undo", "revert the file changes of the last run (write/edit/apply_patch)"},
	{"/compact", "summarize older conversation to free context"},
	{"/exit", "exit sigmoidh"},
}

var bareExitWords = map[string]bool{"exit": true, "quit": true, "q": true}

// slashMatches returns commands whose name extends prefix.
func slashMatches(prefix string) []slashCommand {
	var out []slashCommand
	for _, c := range slashCommands {
		if strings.HasPrefix(c.name, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func longestCommonPrefix(names []string) string {
	if len(names) == 0 {
		return ""
	}
	p := names[0]
	for _, n := range names[1:] {
		for !strings.HasPrefix(n, p) {
			p = p[:len(p)-1]
		}
	}
	return p
}

func formatSlashRow(w io.Writer, c slashCommand) {
	fmt.Fprintf(w, "  %s%s%-11s%s %s%s%s\n",
		ansiDim, ansiReset, c.name, ansiReset, ansiDim, c.desc, ansiReset)
}

// renderSlashMenu paints ambiguous completions in house style. We print this
// ourselves because readline would paste ANSI candidates into the input line.
func renderSlashMenu(w io.Writer, matches []slashCommand) {
	fmt.Fprintln(w)
	for _, c := range matches {
		formatSlashRow(w, c)
	}
}

func printSlashHelp(w io.Writer) {
	fmt.Fprintf(w, "\n%s%s❯ commands%s\n", ansiBold, ansiMagenta, ansiReset)
	for _, c := range slashCommands {
		formatSlashRow(w, c)
	}
	fmt.Fprintf(w, "%s  tab completes · paste pastes whole blocks · ctrl+c stops a run · ctrl+d exits%s\n\n", ansiDim, ansiReset)
}

// slashCompleter adapts the registry to readline's AutoCompleter. Ambiguous
// prefixes render our styled menu on stderr; readline inserts only the shared
// prefix so ANSI never lands in the input buffer.
type slashCompleter struct{}

func (slashCompleter) Do(line []rune, pos int) ([][]rune, int) {
	prefix := string(line[:pos])
	if !strings.HasPrefix(prefix, "/") {
		return nil, 0
	}
	matches := slashMatches(strings.TrimSpace(prefix))
	if len(matches) == 0 {
		return nil, 0
	}
	names := make([]string, 0, len(matches))
	for _, c := range matches {
		names = append(names, c.name)
	}
	if len(names) > 1 {
		renderSlashMenu(os.Stderr, matches)
		return [][]rune{[]rune(strings.TrimPrefix(longestCommonPrefix(names), prefix))}, pos
	}
	return [][]rune{[]rune(strings.TrimPrefix(names[0], prefix))}, pos
}
