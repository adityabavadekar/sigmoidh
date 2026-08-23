package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Kept deliberately short: it is the cached prefix for every request in the
// session, so every line must earn its tokens. Dynamic context (binaries,
// branch, tree) is appended AFTER this static block so the shared prefix
// stays stable across projects.
const defaultSystemPrompt = `You are Sigmoidh, a terminal coding agent. Use tools for anything needing filesystem or shell access; fewest calls possible. Verify changes once; never repeat confirmed checks. Reply direct and concise: no filler, no restating requests, no narrating obvious steps. Keep reasoning terse. Ask before destructive or irreversible actions (deletions, resets, force-push). Environment facts below are current: trust them without re-checking. Listed binaries are confirmed present; unlisted tools may still exist, so check once only when a task needs one.`
const readOnlyModePart = `[[READ-ONLY MODE]] No write tools available. Read, search, analyze. For requested fixes, describe the exact change without applying it. Any Write/Edit operations will be rejected. Inform user you cannot edit files when needed.`

const usefulBinariesPrompt = `Confirmed available binaries (others may also exist):`

type dirInfo struct {
	path       string
	entryCount int
}

func getProjectTree(workDir string, level int, maxPerDir int, maxExpandedDirs int) (string, error) {
	entryCounts := make(map[string]int)
	err := filepath.WalkDir(workDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == workDir || strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() && path != workDir {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(workDir, path)
		depth := strings.Count(rel, string(os.PathSeparator))
		if depth > level {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entryCounts[filepath.Dir(path)]++
		return nil
	})
	if err != nil {
		return "", err
	}

	type dc struct {
		path  string
		count int
	}
	var dirs []dc
	for p, c := range entryCounts {
		dirs = append(dirs, dc{p, c})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].count > dirs[j].count })

	expand := make(map[string]bool)
	for i, d := range dirs {
		if i >= maxExpandedDirs {
			break
		}
		expand[d.path] = true
	}

	var b strings.Builder
	counts := make(map[string]int)
	truncated := make(map[string]bool)

	err = filepath.WalkDir(workDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == workDir {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(workDir, path)
		if err != nil {
			return err
		}
		depth := strings.Count(rel, string(os.PathSeparator))
		if depth > level {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		parent := filepath.Dir(path)

		if depth > 0 && !expand[parent] {
			if !truncated[parent] {
				b.WriteString(strings.Repeat("  ", depth))
				b.WriteString(fmt.Sprintf("(%d items)\n", entryCounts[parent]))
				truncated[parent] = true
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if maxPerDir > 0 && counts[parent] >= maxPerDir {
			if !truncated[parent] {
				b.WriteString(strings.Repeat("  ", depth))
				b.WriteString("...\n")
				truncated[parent] = true
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		counts[parent]++

		b.WriteString(strings.Repeat("  ", depth))
		b.WriteString(d.Name())
		if d.IsDir() {
			b.WriteByte('/')
		}
		b.WriteByte('\n')
		return nil
	})
	return b.String(), err
}

func getAvailableBinaries() []string {
	binaries := []string{
		"rg",
		"jq",
		"curl",
		"wget",
		"git",
		"awk",
		"sed",
		"grep",
		"find",
	}
	available := make([]string, 0, len(binaries))
	for _, name := range binaries {
		if _, err := exec.LookPath(name); err == nil {
			available = append(available, name)
		}
	}
	return available
}

func getGitBranch(workDir string) string {
	cmd := exec.Command("git", "-C", workDir, "branch", "--show-current")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func generateSystemPrompt(basePrompt string, readOnlyMode bool, workDir string) (string, error) {
	projectTree, err := getProjectTree(workDir, 1, 20, 30)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(basePrompt)

	b.WriteString("\n\nMemory (plain markdown, edit with your file tools): project notes in ")
	b.WriteString(projectMemoryPath(workDir))
	b.WriteString(", personal/global notes in ")
	b.WriteString(memoryFilePath())
	b.WriteString(".")

	if readOnlyMode {
		b.WriteString("\n\n")
		b.WriteString(readOnlyModePart)
	}

	if binaries := getAvailableBinaries(); len(binaries) > 0 {
		b.WriteString("\n\n")
		b.WriteString(usefulBinariesPrompt)
		b.WriteString(" ")
		b.WriteString(strings.Join(binaries, ", "))
	}

	if branch := getGitBranch(workDir); branch != "" {
		b.WriteString("\nCurrent git branch: ")
		b.WriteString(branch)
	}

	b.WriteString("\n\nWorking directory: ")
	b.WriteString(workDir)
	b.WriteString("\nProject tree:\n")
	b.WriteString(projectTree)

	return b.String(), nil
}
