package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func runApplyPatch(ctx context.Context, patch string, strip int, reverse bool, workDir string) (string, bool) {
	if patch == "" {
		return "patch cannot be empty", true
	}
	if strip < 0 {
		return "strip cannot be negative", true
	}
	if path := unsafePatchPath(patch, strip); path != "" {
		return "Refused: patch target outside working directory: " + path, true
	}

	// snapshot targets first: for /undo, and to show what changed afterwards
	targets := patchTargets(patch, strip, workDir)
	before := make(map[string]string, len(targets))
	for _, t := range targets {
		if data, err := os.ReadFile(t); err == nil {
			before[t] = string(data)
		}
	}

	pctx, cancel := context.WithTimeout(ctx, bashTimeout)
	defer cancel()

	args := []string{"--forward", "--reject-file=-", "-p" + strconv.Itoa(strip)}
	if reverse {
		args = append(args, "--reverse")
	}
	for _, t := range targets {
		snapshotBefore(t)
	}
	cmd := exec.CommandContext(pctx, "patch", args...)
	cmd.Dir = workDir
	cmd.Stdin = bytes.NewBufferString(patch)
	out, err := cmd.CombinedOutput()
	text := truncate(string(out))
	if pctx.Err() == context.DeadlineExceeded {
		return text + "\n[timed out]", true
	}
	if err != nil {
		return text + fmt.Sprintf("\n[%v]", err), true
	}
	parts := []string{strings.TrimRight(text, "\n")}
	for _, t := range targets {
		after, rerr := os.ReadFile(t)
		if rerr != nil {
			continue // deleted by the patch
		}
		if snip := changedSnippet(t, before[t], string(after)); snip != "" {
			parts = append(parts, snip)
		}
		if report := postEditChecks(ctx, t, workDir); report != "" {
			parts = append(parts, report)
		}
	}
	return truncate(strings.Join(parts, "\n")), false
}

// patchTargets lists the files a unified diff touches, as absolute paths.
func patchTargets(patch string, strip int, workDir string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "--- ") && !strings.HasPrefix(line, "+++ ") {
			continue
		}
		fields := strings.Fields(line[4:])
		if len(fields) == 0 || fields[0] == "/dev/null" {
			continue
		}
		rel := stripPatchPath(fields[0], strip)
		if rel == "" {
			continue
		}
		abs := filepath.Join(workDir, rel)
		if !seen[abs] {
			seen[abs] = true
			out = append(out, abs)
		}
	}
	return out
}

func unsafePatchPath(patch string, strip int) string {
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "--- ") && !strings.HasPrefix(line, "+++ ") {
			continue
		}
		fields := strings.Fields(line[4:])
		if len(fields) == 0 || fields[0] == "/dev/null" {
			continue
		}
		path := stripPatchPath(fields[0], strip)
		if filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") {
			return fields[0]
		}
	}
	return ""
}

func stripPatchPath(path string, strip int) string {
	for i := 0; i < strip; i++ {
		idx := strings.IndexByte(path, '/')
		if idx < 0 {
			return ""
		}
		path = path[idx+1:]
	}
	return path
}
