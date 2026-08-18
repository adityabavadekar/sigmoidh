package tools

import (
	"fmt"
	"os"
	"strings"
)

func runEdit(path, oldStr, newStr string, workDir string) (string, bool) {
	path = resolveToolPath(path, workDir)
	if isSensitiveFile(path) {
		return refusalEditSensitive, true
	}
	if isProtectedCommandPath(path, workDir) {
		return refusalEditProtected, true
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error(), true
	}
	if !isTextFile(data) {
		return refusalEditBinary, true
	}

	content := string(data)
	updated, errMsg := replaceUnique(content, oldStr, newStr)
	if errMsg != "" {
		return errMsg, true
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return err.Error(), true
	}
	return fmt.Sprintf("edited %s (%d bytes -> %d bytes)", path, len(content), len(updated)), false
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
