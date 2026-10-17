package sigmoidh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// session_search: cross-session recall over the local transcript corpus.
// Scores entries containing ALL query tokens, returns ranked snippets with
// the source file so the model can `read` the full context around a hit.

type sessionSearchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type searchHit struct {
	file   string
	ts     string
	role   string
	text   string
	score  int
	offset int
}

func (s *Sigmoidh) handleSessionSearch(_ context.Context, raw json.RawMessage, _ string) (string, bool) {
	var a sessionSearchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err.Error(), true
	}
	tokens := strings.Fields(strings.ToLower(strings.TrimSpace(a.Query)))
	if len(tokens) == 0 {
		return "query is required", true
	}
	limit := a.Limit
	if limit <= 0 || limit > 25 {
		limit = 8
	}

	dir := filepath.Join(s.AppDir(), "history")
	files, err := listSessions(dir)
	if err != nil {
		return "no saved sessions yet", true
	}

	var hits []searchHit
	for _, f := range files {
		for _, e := range scanEntries(f) {
			if e.Role != "user" && e.Role != "assistant" {
				continue
			}
			score, firstIdx := scoreText(e.Content, tokens)
			if score == 0 {
				continue
			}
			hits = append(hits, searchHit{
				file: filepath.Base(f), ts: e.Timestamp, role: e.Role,
				text: e.Content, score: score, offset: firstIdx,
			})
		}
	}
	if len(hits) == 0 {
		return fmt.Sprintf("no matches for %q in %d sessions", strings.Join(tokens, " "), len(files)), false
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > limit {
		hits = hits[:limit]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d matches (top %d) — use read on the shown path for full context:\n", len(hits), limit)
	for _, h := range hits {
		path := filepath.Join(dir, h.file)
		snippet := snippetAround(h.text, h.offset, 140)
		fmt.Fprintf(&b, "\n[%s · %s · %s] %s\n  %q\n  → %s\n",
			h.file, h.role, h.ts, strings.Join(tokens, "+"), snippet, path)
	}
	return strings.TrimRight(b.String(), "\n"), false
}

// scanEntries parses one transcript tolerantly.
func scanEntries(path string) []historyEntry {
	data, err := readFileLimited(path)
	if err != nil {
		return nil
	}
	var out []historyEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e historyEntry
		if json.Unmarshal([]byte(line), &e) == nil && e.Role != "" {
			out = append(out, e)
		}
	}
	return out
}

func scoreText(text string, tokens []string) (int, int) {
	lower := strings.ToLower(text)
	first := -1
	total := 0
	for _, tk := range tokens {
		c := strings.Count(lower, tk)
		if c == 0 {
			return 0, -1 // all tokens must be present in the same entry
		}
		total += c
		if idx := strings.Index(lower, tk); first < 0 || idx < first {
			if first < 0 {
				first = idx
			}
		}
	}
	if first < 0 {
		first = 0
	}
	return total, first
}

func snippetAround(text string, center, width int) string {
	start := center - width/3
	if start < 0 {
		start = 0
	}
	end := start + width
	if end > len(text) {
		end = len(text)
	}
	s := text[start:end]
	s = strings.ReplaceAll(s, "\n", " ")
	pad := ""
	if start > 0 {
		pad += "…"
	}
	if end < len(text) {
		s += "…"
	}
	return pad + s
}

func readFileLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const max = 32 << 20
	return ioReadAll(f, max)
}

func ioReadAll(f *os.File, max int64) ([]byte, error) {
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, err := f.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			if err == io.EOF {
				return buf, nil
			}
			return buf, err
		}
		if int64(len(buf)) > max {
			return buf[:max], nil
		}
	}
}
