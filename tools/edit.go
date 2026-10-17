package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// editOp is one old_str → new_str replacement.
type editOp struct {
	OldStr string `json:"old_str"`
	NewStr string `json:"new_str"`
}

// runEdit applies one or more replacements to a file atomically: every edit
// must apply (in order, each to the result of the previous) or nothing is
// written. The result shows the changed lines with context, plus problems
// reported by post-edit checks.
func runEdit(ctx context.Context, path string, edits []editOp, workDir string) (string, bool) {
	path = resolveToolPath(path, workDir)
	if isSensitiveFile(path) {
		return refusalEditSensitive, true
	}
	if isProtectedCommandPath(path, workDir) {
		return refusalEditProtected, true
	}
	if len(edits) == 0 {
		return editErrEmptyOldStr, true
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error(), true
	}
	if !isTextFile(data) {
		return refusalEditBinary, true
	}

	content := string(data)
	updated := content
	var notes []string
	for i, e := range edits {
		next, errMsg := replaceUnique(updated, e.OldStr, e.NewStr)
		if errMsg == editErrNotFound {
			if fuzzy, ok := replaceIgnoringIndent(updated, e.OldStr, e.NewStr); ok {
				next, errMsg = fuzzy, ""
				notes = append(notes, fmt.Sprintf("edit %d: old_str matched ignoring indentation, new_str re-indented to the file's style", i+1))
			}
		}
		if errMsg != "" {
			if len(edits) > 1 {
				errMsg = fmt.Sprintf("edit %d of %d: %s No edits were applied.", i+1, len(edits), errMsg)
			}
			return errMsg, true
		}
		updated = next
	}

	snapshotBefore(path)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return err.Error(), true
	}
	return mutationResult(ctx, fmt.Sprintf("edited %s (%d bytes -> %d bytes)", path, len(content), len(updated)),
		notes, path, content, updated, workDir), false
}

// mutationResult assembles a mutating tool's reply: summary, notes, the
// changed region and post-edit check problems.
func mutationResult(ctx context.Context, summary string, notes []string, path, before, after, workDir string) string {
	parts := []string{summary}
	parts = append(parts, notes...)
	if snip := changedSnippet(path, before, after); snip != "" {
		parts = append(parts, snip)
	}
	if report := postEditChecks(ctx, path, workDir); report != "" {
		parts = append(parts, report)
	}
	return strings.Join(parts, "\n")
}

// replaceIgnoringIndent is the fallback when old_str is not found verbatim.
// Models routinely retype tab-indented code with spaces (or drop trailing
// whitespace), so every exact match fails and they burn turns retrying. Here
// whole lines are compared with surrounding whitespace trimmed; exactly one
// matching block must exist. new_str is re-indented from old_str's style to
// the file's so the edit does not mix tabs and spaces.
func replaceIgnoringIndent(content, oldStr, newStr string) (string, bool) {
	oldLines := trimBlankEdges(strings.Split(oldStr, "\n"))
	if len(oldLines) == 0 {
		return "", false
	}
	fileLines := strings.Split(content, "\n")
	at := -1
	for i := 0; i+len(oldLines) <= len(fileLines); i++ {
		if linesMatchTrimmed(fileLines[i:i+len(oldLines)], oldLines) {
			if at >= 0 {
				return "", false // ambiguous: refuse rather than guess
			}
			at = i
		}
	}
	if at < 0 {
		return "", false
	}
	matched := fileLines[at : at+len(oldLines)]
	newLines := reindent(trimBlankEdges(strings.Split(newStr, "\n")), oldLines, matched)
	out := append(append(append([]string{}, fileLines[:at]...), newLines...), fileLines[at+len(oldLines):]...)
	return strings.Join(out, "\n"), true
}

func trimBlankEdges(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func linesMatchTrimmed(a, b []string) bool {
	for i := range a {
		if strings.TrimSpace(a[i]) != strings.TrimSpace(b[i]) {
			return false
		}
	}
	return true
}

func leadingWS(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// indentWidth measures leading whitespace with tabs counted as tabWidth.
func indentWidth(ws string, tabWidth int) int {
	n := 0
	for _, r := range ws {
		if r == '\t' {
			n += tabWidth
		} else {
			n++
		}
	}
	return n
}

// reindent maps each new line's indentation from the model's style (seen in
// oldLines) to the file's (seen in the matched lines). Indents that appear in
// the matched pairs map directly; others go through indent levels.
func reindent(newLines, oldLines, matched []string) []string {
	direct := map[string]string{}
	modelUnit, fileUnit := 0, ""
	for i := range oldLines {
		if strings.TrimSpace(oldLines[i]) == "" {
			continue
		}
		mw, fw := leadingWS(oldLines[i]), leadingWS(matched[i])
		direct[mw] = fw
		// both units come from the same (shallowest indented) line pair
		if w := indentWidth(mw, 4); w > 0 && fw != "" && (modelUnit == 0 || w < modelUnit) {
			modelUnit, fileUnit = w, fw
		}
	}
	if k := len(fileUnit); k > 1 && strings.Trim(fileUnit, "\t") == "" && modelUnit%k == 0 {
		modelUnit, fileUnit = modelUnit/k, "\t" // "\t\t" ↔ 8 spaces: one level is a tab
	}
	out := make([]string, len(newLines))
	for i, ln := range newLines {
		ws := leadingWS(ln)
		body := ln[len(ws):]
		switch fw, ok := direct[ws]; {
		case ok:
			out[i] = fw + body
		case ws == "" || modelUnit == 0 || fileUnit == "":
			out[i] = ln
		default:
			// unseen depth: convert by indent level
			out[i] = strings.Repeat(fileUnit, indentWidth(ws, 4)/modelUnit) + body
		}
	}
	return out
}

func replaceUnique(content, oldStr, newStr string) (string, string) {
	if oldStr == "" {
		return "", editErrEmptyOldStr
	}
	switch n := strings.Count(content, oldStr); {
	case n == 0:
		return "", editErrNotFound
	case n > 1:
		return "", editErrAmbiguous
	}
	return strings.Replace(content, oldStr, newStr, 1), ""
}
