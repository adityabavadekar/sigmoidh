package sigmoidh

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

// Bracketed paste support for the REPL. Terminals wrap pasted text in
// ESC[200~ … ESC[201~; chzyer/readline has no native handling, so a wrapper
// around stdin swallows the markers, stashes the full block, and feeds
// readline a short [[paste:id]] token instead. Tokens expand back to the
// original text (real newlines included) when the line is submitted — so a
// giant block lands as ONE input without executing mid-paste, and readline's
// per-rune redraw never sees more than ~20 characters.

const (
	pasteMaxBytes    = 4 << 20 // hard cap per paste
	pasteTokenPrefix = "[[paste:"
	pasteTokenSuffix = "]]"
)

// Terminal escape sequences toggling bracketed-paste mode.
const (
	bracketedPasteOn  = "\x1b[?2004h"
	bracketedPasteOff = "\x1b[?2004l"
)

// token store
type pasteStore struct {
	mu     sync.Mutex
	tokens map[string]string
}

var replPastes = &pasteStore{tokens: map[string]string{}}

func (s *pasteStore) add(text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id [4]byte
	_, _ = rand.Read(id[:])
	token := pasteTokenPrefix + hex.EncodeToString(id[:]) + pasteTokenSuffix
	s.tokens[token] = text
	return token
}

func (s *pasteStore) get(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text, ok := s.tokens[token]
	return text, ok
}

// expand substitutes every registered token in line with its stashed block.
// Unregistered lookalikes (user-typed) pass through untouched.
func (s *pasteStore) expand(line string) string {
	if !strings.Contains(line, pasteTokenPrefix) {
		return line
	}
	var b strings.Builder
	for i := 0; i < len(line); {
		start := strings.Index(line[i:], pasteTokenPrefix)
		if start < 0 {
			b.WriteString(line[i:])
			break
		}
		start += i
		end := strings.Index(line[start:], pasteTokenSuffix)
		if end < 0 {
			b.WriteString(line[start:])
			break
		}
		end += start + len(pasteTokenSuffix)

		b.WriteString(line[i:start]) // gap before the token
		token := line[start:end]
		if text, ok := s.get(token); ok {
			b.WriteString(text)
		} else {
			b.WriteString(token)
		}
		i = end
	}
	return b.String()
}

// wasExpanded reports whether expanding changed the line, i.e. at least one
// registered paste token was present.
func (s *pasteStore) wasExpanded(raw, expanded string) bool {
	return raw != expanded && strings.Contains(raw, pasteTokenPrefix)
}

// stdin transformer
type pasteReader struct {
	src       io.ReadCloser
	store     *pasteStore
	inPaste   bool
	esc       int    // 0 none · 1 saw ESC · 2 collecting CSI params
	csi       []byte // current CSI parameter bytes
	block     []byte // paste accumulation
	truncated bool
	out       []byte // decoded bytes awaiting consumption by readline
	eof       bool
}

func newPasteReader(src io.ReadCloser, store *pasteStore) *pasteReader {
	return &pasteReader{src: src, store: store}
}

var _ io.ReadCloser = (*pasteReader)(nil)

// Read decodes the terminal stream: paste markers are consumed, paste bodies
// become tokens, every other byte passes through verbatim.
func (r *pasteReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		if r.eof {
			return 0, io.EOF
		}
		tmp := make([]byte, 4096)
		n, err := r.src.Read(tmp)
		if n > 0 {
			r.feed(tmp[:n])
		}
		if err != nil {
			r.eof = true
			r.flushPartial()
			if len(r.out) == 0 {
				return 0, err
			}
		}
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

func (r *pasteReader) Close() error { return r.src.Close() }

func (r *pasteReader) feed(chunk []byte) {
	for _, b := range chunk {
		switch {
		case r.esc == 1: // saw ESC, deciding CSI vs plain escape
			if b == '[' {
				r.esc, r.csi = 2, r.csi[:0]
				continue
			}
			r.resolvePlainEscape(b)
		case r.esc == 2: // collecting CSI until final byte 0x40–0x7e
			r.csi = append(r.csi, b)
			if b < 0x40 || b > 0x7e {
				continue
			}
			params := string(r.csi[:len(r.csi)-1])
			r.esc = 0
			switch {
			case params == "200" && !r.inPaste:
				r.inPaste, r.block, r.truncated = true, r.block[:0], false
			case params == "201" && r.inPaste:
				r.finishPaste()
			case r.inPaste:
				// pasted escape junk: swallow entirely
			default:
				r.out = append(r.out, 0x1b, '[')
				r.out = append(r.out, r.csi...)
			}
		case r.inPaste:
			if b == 0x1b {
				r.esc = 1
				continue
			}
			if b == '\r' {
				continue // normalize CRLF/CR down to \n
			}
			if len(r.block) >= pasteMaxBytes {
				r.truncated = true
				continue
			}
			r.block = append(r.block, b)
		default:
			if b == 0x1b {
				r.esc = 1
				continue
			}
			r.out = append(r.out, b)
		}
	}
}

// resolvePlainEscape forwards a non-CSI escape pair (or drops it mid-paste).
func (r *pasteReader) resolvePlainEscape(b byte) {
	if !r.inPaste {
		r.out = append(r.out, 0x1b, b)
	}
}

// finishPaste registers the accumulated block and queues its token.
func (r *pasteReader) finishPaste() {
	r.inPaste = false
	text := string(r.block)
	r.block = r.block[:0]
	if r.truncated {
		text += "\n\n[paste truncated at " + humanNumber(pasteMaxBytes) + " bytes]"
	}
	r.out = append(r.out, r.store.add(text)...)
}

// flushPartial resolves dangling state at EOF so no bytes vanish silently.
func (r *pasteReader) flushPartial() {
	switch {
	case r.esc == 1 && !r.inPaste:
		r.out = append(r.out, 0x1b)
	case r.esc == 2 && !r.inPaste:
		r.out = append(r.out, 0x1b, '[')
		r.out = append(r.out, r.csi...)
	case r.inPaste:
		r.finishPaste()
	}
	r.esc = 0
}

// terminal toggle helpers
func enableBracketedPaste(f *os.File) bool {
	if f == nil || !term.IsTerminal(int(f.Fd())) {
		return false
	}
	_, _ = f.WriteString(bracketedPasteOn)
	return true
}

func disableBracketedPaste(f *os.File) {
	if f == nil || !term.IsTerminal(int(f.Fd())) {
		return
	}
	_, _ = f.WriteString(bracketedPasteOff)
}
