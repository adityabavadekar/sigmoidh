package tools

import (
	"fmt"
	"os"
	"strings"
)

func runRead(path string, offset, limit int, numbered bool, workDir ...string) (string, bool) {
	path = resolveToolPath(path, workDir...)
	if isSensitiveFile(path) {
		return refusalReadSensitive, true
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error(), true
	}
	if !isTextFile(data) {
		return refusalReadBinary, true
	}

	lines := splitFileLines(string(data))
	start, end, errText := lineWindow(len(lines), offset, limit)
	if errText != "" {
		return errText, true
	}

	output := formatLines(lines[start:end], start, numbered)
	if limit > 0 && end < len(lines) {
		remaining := len(lines) - end
		output += fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]", remaining, end+1)
	}
	return truncate(output), false
}

func splitFileLines(content string) []string {
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		return lines[:len(lines)-1]
	}
	return lines
}

func lineWindow(lineCount, offset, limit int) (int, int, string) {
	start := 0
	if offset > 1 {
		start = offset - 1
	}
	if offset > 1 && start >= lineCount {
		return 0, 0, fmt.Sprintf("Offset %d is beyond end of file (%d lines total)", offset, lineCount)
	}

	end := lineCount
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return start, end, ""
}

func formatLines(lines []string, start int, numbered bool) string {
	if !numbered {
		return strings.Join(lines, "\n")
	}

	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%d\t%s\n", start+i+1, line)
	}
	return b.String()
}
