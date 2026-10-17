package tools

import (
	"fmt"
	"os"
	"strings"
)

func runRead(path string, offset, limit int, numbered bool, workDir ...string) (string, bool) {
	if p := strings.ToLower(strings.TrimSpace(path)); strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		// joined onto the workdir this became "open …/https:/host/…: no such file"
		return fmt.Sprintf("read only opens local files. To fetch %s use the web tool with url set to it.", strings.TrimSpace(path)), true
	}
	path = resolveToolPath(path, workDir...)
	if isSensitiveFile(path) {
		return refusalReadSensitive, true
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		// models often "read" a directory; answer with its listing instead of "is a directory"
		wd := ""
		if len(workDir) > 0 {
			wd = workDir[0]
		}
		listing, isErr := runListFiles(path, 200, wd)
		if isErr {
			return listing, true
		}
		return fmt.Sprintf("%s is a directory; its entries (use read on a file, or list_files with pattern to search):\n%s", path, listing), false
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

	// cut whole lines to fit the output budget, and say exactly where to
	// continue (a byte cut mid-line gave no usable offset)
	budget := maxOutput - 200
	size := 0
	for i := start; i < end; i++ {
		size += len(lines[i]) + 1
		if numbered {
			size += 8
		}
		if size > budget && i > start {
			end = i
			break
		}
	}
	output := formatLines(lines[start:end], start, numbered)
	if end < len(lines) {
		output = strings.TrimRight(output, "\n")
		output += fmt.Sprintf("\n\n[lines %d-%d of %d shown. Continue with offset=%d; code_nav outline or search can find a specific part.]",
			start+1, end, len(lines), end+1)
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
