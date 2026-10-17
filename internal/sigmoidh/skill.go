package sigmoidh

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A skill is a reusable procedure distilled from what this session actually
// did. writeSessionSkill reads the saved transcript and emits a SKILL.md-style
// document (frontmatter + steps). It is model-free: no extra provider request.

func writeSessionSkill(s *Sigmoidh, destination string) (string, error) {
	if s == nil || s.currentHistoryPath() == "" {
		return "", fmt.Errorf("this session has no saved transcript yet")
	}
	if destination == "" {
		name := s.CurrentSessionName()
		if name == "" {
			name = "session"
		}
		destination = "skill-" + slugify(name) + ".md"
	}
	if !filepath.IsAbs(destination) {
		destination = filepath.Join(s.workDir, destination)
	}

	var requests, responses []string
	var activities []skillActivity
	files := make(map[string]bool)

	input, err := os.Open(s.currentHistoryPath())
	if err != nil {
		return "", fmt.Errorf("open session transcript: %w", err)
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var entry historyEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		switch entry.Role {
		case "user":
			if c := strings.TrimSpace(entry.Content); c != "" {
				requests = append(requests, c)
			}
		case "assistant":
			if c := strings.TrimSpace(entry.Content); c != "" {
				responses = append(responses, c)
			}
			for _, call := range entry.ToolCalls {
				activities = append(activities, skillActivity{name: call.Name, args: call.Args})
				for _, p := range changedPaths(call.Name, call.Args) {
					files[p] = true
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		input.Close()
		return "", fmt.Errorf("read session transcript: %w", err)
	}
	input.Close()
	if len(requests) == 0 {
		return "", fmt.Errorf("session transcript contains no user requests")
	}

	title := s.CurrentSessionTitle()
	if title == "" {
		title = sessionTitle(requests[0])
	}
	goal := substantiveRequest(requests[0])
	if goal == "" {
		goal = requests[0]
	}
	description := firstLine(strings.Join(strings.Fields(goal), " "), 120)

	var b strings.Builder
	fmt.Fprintf(&b, "---\nname: %s\ndescription: %s\n---\n\n", slugify(title), description)
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "## When to use\n\n%s\n\n", handoffExcerpt(goal, 1200))

	if steps := skillSteps(activities); len(steps) > 0 {
		b.WriteString("## Procedure\n\n")
		for i, step := range steps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, step)
		}
		b.WriteString("\n")
	}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) > 0 {
		b.WriteString("## Files involved\n\n")
		for _, p := range paths {
			fmt.Fprintf(&b, "- `%s`\n", p)
		}
		b.WriteString("\n")
	}

	start := max(0, len(requests)-5)
	b.WriteString("## Example requests\n\n")
	for _, r := range requests[start:] {
		fmt.Fprintf(&b, "- %s\n", handoffExcerpt(strings.Join(strings.Fields(r), " "), 300))
	}
	b.WriteString("\n")

	if len(responses) > 0 {
		b.WriteString("## Outcome\n\n")
		fmt.Fprintf(&b, "%s\n", handoffExcerpt(strings.Join(strings.Fields(responses[len(responses)-1]), " "), 800))
	}

	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("%s already exists; choose another skill path", destination)
		}
		return "", fmt.Errorf("create skill file: %w", err)
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		os.Remove(destination)
		return "", fmt.Errorf("write skill file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close skill file: %w", err)
	}
	return fmt.Sprintf("%s\n\nSkill saved to %s", b.String(), destination), nil
}

type skillActivity struct {
	name string
	args json.RawMessage
}

// skillSteps turns recorded tool activity into a deduplicated, imperative
// procedure. Pure exploration (reads, searches) is omitted; execution and
// mutations become steps.
func skillSteps(activities []skillActivity) []string {
	var steps []string
	seen := map[string]bool{}
	add := func(step string) {
		step = strings.TrimSpace(step)
		if step == "" || seen[step] || len(steps) >= 20 {
			return
		}
		seen[step] = true
		steps = append(steps, step)
	}
	for _, a := range activities {
		switch a.name {
		case "bash", "exec":
			add("Run `" + oneLine(string(argCommand(a.args)), 160) + "`")
		case "run_tests":
			cmd := strings.TrimSpace(string(argCommand(a.args)))
			if cmd == "" {
				add("Run the project's test suite")
			} else {
				add("Run the tests: `" + oneLine(cmd, 160) + "`")
			}
		case "write":
			add("Create/replace `" + oneLine(string(argPath(a.args)), 160) + "`")
		case "edit":
			add("Edit `" + oneLine(string(argPath(a.args)), 160) + "`")
		case "apply_patch":
			add("Apply a patch")
		case "subagent":
			add("Delegate: " + oneLine(string(argPrompt(a.args)), 160))
		}
	}
	return steps
}

func argCommand(raw json.RawMessage) string {
	var a struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(raw, &a)
	return a.Command
}

func argPath(raw json.RawMessage) string {
	var a struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(raw, &a)
	return a.Path
}

func argPrompt(raw json.RawMessage) string {
	var a struct {
		Prompt string `json:"prompt"`
	}
	_ = json.Unmarshal(raw, &a)
	return a.Prompt
}

// slugify yields a filesystem- and frontmatter-safe identifier.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "skill"
	}
	if len(out) > 48 {
		out = strings.Trim(out[:48], "-")
	}
	return out
}

// oneLine collapses whitespace and truncates to limit runes.
func oneLine(s string, limit int) string {
	return handoffExcerpt(strings.Join(strings.Fields(strings.TrimSpace(s)), " "), limit)
}
