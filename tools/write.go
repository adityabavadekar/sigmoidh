package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func runWrite(ctx context.Context, path, content string, workDir string) (string, bool) {
	path = resolveToolPath(path, workDir)
	if isSensitiveFile(path) {
		return refusalWriteSensitive, true
	}
	if isProtectedCommandPath(path, workDir) {
		return refusalWriteProtected, true
	}
	if err := ensureParentDir(path); err != nil {
		return err.Error(), true
	}
	before, readErr := os.ReadFile(path)
	snapshotBefore(path)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err.Error(), true
	}
	summary := fmt.Sprintf("wrote %d bytes to %s (new file)", len(content), path)
	snippet := ""
	if readErr == nil {
		summary = fmt.Sprintf("wrote %d bytes to %s (replaced %d bytes)", len(content), path, len(before))
		// an overwrite shows what changed; a new file's content is already
		// in the call, so echoing it back would only cost tokens
		snippet = changedSnippet(path, string(before), content)
	}
	parts := []string{summary}
	if snippet != "" {
		parts = append(parts, snippet)
	}
	if report := postEditChecks(ctx, path, workDir); report != "" {
		parts = append(parts, report)
	}
	return strings.Join(parts, "\n"), false
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
