package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tabbedFib = "package main\n\nfunc fib(n int) int {\n\tif n < 2 {\n\t\treturn n\n\t}\n\treturn fib(n-1) + fib(n-2)\n}\n"

// Small models retype tab-indented code with spaces; the edit must still
// land, re-indented with tabs, instead of failing with "not found".
func TestEditMatchesDespiteSpaceIndent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(tabbedFib), 0o644); err != nil {
		t.Fatal(err)
	}
	oldStr := "func fib(n int) int {\n    if n < 2 {\n        return n\n    }\n    return fib(n-1) + fib(n-2)\n}"
	newStr := "func fib(n int) int {\n    a, b := 0, 1\n    for i := 0; i < n; i++ {\n        a, b = b, a+b\n    }\n    return a\n}"
	out, isErr := runEdit(context.Background(), path, []editOp{{OldStr: oldStr, NewStr: newStr}}, dir)
	if isErr {
		t.Fatalf("edit failed: %s", out)
	}
	if !strings.Contains(out, "ignoring indentation") {
		t.Fatalf("fallback not reported to the model: %s", out)
	}
	got, _ := os.ReadFile(path)
	want := "package main\n\nfunc fib(n int) int {\n\ta, b := 0, 1\n\tfor i := 0; i < n; i++ {\n\t\ta, b = b, a+b\n\t}\n\treturn a\n}\n"
	if string(got) != want {
		t.Fatalf("file =\n%q\nwant\n%q", got, want)
	}
}

func TestEditIndentFallbackRefusesAmbiguous(t *testing.T) {
	content := "a\n\tx := 1\nb\n    x := 1\n"
	if _, ok := replaceIgnoringIndent(content, "  x := 1", "  x := 2"); ok {
		t.Fatal("matched an ambiguous block")
	}
}

func TestEditIndentFallbackStillNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(tabbedFib), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, isErr := runEdit(context.Background(), path, []editOp{{OldStr: "func nope() {}", NewStr: "x"}}, dir); !isErr || out != editErrNotFound {
		t.Fatalf("want not-found, got %v %q", isErr, out)
	}
}
