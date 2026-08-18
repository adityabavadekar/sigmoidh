package tools

import (
	"bytes"
	"context"
	"fmt"
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

	ctx, cancel := context.WithTimeout(ctx, bashTimeout)
	defer cancel()

	args := []string{"--forward", "--reject-file=-", "-p" + strconv.Itoa(strip)}
	if reverse {
		args = append(args, "--reverse")
	}
	cmd := exec.CommandContext(ctx, "patch", args...)
	cmd.Dir = workDir
	cmd.Stdin = bytes.NewBufferString(patch)
	out, err := cmd.CombinedOutput()
	text := truncate(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return text + "\n[timed out]", true
	}
	if err != nil {
		return text + fmt.Sprintf("\n[%v]", err), true
	}
	return text, false
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
