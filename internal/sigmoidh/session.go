package sigmoidh

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/mattn/go-runewidth"
	"github.com/voocel/litellm"
)

const sessionTimeLayout = "2006-01-02_15-04-05"
const sessionTimeLayoutCompact = "20060102-150405" // written since v0.6

// parseSessionName accepts both the compact current layout and the legacy
// underscored one so old transcripts keep listing and resuming.
func parseSessionName(name string) (time.Time, bool) {
	// imported files may carry a source suffix: 20260823-221400-opencode
	if i := strings.LastIndex(name, "-"); i > 0 && !isAllDigits(name[i+1:]) {
		name = name[:i]
	}
	for _, layout := range []string{sessionTimeLayoutCompact, sessionTimeLayout} {
		if t, err := time.ParseInLocation(layout, name, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sessionInfo describes one saved transcript.
type sessionInfo struct {
	Path    string
	Name    string // file name without extension
	Cwd     string // working directory the session ran in
	Msgs    int    // conversation entries (system seed excluded)
	At      time.Time
	Preview string // first substantive user request (greetings skipped), collapsed and truncated
	Request string // same, untruncated (the table fits it to the terminal)
	Prompts int    // user messages
	Tools   int    // tool calls
	Files   int    // distinct files changed by write/edit/apply_patch
	LastAt  time.Time
}

// listSessions returns paths of saved transcripts, newest first.
func listSessions(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no saved sessions yet")
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		// Legacy transcripts from the old date-layout bug (e.g. "22008-…")
		// are unparseable and would sort as forever-newest; hide them.
		if _, ok := parseSessionName(strings.TrimSuffix(name, ".jsonl")); !ok {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no saved sessions yet")
	}
	// timestamped filenames sort chronologically
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	return files, nil
}

// scanSession reads a whole transcript to extract metadata: the directory it
// ran in, its length, and the first user message as a preview. Malformed
// lines are tolerated.
func scanSession(path string) (sessionInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return sessionInfo{}, err
	}
	defer f.Close()

	info := sessionInfo{
		Path: path,
		Name: strings.TrimSuffix(filepath.Base(path), ".jsonl"),
	}
	// Filenames carry local-time digits; parse them as local, not UTC.
	if t, ok := parseSessionName(info.Name); ok {
		info.At = t
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	files := map[string]bool{}
	firstUser := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e struct {
			Ts        string `json:"ts"`
			Role      string `json:"role"`
			Cwd       string `json:"cwd"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Name string          `json:"name"`
				Args json.RawMessage `json:"args"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		if info.Cwd == "" {
			info.Cwd = e.Cwd
		}
		if e.Role != "" && e.Role != "system" {
			info.Msgs++
		}
		if t, err := time.Parse(time.RFC3339, e.Ts); err == nil {
			info.LastAt = t
		}
		if e.Role == "user" {
			info.Prompts++
			if firstUser == "" {
				firstUser = e.Content
			}
			if info.Request == "" {
				info.Request = substantiveRequest(e.Content)
			}
		}
		info.Tools += len(e.ToolCalls)
		for _, c := range e.ToolCalls {
			for _, p := range changedPaths(c.Name, c.Args) {
				files[p] = true
			}
		}
	}
	if info.Request == "" { // only greetings: show what there was
		info.Request = strings.Join(strings.Fields(firstUser), " ")
	}
	info.Preview = truncatePreview(info.Request, 46)
	info.Files = len(files)
	return info, sc.Err()
}

// fmtSpan renders a session length compactly: 45s, 3m 20s, 2h 5m.
func fmtSpan(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// greetingWords are pleasantries that say nothing about a session's topic.
var greetingWords = map[string]bool{
	"hi": true, "hii": true, "hello": true, "hey": true, "yo": true, "sup": true, "hola": true,
	"thanks": true, "thank": true, "you": true, "thx": true, "ok": true, "okay": true, "cool": true,
	"good": true, "morning": true, "evening": true, "afternoon": true, "there": true, "please": true,
}

// substantiveRequest strips leading greetings ("Hi, can you…" → "can you…")
// and returns "" when nothing but greetings remains.
func substantiveRequest(s string) string {
	words := strings.Fields(s)
	i := 0
	for i < len(words) && greetingWords[strings.ToLower(strings.Trim(words[i], ",.!?:;"))] {
		i++
	}
	return strings.Join(words[i:], " ")
}

// changedPaths extracts the files a mutating tool call touched.
func changedPaths(name string, args json.RawMessage) []string {
	switch name {
	case "write", "edit":
		var a struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(args, &a) == nil && a.Path != "" {
			return []string{a.Path}
		}
	case "apply_patch":
		var a struct {
			Patch string `json:"patch"`
		}
		if json.Unmarshal(args, &a) != nil {
			return nil
		}
		var out []string
		for _, ln := range strings.Split(a.Patch, "\n") {
			if strings.HasPrefix(ln, "+++ ") {
				if f := strings.Fields(ln[4:]); len(f) > 0 && f[0] != "/dev/null" {
					out = append(out, f[0])
				}
			}
		}
		return out
	}
	return nil
}

// truncatePreview collapses whitespace/newlines and clips to max columns,
// taking wide (CJK) runes into account.
func truncatePreview(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if runewidth.StringWidth(s) <= max {
		return s
	}
	out := ""
	for _, r := range s {
		if runewidth.StringWidth(out)+runewidth.RuneWidth(r) > max-1 {
			break
		}
		out += string(r)
	}
	return out + "…"
}

// relativeTime renders human-friendly recency for a session timestamp.
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case t.Year() == time.Now().Year():
		return t.Format("Jan 2")
	default:
		return t.Format("Jan 2, 2006")
	}
}

// listSessionInfos returns session metadata newest-first, optionally filtered
// to one working directory (empty cwdFilter matches everything). Sessions
// written before cwd stamping have no directory recorded and only match the
// unfiltered view. Unreadable or empty transcripts are skipped.
func listSessionInfos(dir, cwdFilter string) ([]sessionInfo, error) {
	files, err := listSessions(dir)
	if err != nil {
		return nil, err
	}
	var infos []sessionInfo
	for _, p := range files {
		info, err := scanSession(p)
		if err != nil || info.Msgs == 0 {
			continue
		}
		if cwdFilter != "" && info.Cwd != cwdFilter {
			continue
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// sessionsHere lists sessions that were run in the agent's working directory,
// newest first.
func (s *Sigmoidh) sessionsHere() ([]sessionInfo, error) {
	return listSessionInfos(filepath.Join(s.AppDir(), "history"), s.workDir)
}

// printSessionTable renders the session listing: what each session was
// about (first real request, greetings skipped), when it was last active,
// how long it ran and how much it did.
func printSessionTable(infos []sessionInfo) {
	if len(infos) == 0 {
		fmt.Println("No sessions found.")
		return
	}

	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	// the request column takes whatever width the fixed columns leave
	reqWidth := getStdoutWidth() - 2 - 58
	reqWidth = max(24, min(reqWidth, 90))

	t := table.New()
	t.Border(lipgloss.RoundedBorder())
	t.BorderStyle(dim)
	t.StyleFunc(func(row, col int) lipgloss.Style {
		st := lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1)
		if row == table.HeaderRow {
			return dim.Bold(true).PaddingLeft(1).PaddingRight(1)
		}
		if col >= 2 && col <= 5 { // TOOK, ASKS, TOOLS, FILES
			return st.Align(lipgloss.Right)
		}
		return st
	})
	t.Headers("#", "ACTIVE", "TOOK", "ASKS", "TOOLS", "FILES", "REQUEST")

	for i, si := range infos {
		last := si.LastAt
		if last.IsZero() {
			last = si.At
		}
		when := relativeTime(last)
		if when == "" {
			when = si.Name
		}
		took := "-"
		if !si.At.IsZero() && !si.LastAt.IsZero() && si.LastAt.After(si.At) {
			took = fmtSpan(si.LastAt.Sub(si.At))
		}
		num := fmt.Sprint(i + 1)
		if i == 0 {
			num = "›1" // what -continue would pick
		}
		req := si.Request
		if req == "" {
			req = "-"
		}
		more := ""
		if si.Prompts > 1 {
			more = fmt.Sprintf(" +%d", si.Prompts-1)
		}
		req = truncatePreview(req, reqWidth-runewidth.StringWidth(more)) + dim.Render(more)
		files := "-"
		if si.Files > 0 {
			files = fmt.Sprint(si.Files)
		}
		t.Row(num, when, took, fmt.Sprint(si.Prompts), fmt.Sprint(si.Tools), files, req)
	}

	for _, line := range strings.Split(t.Render(), "\n") {
		fmt.Println("  " + line)
	}
	fmt.Println(dim.Render("  resume: sigmoidh -session <#>   ·   -continue picks ›1   ·   +N = further requests in that session"))
	fmt.Println()
}

// resolveSessionPath maps a user-supplied session id (with or without
// extension) onto a transcript inside dir, refusing anything outside it.
func resolveSessionPath(dir, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("empty session name")
	}
	if !strings.HasSuffix(name, ".jsonl") {
		name += ".jsonl"
	}
	path := filepath.Join(dir, filepath.Base(name))
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("no such session: %s", filepath.Base(name))
	}
	return path, nil
}

// resolveSessionArg resolves a -session argument: either the number shown in
// the session table (# column, scoped to cwd like every other listing) or an
// explicit session id.
func resolveSessionArg(dir, cwd, arg string) (string, error) {
	if n, err := strconv.Atoi(strings.TrimSpace(arg)); err == nil {
		infos, ierr := listSessionInfos(dir, cwd)
		if ierr != nil {
			return "", ierr
		}
		if n < 1 || n > len(infos) {
			return "", fmt.Errorf("session %d out of range (have %d) — see sigmoidh -list-sessions", n, len(infos))
		}
		return infos[n-1].Path, nil
	}
	return resolveSessionPath(dir, arg)
}

// loadSessionMessages rebuilds provider messages from a transcript file.
// The stored system entry is skipped: a fresh system prompt is always used.
//
// Transcripts written by a crashed process may contain assistant tool calls
// that never got a result entry (the crash happened mid-turn). Providers
// reject such histories ("user message follows unresolved tool use"), which
// used to brick resumption of those sessions forever. Here any unresolved
// call gets a synthetic error result inserted, so damaged sessions load.
func loadSessionMessages(path string) ([]litellm.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var msgs []litellm.Message
	pending := map[string]bool{} // tool_use ids awaiting a result entry
	repairs := 0

	flushPending := func() {
		if len(pending) == 0 {
			return
		}
		blocks := make([]litellm.Block, 0, len(pending))
		for id := range pending {
			blocks = append(blocks, litellm.ToolResultBlock{
				ToolUseID: id,
				Content:   []litellm.Block{litellm.Text("no result recorded — the agent was interrupted before this tool ran")},
				IsError:   true,
			})
			repairs++
		}
		msgs = append(msgs, litellm.Message{Role: litellm.RoleTool, Blocks: blocks})
		logWarn("repaired unresolved tool uses in transcript", "path", filepath.Base(path), "count", len(pending))
		pending = map[string]bool{}
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e historyEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // tolerate malformed lines
		}
		switch e.Role {
		case "system":
			continue
		case "user":
			flushPending()
			msgs = append(msgs, litellm.UserText(e.Content))
		case "assistant":
			var blocks []litellm.Block
			if e.Reasoning != "" {
				blocks = append(blocks, litellm.ReasoningBlock{Text: e.Reasoning})
			}
			if e.Content != "" {
				blocks = append(blocks, litellm.Text(e.Content))
			}
			for _, tc := range e.ToolCalls {
				blocks = append(blocks, litellm.ToolUseBlock{ID: tc.ID, Name: tc.Name, Arguments: tc.Args})
				pending[tc.ID] = true
			}
			if len(blocks) > 0 {
				msgs = append(msgs, litellm.Assistant(blocks...))
			}
		case "tool":
			blocks := make([]litellm.Block, 0, len(e.Results))
			for _, r := range e.Results {
				delete(pending, r.ToolUseID)
				blocks = append(blocks, litellm.ToolResultBlock{
					ToolUseID: r.ToolUseID,
					Content:   []litellm.Block{litellm.Text(r.Output)},
					IsError:   r.IsError,
				})
			}
			if len(blocks) > 0 {
				msgs = append(msgs, litellm.Message{Role: litellm.RoleTool, Blocks: blocks})
			}
		}
	}
	flushPending() // transcript ends mid-turn: resolve trailing calls too

	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("no resumable messages in %s", filepath.Base(path))
	}
	if repairs > 0 {
		logInfo("transcript repaired on load", "path", filepath.Base(path), "synthesized_results", repairs)
	}
	return msgs, nil
}

// ResumeFrom loads a previous conversation into the current session and keeps
// appending new history to the same transcript file. Returns message count.
func (s *Sigmoidh) ResumeFrom(path string) (int, error) {
	msgs, err := loadSessionMessages(path)
	if err != nil {
		return 0, err
	}
	logInfo("resuming session", "path", filepath.Base(path), "messages", len(msgs))

	s.messages = append(s.messages[:1], msgs...) // keep fresh system prompt at [0]

	s.lastTexts = s.lastTexts[:0]
	for _, m := range msgs {
		if m.Role != litellm.RoleAssistant || len(m.Blocks) == 0 {
			continue
		}
		var text strings.Builder
		hasCalls := false
		for _, b := range m.Blocks {
			switch tb := b.(type) {
			case litellm.TextBlock:
				text.WriteString(tb.Text)
			case litellm.ToolUseBlock:
				hasCalls = true
			}
		}
		if !hasCalls {
			s.lastTexts = append(s.lastTexts, text.String())
		}
	}

	if f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		if s.historyFile != nil {
			s.historyFile.Close()
		}
		s.historyFile = f
		s.histPath = path
	}
	return len(msgs), nil
}
