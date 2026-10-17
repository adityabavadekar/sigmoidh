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

// writeSessionHandoff creates a model-free continuation note from the saved
// transcript, so preparing a handoff does not consume another model request.
func writeSessionHandoff(s *Sigmoidh, destination string) (string, error) {
	if s == nil || s.currentHistoryPath() == "" {
		return "", fmt.Errorf("this session has no saved transcript yet")
	}
	if destination == "" {
		name := s.CurrentSessionName()
		if name == "" {
			name = "session"
		}
		destination = "handoff-" + name + ".md"
	}
	if !filepath.IsAbs(destination) {
		destination = filepath.Join(s.workDir, destination)
	}

	type toolActivity struct {
		name string
		args json.RawMessage
	}
	var requests, responses []string
	var activities []toolActivity
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
			if content := strings.TrimSpace(entry.Content); content != "" {
				requests = append(requests, content)
			}
		case "assistant":
			if content := strings.TrimSpace(entry.Content); content != "" {
				responses = append(responses, content)
			}
			for _, call := range entry.ToolCalls {
				activities = append(activities, toolActivity{name: call.Name, args: call.Args})
				for _, path := range changedPaths(call.Name, call.Args) {
					files[path] = true
				}
			}
		}
	}
	closeErr := input.Close()
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read session transcript: %w", err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close session transcript: %w", closeErr)
	}
	if len(requests) == 0 {
		return "", fmt.Errorf("session transcript contains no user requests")
	}

	title := s.CurrentSessionTitle()
	if title == "" {
		title = sessionTitle(requests[0])
	}
	var report strings.Builder
	fmt.Fprintf(&report, "# Agent handoff: %s\n\n", title)
	fmt.Fprintf(&report, "- Session: %s\n- Working directory: %s\n- Transcript: %s\n\n", s.CurrentSessionName(), s.workDir, s.currentHistoryPath())
	req := substantiveRequest(requests[0])
	if req == "" {
		req = requests[0]
	}
	fmt.Fprintf(&report, "## Original goal\n\n%s\n\n", handoffExcerpt(req, 1800))

	start := max(0, len(requests)-5)
	fmt.Fprintln(&report, "## Recent user requests and steering")
	for _, request := range requests[start:] {
		fmt.Fprintf(&report, "- %s\n", handoffExcerpt(strings.Join(strings.Fields(request), " "), 700))
	}
	fmt.Fprintln(&report)

	if len(responses) > 0 {
		start = max(0, len(responses)-3)
		fmt.Fprintln(&report, "## Latest assistant updates")
		for _, response := range responses[start:] {
			fmt.Fprintf(&report, "- %s\n", handoffExcerpt(strings.Join(strings.Fields(response), " "), 1000))
		}
		fmt.Fprintln(&report)
	}

	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	fmt.Fprintln(&report, "## Files changed through agent tools")
	if len(paths) == 0 {
		fmt.Fprintln(&report, "- No file changes recorded in tool history.")
	} else {
		for _, path := range paths {
			fmt.Fprintf(&report, "- `%s`\n", path)
		}
	}
	fmt.Fprintln(&report)

	if len(activities) > 0 {
		start = max(0, len(activities)-12)
		fmt.Fprintln(&report, "## Recent tool activity")
		for _, activity := range activities[start:] {
			line := activity.name
			if changed := changedPaths(activity.name, activity.args); len(changed) > 0 {
				line += " — " + strings.Join(changed, ", ")
			}
			fmt.Fprintf(&report, "- `%s`\n", line)
		}
		fmt.Fprintln(&report)
	}
	fmt.Fprintln(&report, "## Continue")
	fmt.Fprintln(&report, "Inspect the current workspace and `git status` before editing. Treat the recent assistant updates as claims from the transcript, check the actual files, then continue any unfinished part of the latest user request. Do not assume approval for actions the user has not authorized.")

	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("%s already exists; choose another handoff path", destination)
		}
		return "", fmt.Errorf("create handoff file: %w", err)
	}
	if _, err := f.WriteString(report.String()); err != nil {
		f.Close()
		os.Remove(destination)
		return "", fmt.Errorf("write handoff file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close handoff file: %w", err)
	}
	return fmt.Sprintf("%s\n\nHandoff saved to %s", report.String(), destination), nil
}

func handoffExcerpt(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
