package tools

import (
	"os/exec"
	"path/filepath"
	"strings"
)

const noMatches = "No matches found."

var searchGlobs = []string{
	"!.git/**",
	"!.env",
	"!.env.*",
	"!**/*.pem",
	"!**/*.key",
	"!**/id_rsa",
	"!**/id_ed25519",
	"!**/id_ecdsa",
	"!**/.npmrc",
	"!**/.pypirc",
	"!**/.netrc",
	"!**/credentials",
	"!**/shadow",
}

func runSearch(query, path, mode string, maxResults int, workDir string) (string, bool) {
	mode, maxResults, errText := normalizeSearchOptions(query, mode, maxResults)
	if errText != "" {
		return errText, true
	}

	realPath, errText := resolveSearchRoot(path, workDir)
	if errText != "" {
		return errText, true
	}

	out, err := exec.Command("rg", searchArgsFor(query, mode, realPath)...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return noMatches, false
		}
		return err.Error(), true
	}

	return formatSearchOutput(string(out), maxResults), false
}

func normalizeSearchOptions(query, mode string, maxResults int) (string, int, string) {
	if query == "" {
		return "", 0, "search query cannot be empty"
	}
	if mode == "" {
		mode = "literal"
	}
	if mode != "literal" && mode != "regex" {
		return "", 0, "search mode must be literal or regex"
	}
	if maxResults <= 0 {
		maxResults = 50
	}
	return mode, maxResults, ""
}

func resolveSearchRoot(path, workDir string) (string, string) {
	if path == "" {
		path = workDir
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}

	realWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return "", err.Error()
	}

	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err.Error()
	}

	if realPath != realWorkDir && !strings.HasPrefix(realPath, realWorkDir+string(filepath.Separator)) {
		return "", "Refused: search path is outside the working directory."
	}
	return realPath, ""
}

func searchArgsFor(query, mode, path string) []string {
	args := []string{
		"--line-number",
		"--column",
		"--no-heading",
		"--color=never",
		"--hidden",
	}
	for _, glob := range searchGlobs {
		args = append(args, "--glob", glob)
	}
	if mode == "literal" {
		args = append(args, "--fixed-strings")
	}
	return append(args, query, path)
}

func formatSearchOutput(out string, maxResults int) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) > maxResults {
		lines = lines[:maxResults]
	}
	if len(lines) == 0 || lines[0] == "" {
		return noMatches
	}
	return truncate(strings.Join(lines, "\n"))
}
