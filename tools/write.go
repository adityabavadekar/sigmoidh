package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

func runWrite(path, content string, workDir string) (string, bool) {
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
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err.Error(), true
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), false
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
