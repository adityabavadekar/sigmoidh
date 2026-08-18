package tools

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

func runListFiles(path string, maxResults int, workDir string) (string, bool) {
	root := resolveToolPath(path, workDir)
	if path == "" {
		root = workDir
	}
	realRoot, errText := resolveSearchRoot(root, workDir)
	if errText != "" {
		return errText, true
	}
	if maxResults <= 0 {
		maxResults = 200
	}

	var entries []string
	err := filepath.WalkDir(realRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == realRoot {
			return nil
		}
		if shouldSkipListEntry(path, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(workDir, path)
		if err != nil {
			return err
		}
		name := rel
		if d.IsDir() {
			name += "/"
		}
		entries = append(entries, name)
		if len(entries) >= maxResults {
			return errStopWalk
		}
		return nil
	})
	if err != nil && err != errStopWalk {
		return err.Error(), true
	}
	if len(entries) == 0 {
		return "(no files)", false
	}
	out := strings.Join(entries, "\n")
	if err == errStopWalk {
		out += fmt.Sprintf("\n...[truncated after %d entries]", maxResults)
	}
	return truncate(out), false
}

func shouldSkipListEntry(path string, d fs.DirEntry) bool {
	base := d.Name()
	return base == ".git" || isSensitiveFile(path)
}

type stopWalkError struct{}

func (stopWalkError) Error() string { return "stop walk" }

var errStopWalk error = stopWalkError{}
