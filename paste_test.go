package main

import (
	"io"
	"strings"
	"testing"
)

func feedPaste(t *testing.T, chunks []string) (string, *pasteStore) {
	t.Helper()
	store := &pasteStore{tokens: map[string]string{}}
	r := newPasteReader(io.NopCloser(strings.NewReader(strings.Join(chunks, ""))), store)
	var out strings.Builder
	buf := make([]byte, 64)
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return out.String(), store
}

func TestPasteNormalTypingUntouched(t *testing.T) {
	got, _ := feedPaste(t, []string{"hello ", "\x1b[A", "world", "\x1b[?1;2c"})
	if got != "hello \x1b[Aworld\x1b[?1;2c" {
		t.Fatalf("passthrough broken: %q", got)
	}
}

func TestPasteGiantBlockBecomesToken(t *testing.T) {
	body := "line one\nline two\r\nline three"
	raw := "\x1b[200~" + body + "\x1b[201~rest"
	got, store := feedPaste(t, []string{raw})

	if !strings.HasPrefix(got, pasteTokenPrefix) || !strings.HasSuffix(got, "]]rest") {
		t.Fatalf("token not emitted: %q", got)
	}
	token := got[:strings.Index(got, "]]")+2]

	expanded := store.expand(token)
	want := "line one\nline two\nline three" // CRLF normalized
	if expanded != want {
		t.Fatalf("expansion wrong:\n got %q\nwant %q", expanded, want)
	}
}

func TestPasteSplitAcrossChunks(t *testing.T) {
	got, store := feedPaste(t, []string{
		"\x1b", "[2", "00~pa", "sted con", "tent\x1b", "[20", "1~",
	})
	if !strings.Contains(got, pasteTokenPrefix) {
		t.Fatalf("no token from split chunks: %q", got)
	}
	if expanded := store.expand(got); expanded != "pasted content" {
		t.Fatalf("expand wrong: %q", expanded)
	}
}

func TestPasteSwallowsEmbeddedEscapes(t *testing.T) {
	// colored terminal output pasted back in: CSI codes must not leak
	raw := "\x1b[200~\x1b[31mred\x1b[0m text\x1b[201~"
	got, store := feedPaste(t, []string{raw})
	if expanded := store.expand(got); expanded != "red text" {
		t.Fatalf("escapes leaked: %q", expanded)
	}
}

func TestPasteTruncationNote(t *testing.T) {
	huge := strings.Repeat("x", pasteMaxBytes+16)
	got, store := feedPaste(t, []string{"\x1b[200~" + huge + "\x1b[201~"})
	expanded := store.expand(got)
	if len(expanded) > pasteMaxBytes+200 {
		t.Fatalf("cap not enforced: %d bytes", len(expanded))
	}
	if !strings.Contains(expanded, "[paste truncated") {
		t.Fatalf("truncation note missing: tail=%q", expanded[len(expanded)-80:])
	}
}

func TestStrayPasteEndIgnored(t *testing.T) {
	// a stray end marker outside any paste is not ours: pass through verbatim
	got, _ := feedPaste(t, []string{"abc\x1b[201~def"})
	if got != "abc\x1b[201~def" {
		t.Fatalf("stray end marker mishandled: %q", got)
	}
}

func TestEOFMidSequenceFlushesLiteral(t *testing.T) {
	got, _ := feedPaste(t, []string{"ok\x1b[12"})
	if got != "ok\x1b[12" {
		t.Fatalf("dangling sequence lost: %q", got)
	}
}

func TestExpandUnknownTokenPassesThrough(t *testing.T) {
	store := &pasteStore{tokens: map[string]string{}}
	line := "see [[paste:deadbeef]] here"
	if out := store.expand(line); out != line {
		t.Fatalf("unknown token mangled: %q", out)
	}
}

func TestExpandMultipleTokensInOrder(t *testing.T) {
	store := &pasteStore{tokens: map[string]string{}}
	t1 := store.add("FIRST")
	t2 := store.add("SECOND")
	line := t1 + " mid " + t2
	want := "FIRST mid SECOND"
	if out := store.expand(line); out != want {
		t.Fatalf("multi expand wrong: %q", out)
	}
	if !store.wasExpanded(line, want) || store.wasExpanded(want, want) {
		t.Fatal("wasExpanded detection wrong")
	}
}

func TestExpandMultilineBlockKeepsStructure(t *testing.T) {
	store := &pasteStore{tokens: map[string]string{}}
	block := "def f():\n    return 42\n"
	tok := store.add(block)
	line := "run this: " + tok
	got := store.expand(line)
	if got != "run this: "+block {
		t.Fatalf("structure lost: %q", got)
	}
}
