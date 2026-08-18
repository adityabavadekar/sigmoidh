package tools

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	noMatches      = "No matches found."
	maxLineLen     = 300
	maxFileByteCap = 10 * 1024 * 1024 // 10MB
	maxScannerBuf  = 512 * 1024       // 512KB max line buffer
)

var defaultIgnoredDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".venv":        true,
	"__pycache__":  true,
}

var searchGlobs = []string{
	"!.git/**",
	"!node_modules/**",
	"!.env*",
	"!**/*.pem",
	"!**/*.key",
	"!**/id_rsa*",
	"!**/id_ed25519*",
	"!**/id_ecdsa*",
	"!**/.npmrc",
	"!**/.pypirc",
	"!**/.netrc",
	"!**/credentials",
	"!**/shadow",
}

// RunSearch searches files using ripgrep if available, falling back to pure Go.
func RunSearch(query, path, mode string, maxResults int, workDir string) (string, bool) {
	return runSearch(query, path, mode, maxResults, workDir)
}

func runSearch(query, path, mode string, maxResults int, workDir string) (string, bool) {
	mode, maxResults, errText := normalizeSearchOptions(query, mode, maxResults)
	if errText != "" {
		return errText, true
	}

	realPath, realWorkDir, errText := resolveSearchRoot(path, workDir)
	if errText != "" {
		return errText, true
	}

	if _, err := exec.LookPath("rg"); err == nil {
		out, isErr, handled := runRgSearch(query, mode, realPath, realWorkDir, maxResults)
		if handled {
			return out, isErr
		}
	}

	return runManualSearch(query, mode, realPath, realWorkDir, maxResults)
}

func normalizeSearchOptions(query, mode string, maxResults int) (string, int, string) {
	if strings.TrimSpace(query) == "" {
		return "", 0, "search query cannot be empty"
	}
	if mode == "" {
		mode = "literal"
	}
	if mode != "literal" && mode != "regex" {
		return "", 0, "search mode must be 'literal' or 'regex'"
	}
	if maxResults <= 0 {
		maxResults = 50
	}
	return mode, maxResults, ""
}

func resolveSearchRoot(path, workDir string) (string, string, string) {
	if workDir == "" {
		workDir = "."
	}
	realWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return "", "", fmt.Sprintf("invalid workDir: %v", err)
	}

	if path == "" {
		path = realWorkDir
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(realWorkDir, path)
	}

	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", fmt.Sprintf("invalid path: %v", err)
	}

	rel, err := filepath.Rel(realWorkDir, realPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "", "Refused: search path is outside the working directory."
	}
	return realPath, realWorkDir, ""
}

func runRgSearch(query, mode, realPath, workDir string, maxResults int) (string, bool, bool) {
	args := []string{
		"--line-number",
		"--column",
		"--no-heading",
		"--color=never",
		"--hidden",
		"--smart-case",
		"--max-count", strconv.Itoa(maxResults),
		"--max-filesize", "10M",
	}
	for _, glob := range searchGlobs {
		args = append(args, "--glob", glob)
	}
	if mode == "literal" {
		args = append(args, "--fixed-strings")
	}

	args = append(args, "--", query, realPath)
	cmd := exec.Command("rg", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return noMatches, false, true
		}
		return "", true, false
	}

	return formatRawOutput(stdout.String(), workDir, maxResults), false, true
}

func runManualSearch(query, mode, searchRoot, workDir string, maxResults int) (string, bool) {
	var matcher func(line string) (int, bool)

	if mode == "literal" {
		// Implement smart-case matching to match ripgrep behavior
		isAllLower := strings.ToLower(query) == query
		if isAllLower {
			lowerQuery := strings.ToLower(query)
			matcher = func(line string) (int, bool) {
				idx := strings.Index(strings.ToLower(line), lowerQuery)
				if idx != -1 {
					return idx + 1, true
				}
				return 0, false
			}
		} else {
			matcher = func(line string) (int, bool) {
				idx := strings.Index(line, query)
				if idx != -1 {
					return idx + 1, true
				}
				return 0, false
			}
		}
	} else {
		re, err := regexp.Compile(query)
		if err != nil {
			return fmt.Sprintf("invalid regex pattern: %v", err), true
		}
		matcher = func(line string) (int, bool) {
			loc := re.FindStringIndex(line)
			if loc != nil {
				return loc[0] + 1, true
			}
			return 0, false
		}
	}

	var results []string
	matchCount := 0

	walkErr := filepath.WalkDir(searchRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		name := d.Name()
		if d.IsDir() {
			if defaultIgnoredDirs[name] || (strings.HasPrefix(name, ".") && name != ".") {
				return filepath.SkipDir
			}
			return nil
		}

		if isIgnoredFile(name) {
			return nil
		}

		info, err := d.Info()
		if err != nil || info.Size() > maxFileByteCap {
			return nil
		}

		file, err := os.Open(p)
		if err != nil {
			return nil
		}

		// binary header check
		header := make([]byte, 512)
		n, _ := file.Read(header)
		if n > 0 && !isText(header[:n]) {
			file.Close()
			return nil
		}
		_, _ = file.Seek(0, io.SeekStart)

		scanner := bufio.NewScanner(file)
		scanBuf := make([]byte, 64*1024)
		scanner.Buffer(scanBuf, maxScannerBuf)

		lineNum := 1
		for scanner.Scan() {
			line := scanner.Text()
			if col, found := matcher(line); found {
				relPath, _ := filepath.Rel(workDir, p)
				cleanedLine := sanitizeLine(line)
				results = append(results, fmt.Sprintf("%s:%d:%d:%s", relPath, lineNum, col, cleanedLine))
				matchCount++
				if matchCount >= maxResults {
					file.Close()
					return fs.SkipAll
				}
			}
			lineNum++
		}
		file.Close()
		return nil
	})

	if walkErr != nil && len(results) == 0 {
		return walkErr.Error(), true
	}
	if len(results) == 0 {
		return noMatches, false
	}

	return strings.Join(results, "\n"), false
}

func formatRawOutput(raw, workDir string, maxResults int) string {
	raw = strings.TrimRight(raw, "\r\n")
	if raw == "" {
		return noMatches
	}

	lines := strings.Split(raw, "\n")
	var formatted []string

	for i, line := range lines {
		if i >= maxResults {
			formatted = append(formatted, fmt.Sprintf("... [truncated, reached limit of %d results]", maxResults))
			break
		}

		parts := strings.SplitN(line, ":", 4)
		if len(parts) == 4 {
			file, lineNo, col, content := parts[0], parts[1], parts[2], parts[3]
			if rel, err := filepath.Rel(workDir, file); err == nil && !strings.HasPrefix(rel, "..") {
				file = rel
			}
			formatted = append(formatted, fmt.Sprintf("%s:%s:%s:%s", file, lineNo, col, sanitizeLine(content)))
		} else {
			formatted = append(formatted, sanitizeLine(line))
		}
	}
	return strings.Join(formatted, "\n")
}

func sanitizeLine(line string) string {
	line = strings.TrimSpace(line)
	if len(line) > maxLineLen {
		return line[:maxLineLen] + "..."
	}
	return line
}

func isIgnoredFile(name string) bool {
	if strings.HasPrefix(name, ".env") || strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".key") {
		return true
	}
	switch name {
	case "id_rsa", "id_ed25519", "id_ecdsa", ".npmrc", ".pypirc", ".netrc", "credentials", "shadow":
		return true
	}
	return false
}

func isText(data []byte) bool {
	if bytes.IndexByte(data, 0) != -1 {
		return false
	}
	return utf8.Valid(data)
}
