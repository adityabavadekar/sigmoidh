package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobPatterns(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src", "deep"), 0o755)
	files := []string{
		"main.go", "src/util.go", "src/deep/helper.go",
		"README.md", "src/index.ts",
	}
	for _, f := range files {
		os.WriteFile(filepath.Join(dir, filepath.FromSlash(f)), []byte("x"), 0o644)
	}

	run := func(pattern string) string {
		raw, _ := json.Marshal(map[string]string{"pattern": pattern, "path": dir})
		out, isErr := RunTool(context.Background(), "list_files", raw, dir)
		if isErr {
			t.Fatalf("pattern %q errored: %s", pattern, out)
		}
		return out
	}

	goOut := run("**/*.go")
	for _, want := range []string{"main.go", "util.go", "helper.go"} {
		if !strings.Contains(goOut, want) {
			t.Fatalf("**/*.go missing %q:\\n%s", want, goOut)
		}
	}
	if strings.Contains(goOut, "index.ts") || strings.Contains(goOut, "README.md") {
		t.Fatalf("**/*.go leaked non-go files:\\n%s", goOut)
	}

	tsOut := run("src/*.ts")
	if !strings.Contains(tsOut, "index.ts") || strings.Contains(tsOut, "util.go") {
		t.Fatalf("src/*.ts wrong:\\n%s", tsOut)
	}

	if out := run("*.nomatch"); !strings.Contains(out, "no files match") {
		t.Fatalf("empty match message: %q", out)
	}
}
