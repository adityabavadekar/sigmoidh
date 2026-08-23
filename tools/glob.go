package tools

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// runGlob lists FILES matching a double-star-aware glob under root,
// sorted shortest-first. Pattern semantics:
//
//	**/  matches zero or more path segments
//	*    matches anything except /
//	?    matches one char (not /)
func runGlob(pattern, path string, maxResults int, workDir string) (string, bool) {
	root := workDir
	if strings.TrimSpace(path) != "" {
		root = resolveToolPath(path, workDir)
	}
	if isProtectedCommandPath(root, workDir) {
		return refusalReadSensitive, true
	}
	if maxResults <= 0 || maxResults > 1000 {
		maxResults = 200
	}

	re, err := globRegexp(pattern)
	if err != nil {
		return fmt.Sprintf("bad pattern %q: %v", pattern, err), true
	}

	var matches []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if defaultSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		if re.MatchString(rel) {
			matches = append(matches, rel)
		}
		return nil
	})

	sort.Slice(matches, func(i, j int) bool { return len(matches[i]) < len(matches[j]) })
	truncated := false
	if len(matches) > maxResults {
		matches = matches[:maxResults]
		truncated = true
	}
	if len(matches) == 0 {
		return "(no files match pattern)", false
	}
	out := strings.Join(matches, "\n")
	if truncated {
		out += "\n…(truncated)"
	}
	return out, false
}

// globRegexp compiles a path glob into an anchored regex.
func globRegexp(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimSpace(pattern)
	var b strings.Builder
	b.WriteString("^")
	i := 0
	for i < len(pattern) {
		c := pattern[i]
		switch {
		case c == '*':
			j := i
			for j < len(pattern) && pattern[j] == '*' {
				j++
			}
			if n := j - i; n >= 2 {
				if j < len(pattern) && pattern[j] == '/' {
					b.WriteString("(?:.*/)?")
					i = j + 1
				} else {
					b.WriteString(".*")
					i = j
				}
			} else {
				b.WriteString("[^/]*")
				i = j
			}
		case c == '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
			i++
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
