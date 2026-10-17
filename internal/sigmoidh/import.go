package sigmoidh

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Session ingestion: convert foreign transcripts (opencode, Claude Code) into
// sigmoidh history files. Import is a snapshot — everything after the import
// lives in OUR file under ~/.sigmoidh/history; the source is never touched.

// shared writing
// writeImportedSession serializes entries into a fresh history file named
// after the source, returning the path and its resumable session name.
func writeImportedSession(source string, cwd string, entries []historyEntry) (string, string, error) {
	dir := filepath.Join(homeDir(), ".sigmoidh", "history")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	base := time.Now().Format("20060102-150405")
	name := base + "-" + sanitizeSourceTag(source)
	path := filepath.Join(dir, name+".jsonl")
	for i := 2; fileExists(path); i++ { // collision: same-second re-import
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.jsonl", name, i))
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if e.Cwd == "" {
			e.Cwd = cwd
		}
		if err := enc.Encode(e); err != nil {
			return "", "", err
		}
	}
	return path, strings.TrimSuffix(filepath.Base(path), ".jsonl"), nil
}

func sanitizeSourceTag(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		}
		if b.Len() >= 24 {
			break
		}
	}
	if b.Len() == 0 {
		return "import"
	}
	return b.String()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// opencode (SQLite via the sqlite3 CLI; zero module deps)
type ocPart struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Tool   string `json:"tool"`
	CallID string `json:"callID"`
	State  struct {
		Status string          `json:"status"`
		Input  json.RawMessage `json:"input"`
		Output string          `json:"output"`
	} `json:"state"`
}

type ocMessage struct {
	Role     string `json:"role"`
	ParentID string `json:"parentID"`
}

func sqliteJSON(db, query string) ([]map[string]any, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, fmt.Errorf("sqlite3 CLI not found in PATH (required for opencode import)")
	}
	out, err := exec.Command("sqlite3", "-json", db, query).Output()
	if err != nil {
		return nil, fmt.Errorf("sqlite3: %w", err)
	}
	var rows []map[string]any
	if len(strings.TrimSpace(string(out))) > 0 {
		if err := json.Unmarshal(out, &rows); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func rowString(r map[string]any, key string) string {
	v, _ := r[key].(string)
	return v
}

// importOpenCode converts one opencode session (latest when idPrefix is "")
// into a sigmoidh history file.
func importOpenCode(idPrefix string) (string, string, int, error) {
	src := filepath.Join(homeDir(), ".local", "share", "opencode", "opencode.db")
	if !fileExists(src) {
		return "", "", 0, fmt.Errorf("opencode database not found at %s", src)
	}
	// work on a copy: a live opencode instance may hold locks / WAL
	tmp, err := os.MkdirTemp("", "sigmoidh-import-*")
	if err != nil {
		return "", "", 0, err
	}
	defer os.RemoveAll(tmp)
	db := filepath.Join(tmp, "opencode.db")
	for _, ext := range []string{"", "-wal", "-shm"} {
		if b, err := os.ReadFile(src + ext); err == nil {
			if werr := os.WriteFile(db+ext, b, 0o600); werr != nil {
				return "", "", 0, werr
			}
		}
	}

	sel := "SELECT id, directory, title FROM session"
	if idPrefix != "" {
		sel += fmt.Sprintf(" WHERE id LIKE '%s%%'", strings.ReplaceAll(idPrefix, "'", ""))
	}
	sel += " ORDER BY time_updated DESC LIMIT 1"
	srows, err := sqliteJSON(db, sel)
	if err != nil {
		return "", "", 0, err
	}
	if len(srows) == 0 {
		return "", "", 0, fmt.Errorf("no opencode session matching %q", idPrefix)
	}
	sess := struct{ ID, Directory, Title string }{
		ID:        rowString(srows[0], "id"),
		Directory: rowString(srows[0], "directory"),
		Title:     rowString(srows[0], "title"),
	}

	mrows, err := sqliteJSON(db, fmt.Sprintf(
		"SELECT id, data FROM message WHERE session_id='%s' ORDER BY time_created ASC",
		strings.ReplaceAll(sess.ID, "'", "")))
	if err != nil {
		return "", "", 0, err
	}

	// one joined query for every part of the session, grouped in memory:
	// spawning sqlite3 per message made imports crawl
	prows, err := sqliteJSON(db, fmt.Sprintf(
		"SELECT p.data, p.message_id FROM part p JOIN message m ON m.id = p.message_id "+
			"WHERE m.session_id='%s' ORDER BY m.time_created ASC, p.time_created ASC, p.rowid ASC",
		strings.ReplaceAll(sess.ID, "'", "")))
	if err != nil {
		return "", "", 0, err
	}
	partsByMsg := map[string][]json.RawMessage{}
	order := []string{}
	for _, pr := range prows {
		mid := rowString(pr, "message_id")
		if _, seen := partsByMsg[mid]; !seen {
			order = append(order, mid)
		}
		partsByMsg[mid] = append(partsByMsg[mid], json.RawMessage(rowString(pr, "data")))
	}

	var entries []historyEntry
	for i := range mrows {
		progress(i+1, len(mrows))
		var om ocMessage
		if err := json.Unmarshal([]byte(rowString(mrows[i], "data")), &om); err != nil {
			continue
		}
		pdata := partsByMsg[rowString(mrows[i], "id")]
		parts := make([]ocPart, 0, len(pdata))
		for _, raw := range pdata {
			var p ocPart
			if json.Unmarshal(raw, &p) == nil {
				parts = append(parts, p)
			}
		}

		var entry historyEntry
		switch om.Role {
		case "user":
			var text strings.Builder
			for _, p := range parts {
				if p.Type == "text" {
					text.WriteString(p.Text)
				}
			}
			if strings.TrimSpace(text.String()) == "" {
				continue
			}
			entry = historyEntry{Timestamp: timeNowRFC3339(), Role: "user", Content: text.String()}

		case "assistant":
			entry = historyEntry{Timestamp: timeNowRFC3339(), Role: "assistant"}
			for _, p := range parts {
				switch p.Type {
				case "text":
					entry.Content += p.Text
				case "reasoning":
					entry.Reasoning += p.Text
				case "tool":
					id := p.CallID
					if id == "" {
						id = fmt.Sprintf("oc_%s_%d", p.Tool, len(entry.ToolCalls))
					}
					entry.ToolCalls = append(entry.ToolCalls, historyToolCall{
						ID: id, Name: p.Tool, Args: p.State.Input,
					})
					entries = append(entries, entry) // flush call before its result
					entry = historyEntry{Timestamp: timeNowRFC3339(), Role: "assistant"}
					toolEntry := historyEntry{Timestamp: timeNowRFC3339(), Role: "tool"}
					toolEntry.Results = append(toolEntry.Results, historyResult{
						ToolUseID: id, Output: p.State.Output, IsError: p.State.Status == "error",
					})
					entries = append(entries, toolEntry)
				}
			}
			if entry.Content == "" && entry.Reasoning == "" && len(entry.ToolCalls) == 0 {
				continue
			}

		default:
			continue
		}
		entries = append(entries, entry)
	}

	path, name, err := writeImportedSession("opencode", sess.Directory, entries)
	if err != nil {
		return "", "", 0, err
	}
	return path, name, len(entries), nil
}

func timeNowRFC3339() string { return time.Now().Format(time.RFC3339) }

// Claude Code (JSONL)
type ccLine struct {
	Type    string `json:"type"`
	Message *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"` // string OR block array
	} `json:"message"`
	Cwd string `json:"cwd"`
}

type ccBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"` // tool_result: string OR blocks
}

// importClaudeCode converts one Claude Code session transcript.
func importClaudeCode(path string) (string, string, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", 0, err
	}
	var entries []historyEntry
	cwd := ""
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if i%200 == 0 {
			progress(i, len(lines))
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var cl ccLine
		if json.Unmarshal([]byte(line), &cl) != nil || cl.Message == nil {
			continue
		}
		if cl.Cwd != "" {
			cwd = cl.Cwd
		}
		var blocks []ccBlock
		if len(cl.Message.Content) > 0 && cl.Message.Content[0] == '[' {
			if json.Unmarshal(cl.Message.Content, &blocks) != nil {
				continue
			}
		} else {
			var text string
			if json.Unmarshal(cl.Message.Content, &text) == nil {
				blocks = append(blocks, ccBlock{Type: "text", Text: text})
			}
		}

		switch cl.Type {
		case "user":
			e := historyEntry{Timestamp: timeNowRFC3339(), Role: "user"}
			var pendingTool []historyEntry
			for _, blk := range blocks {
				switch blk.Type {
				case "text":
					e.Content += blk.Text
				case "tool_result":
					te := historyEntry{Timestamp: timeNowRFC3339(), Role: "tool"}
					te.Results = append(te.Results, historyResult{
						ToolUseID: blk.ToolUseID,
						Output:    ccResultText(blk.Content),
						IsError:   blk.IsError,
					})
					pendingTool = append(pendingTool, te)
				}
			}
			if strings.TrimSpace(e.Content) != "" {
				entries = append(entries, e)
			}
			entries = append(entries, pendingTool...)

		case "assistant":
			e := historyEntry{Timestamp: timeNowRFC3339(), Role: "assistant"}
			for _, blk := range blocks {
				switch blk.Type {
				case "text":
					e.Content += blk.Text
				case "thinking":
					e.Reasoning += blk.Thinking
				case "tool_use":
					e.ToolCalls = append(e.ToolCalls, historyToolCall{
						ID: blk.ID, Name: blk.Name, Args: blk.Input,
					})
				}
			}
			if e.Content != "" || e.Reasoning != "" || len(e.ToolCalls) > 0 {
				entries = append(entries, e)
			}
		}
	}
	progress(len(lines), len(lines))
	path, name, err := writeImportedSession("claude", cwd, entries)
	if err != nil {
		return "", "", 0, err
	}
	return path, name, len(entries), nil
}

// ccResultText flattens a tool_result content (string or block array).
func ccResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	var blocks []ccBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, blk := range blocks {
			b.WriteString(blk.Text)
		}
		return b.String()
	}
	return string(raw)
}

// listOpenCodeSessions prints recent opencode sessions for -import discovery.
func listOpenCodeSessions() error {
	db := filepath.Join(homeDir(), ".local", "share", "opencode", "opencode.db")
	if !fileExists(db) {
		return fmt.Errorf("opencode database not found at %s", db)
	}
	rows, err := sqliteJSON(db, "SELECT id, title FROM session ORDER BY time_updated DESC LIMIT 10")
	if err != nil {
		return err
	}
	for _, r := range rows {
		fmt.Printf("  %s  %s\n", rowString(r, "id"), rowString(r, "title"))
	}
	return nil
}

var _ = sort.Strings // keep sort imported for future listings

// progress renders an inline bar on stderr; call with done==total to finish.
func progress(done, total int) {
	if total <= 0 {
		return
	}
	const cells = 24
	filled := cells * done / total
	fmt.Fprintf(os.Stderr, "\r  %s%s%s%s %d/%d (%d%%)%s",
		ansiGreen, strings.Repeat("▰", filled),
		ansiDim, strings.Repeat("▱", cells-filled),
		done, total, 100*done/total, ansiReset)
	if done == total {
		fmt.Fprint(os.Stderr, "\033[K\r\n")
	}
}
