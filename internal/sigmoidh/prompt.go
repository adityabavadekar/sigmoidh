package sigmoidh

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	agenttools "github.com/adityabavadekar/sigmoidh/tools"
)

// Kept deliberately short: it is the cached prefix for every request in the
// session, so every line must earn its tokens. Dynamic context (binaries,
// branch, tree) is appended AFTER this static block so the shared prefix
// stays stable across projects.
const defaultSystemPrompt = `You are Sigmoidh, a terminal coding agent. Use tools for anything needing filesystem or shell access; fewest calls possible, parallel when independent. Edit results show the change and lint problems; don't re-read to verify or repeat confirmed checks. Reply direct and concise: no filler, restating or narrating obvious steps. Keep reasoning terse. Ask before destructive or irreversible actions (deletions, resets, force-push). Environment facts below are current: trust them without re-checking. Listed binaries are present; unlisted ones may exist, so check once only when needed.`
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

	b.WriteString("\n\nThis session's transcript is saved as JSONL under ")
	b.WriteString(filepath.Join(homeDir(), ".sigmoidh", "history"))
	b.WriteString("; once the conversation starts the current file can be re-read with the read tool for read-only context.")

	if cmd := agenttools.ConfiguredTestCommand(workDir); cmd != "" {
		b.WriteString("\n\nTest command (pinned by the user): ")
		b.WriteString(cmd)
	}

	if instr := projectInstructions(workDir); instr != "" {
		b.WriteString("\n\n")
		b.WriteString(instr)
	}

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

	if cat := skillCatalogText(workDir); cat != "" {
		b.WriteString("\n\nRecipes for common artifact tasks (slides, video, docs, spreadsheets, diagrams, images, audio). Before starting one of these, call the skill tool with op=load and the matching name; it names the concrete tooling to use:\n")
		b.WriteString(cat)
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

// instructionFiles are the project-instruction conventions honored, in
// preference order per directory (the first one present is used).
var instructionFiles = []string{"AGENTS.md", "CLAUDE.md", ".github/copilot-instructions.md"}

const (
	maxInstructionFile  = 8 << 10
	maxInstructionTotal = 16 << 10
)

// projectInstructions collects AGENTS.md-style files from the repository
// root down to workDir (outermost first, so nearer files read last and win
// on conflicts). These are how a repo tells any agent its conventions — test
// command, style, do-nots — and they belong in the system prompt.
func projectInstructions(workDir string) string {
	var dirs []string
	for dir := workDir; ; dir = filepath.Dir(dir) {
		dirs = append([]string{dir}, dirs...)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break // repository root
		}
		home, _ := os.UserHomeDir()
		if dir == home || dir == filepath.Dir(dir) {
			dirs = dirs[len(dirs)-1:] // not in a repo: only workDir itself
			break
		}
	}
	var b strings.Builder
	total := 0
	for _, dir := range dirs {
		for _, name := range instructionFiles {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			text := strings.TrimSpace(string(data))
			if len(text) > maxInstructionFile {
				text = text[:maxInstructionFile] + "\n…(truncated)"
			}
			if text == "" || total+len(text) > maxInstructionTotal {
				break
			}
			rel, rerr := filepath.Rel(workDir, filepath.Join(dir, name))
			if rerr != nil {
				rel = filepath.Join(dir, name)
			}
			if b.Len() == 0 {
				b.WriteString("Project instructions (from the repository; follow them):")
			}
			fmt.Fprintf(&b, "\n--- %s ---\n%s", rel, text)
			total += len(text)
			break // one instruction file per directory
		}
	}
	return b.String()
}
