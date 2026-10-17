package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// --- result snippets ---

func TestChangedSnippetMarksInsertedLines(t *testing.T) {
	before := "a\nb\nc\nd\ne\nf\ng\nh\n"
	after := "a\nb\nc\nd\nNEW\ne\nf\ng\nh\n"
	got := changedSnippet("x.txt", before, after)
	for _, want := range []string{"x.txt after change:", "> 5 | NEW", "  4 | d", "  6 | e"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "1 | a") {
		t.Fatalf("context should be limited to %d lines:\n%s", snippetContext, got)
	}
}

func TestChangedSnippetDeletionOnly(t *testing.T) {
	got := changedSnippet("x.txt", "a\nb\nc\n", "a\nc\n")
	if !strings.Contains(got, "lines were removed") {
		t.Fatalf("deletion not reported:\n%s", got)
	}
	if changedSnippet("x", "same\n", "same\n") != "" {
		t.Fatal("identical content should give no snippet")
	}
}

// --- edit: snippet in result, multi-edit atomicity ---

func TestEditResultShowsChangeAndMultiEditIsAtomic(t *testing.T) {
	SetPostEditChecks(false)
	defer SetPostEditChecks(true)
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	writeFile(t, path, "alpha\nbeta\ngamma\n")

	out, isErr := runEdit(context.Background(), path, []editOp{{"beta", "BETA"}, {"gamma", "GAMMA"}}, dir)
	if isErr {
		t.Fatalf("multi-edit failed: %s", out)
	}
	if got := readFile(t, path); got != "alpha\nBETA\nGAMMA\n" {
		t.Fatalf("file = %q", got)
	}
	if !strings.Contains(out, "> 2 | BETA") || !strings.Contains(out, "> 3 | GAMMA") {
		t.Fatalf("result should show the changed lines:\n%s", out)
	}

	// second edit fails → nothing written
	out, isErr = runEdit(context.Background(), path, []editOp{{"alpha", "ALPHA"}, {"missing", "x"}}, dir)
	if !isErr || !strings.Contains(out, "edit 2 of 2") || !strings.Contains(out, "No edits were applied") {
		t.Fatalf("want edit 2 failure, got %v %q", isErr, out)
	}
	if got := readFile(t, path); got != "alpha\nBETA\nGAMMA\n" {
		t.Fatalf("failed multi-edit must not write: %q", got)
	}
}

// --- checkpoints / undo ---

func TestUndoRestoresEditsAndRemovesCreatedFiles(t *testing.T) {
	SetPostEditChecks(false)
	defer SetPostEditChecks(true)
	dir := t.TempDir()
	existing := filepath.Join(dir, "keep.txt")
	created := filepath.Join(dir, "new.txt")
	writeFile(t, existing, "original\n")

	checkpoints.mu.Lock()
	checkpoints.stack = nil // independent of other tests' edits
	checkpoints.mu.Unlock()
	BeginCheckpoint("test run")
	if out, isErr := runEdit(context.Background(), existing, []editOp{{"original", "changed"}}, dir); isErr {
		t.Fatal(out)
	}
	if out, isErr := runEdit(context.Background(), existing, []editOp{{"changed", "changed twice"}}, dir); isErr {
		t.Fatal(out)
	}
	if out, isErr := runWrite(context.Background(), created, "hello", dir); isErr {
		t.Fatal(out)
	}

	res, err := UndoLast()
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, existing); got != "original\n" {
		t.Fatalf("not restored to the state before the run: %q", got)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatal("file created by the run should be removed")
	}
	if res.Label != "test run" || len(res.Restored) != 1 || len(res.Removed) != 1 {
		t.Fatalf("result %+v", res)
	}
	if _, err := UndoLast(); err != ErrNothingToUndo {
		t.Fatalf("second undo: want ErrNothingToUndo, got %v", err)
	}
}

// --- post-edit checks ---

