package tools

import (
	"fmt"
	"strings"
)

func runBatchRead(paths []string, offset, limit int, numbered bool, workDir string) (string, bool) {
	if len(paths) == 0 {
		return "paths cannot be empty", true
	}

	var b strings.Builder
	anyErr := false
	for i, path := range paths {
		if i > 0 {
			b.WriteString("\n\n")
		}
		out, isErr := runRead(path, offset, limit, numbered, workDir)
		if isErr {
			anyErr = true
		}
		fmt.Fprintf(&b, "==> %s", path)
		if isErr {
			b.WriteString(" [error]")
		}
		b.WriteString("\n")
		b.WriteString(out)
	}
	return truncate(b.String()), anyErr
}