func TestPostEditChecksBuiltinAndConfig(t *testing.T) {
	old := globalConfigPath
	globalConfigPath = func() string { return "" }
	defer func() { globalConfigPath = old }()

	dir := t.TempDir()
	bad := filepath.Join(dir, "data.json")
	writeFile(t, bad, `{"a": 1,}`)
	if got := postEditChecks(context.Background(), bad, dir); !strings.Contains(got, "[json]") || !strings.Contains(got, "invalid JSON") {
		t.Fatalf("built-in json check missing: %q", got)
	}
	good := filepath.Join(dir, "ok.json")
	writeFile(t, good, `{"a": 1}`)
	if got := postEditChecks(context.Background(), good, dir); got != "" {
		t.Fatalf("valid json reported: %q", got)
	}

	// a project rule replaces built-ins; an empty command disables checks
	writeFile(t, filepath.Join(dir, ".sigmoidh", "config.json"), `{"checks":[
		{"glob":"*.txt","command":"grep -q TODO {file} && echo 'TODO left in file' && exit 1 || exit 0"},
		{"glob":"*.json","command":""}]}`)
	txt := filepath.Join(dir, "notes.txt")
	writeFile(t, txt, "TODO: finish\n")
	if got := postEditChecks(context.Background(), txt, dir); !strings.Contains(got, "TODO left in file") {
		t.Fatalf("config rule not run: %q", got)
	}
	if got := postEditChecks(context.Background(), bad, dir); got != "" {
		t.Fatalf("empty rule command should disable checks: %q", got)
	}
}

func TestGlobMatch(t *testing.T) {
	for _, c := range []struct {
		pat, rel string
		want     bool
	}{
		{"*.go", "a/b/main.go", true},
		{"*.go", "main.ts", false},
		{"src/**/*.ts", "src/a/b/x.ts", true},
		{"src/**/*.ts", "src/x.ts", true},
		{"src/*.ts", "src/a/x.ts", false},
		{"**/test_*.py", "pkg/tests/test_a.py", true},
	} {
		if got := globMatch(c.pat, c.rel); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v", c.pat, c.rel, got)
		}
	}
}

func TestMarkersPresentWithContent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "pkg", "a.py")
	writeFile(t, file, "x = 1\n")
	writeFile(t, filepath.Join(dir, "pyproject.toml"), "[project]\nname='x'\n")
	if markersPresent([]string{"pyproject.toml:[tool.ruff"}, file, dir) {
		t.Fatal("content marker matched without the content")
	}
	writeFile(t, filepath.Join(dir, "pyproject.toml"), "[tool.ruff]\nline-length=100\n")
	if !markersPresent([]string{"pyproject.toml:[tool.ruff"}, file, dir) {
		t.Fatal("content marker in an ancestor dir not found")
	}
}

// --- search context ---

func TestManualSearchContext(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "one\ntwo\nNEEDLE\nfour\nfive\n")
	out, isErr := runManualSearch("NEEDLE", "literal", dir, dir, 10, 1)
	if isErr {
		t.Fatal(out)
	}
	want := "a.txt-2-two\na.txt:3:1:NEEDLE\na.txt-4-four"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

// --- code_nav ---

func TestCodeNavAcrossLanguages(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc parseConfig(p string) error {\n\treturn nil\n}\n\nfunc main() {\n\t_ = parseConfig(\"x\")\n}\n")
	writeFile(t, filepath.Join(dir, "tool.py"), "class Loader:\n    def load(self):\n        return parse_config()\n\ndef parse_config():\n    pass\n")
	writeFile(t, filepath.Join(dir, "web.ts"), "export function render(x: number): string {\n  return String(x)\n}\nexport const add = (a: number, b: number) => a + b\n")

	out, isErr := runCodeNav(context.Background(), "outline", "main.go", "", dir)
	if isErr || !strings.Contains(out, "3: func parseConfig(p string) error {") || !strings.Contains(out, "main.go (9 lines)") {
		t.Fatalf("go outline:\n%s", out)
	}
	out, _ = runCodeNav(context.Background(), "outline", "tool.py", "", dir)
	for _, want := range []string{"1: class Loader:", "2: def load(self):", "5: def parse_config():"} {
		if !strings.Contains(out, want) {
			t.Fatalf("python outline missing %q:\n%s", want, out)
		}
	}
	out, _ = runCodeNav(context.Background(), "outline", "web.ts", "", dir)
	if !strings.Contains(out, "export function render") || !strings.Contains(out, "export const add") {
		t.Fatalf("ts outline:\n%s", out)
	}

	out, _ = runCodeNav(context.Background(), "definition", "", "parse_config", dir)
	if !strings.Contains(out, "tool.py") || !strings.Contains(out, "5: def parse_config():") {
		t.Fatalf("definition:\n%s", out)
	}
	out, _ = runCodeNav(context.Background(), "references", "", "parseConfig", dir)
	if !strings.Contains(out, "8:") || strings.Contains(out, "3: func parseConfig") {
		t.Fatalf("references should list the call and exclude the definition:\n%s", out)
	}
	if _, isErr := runCodeNav(context.Background(), "bogus", "", "", dir); !isErr {
		t.Fatal("bad op must error")
	}
}

// --- run_tests ---

func TestRunTestsNeedsACommand(t *testing.T) {
	old := globalConfigPath
	globalConfigPath = func() string { return "" }
	defer func() { globalConfigPath = old }()
	out, isErr := runTests(context.Background(), "", t.TempDir())
	if !isErr || !strings.Contains(out, "command is required") {
		t.Fatalf("want a request for the command, got %v %q", isErr, out)
	}
}

func TestSummarizeTestOutput(t *testing.T) {
	goOut := `=== RUN   TestAdd
--- FAIL: TestAdd (0.00s)
    math_test.go:12: add(2,2) = 5, want 4
FAIL
FAIL	example.com/m	0.004s
ok  	example.com/other	0.002s
`
	got := summarizeTestOutput(goOut)
	for _, want := range []string{"failing (1):", "- TestAdd", "TestAdd: math_test.go:12: add(2,2) = 5, want 4", "FAIL\texample.com/m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("go summary missing %q:\n%s", want, got)
		}
	}
	py := "FAILED tests/test_a.py::test_x - AssertionError: 1 != 2\n==== 1 failed, 3 passed in 0.10s ====\n"
	got = summarizeTestOutput(py)
	if !strings.Contains(got, "tests/test_a.py::test_x AssertionError: 1 != 2") || !strings.Contains(got, "1 failed, 3 passed") {
		t.Fatalf("pytest summary:\n%s", got)
	}
	build := "# demo\n./main.go:13:1: syntax error: non-declaration statement outside function body\n"
	if got := summarizeTestOutput(build); !strings.Contains(got, "./main.go:13:1: syntax error") {
		t.Fatalf("build error not extracted:\n%s", got)
	}
}

// --- tool name help ---

func TestUnknownToolSuggestsAlternative(t *testing.T) {
	out, isErr := runToolDirect(context.Background(), "find", []byte(`{}`), t.TempDir())
	if !isErr || !strings.Contains(out, "Use list_files instead") || !strings.Contains(out, "Available tools:") {
		t.Fatalf("got %q", out)
	}
}

// --- read: whole-line truncation with a usable offset ---

func TestReadTruncatesAtLineWithOffset(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString("line of moderately long text for the read budget\n")
	}
	path := filepath.Join(dir, "big.txt")
	writeFile(t, path, b.String())
	out, isErr := runRead(path, 0, 0, false)
	if isErr {
		t.Fatal(out)
	}
	if !strings.Contains(out, "of 2000 shown. Continue with offset=") {
		t.Fatalf("no continuation hint: %q", out[len(out)-200:])
	}
	if len(out) > maxOutput {
		t.Fatalf("output %d bytes exceeds budget", len(out))
	}
}

// Reading a directory answers with its listing instead of an error.
func TestReadDirectoryListsEntries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pkg", "a.go"), "package pkg\n")
	out, isErr := runRead("pkg", 0, 0, false, dir)
	if isErr || !strings.Contains(out, "is a directory") || !strings.Contains(out, "a.go") {
		t.Fatalf("got %v %q", isErr, out)
	}
}
