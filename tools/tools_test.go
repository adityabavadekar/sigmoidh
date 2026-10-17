package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	files := map[string]string{
		"main.go":           "package main\n\nfunc main() {\n\tprintln(\"TARGET_STRING_ALPHA\")\n}\n",
		"util/helper.go":    "package util\n\n// TARGET_STRING_ALPHA in helper\nfunc Helper() {}\n",
		"util/regex.go":     "package util\n\nconst AppVersion = \"v1.23.4-alpha\"\n",
		".env":              "SECRET_KEY=TARGET_STRING_ALPHA",
		"secrets/id_rsa":    "TARGET_STRING_ALPHA",
		"node_modules/a.js": "console.log('TARGET_STRING_ALPHA');",
		"large.bin":         string([]byte{0x00, 0x54, 0x41, 0x52, 0x47, 0x45, 0x54}),
	}

	for relPath, content := range files {
		fullPath := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("failed to create directory for %s: %v", relPath, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file %s: %v", relPath, err)
		}
	}

	return dir
}

func TestSearchLiteral(t *testing.T) {
	workDir := setupTestWorkspace(t)

	out, isErr := runSearch("TARGET_STRING_ALPHA", "", "literal", 10, workDir)
	if isErr {
		t.Fatalf("runSearch literal failed: %s", out)
	}

	if !strings.Contains(out, "main.go:4:") || !strings.Contains(out, "util/helper.go:3:") {
		t.Fatalf("expected matches in main.go and helper.go, got:\n%s", out)
	}

	for _, forbidden := range []string{".env", "id_rsa", "node_modules", "large.bin"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("forbidden file %q found in output:\n%s", forbidden, out)
		}
	}

	t.Logf("Search Literal OK:\n%s", out)
}

func TestSearchRegex(t *testing.T) {
	workDir := setupTestWorkspace(t)

	out, isErr := runSearch(`v[0-9]+\.[0-9]+\.[0-9]+-[a-z]+`, "", "regex", 10, workDir)
	if isErr {
		t.Fatalf("runSearch regex failed: %s", out)
	}

	if !strings.Contains(out, "util/regex.go:3:") || !strings.Contains(out, "v1.23.4-alpha") {
		t.Fatalf("expected semver regex match in util/regex.go, got:\n%s", out)
	}

	t.Logf("Search Regex OK:\n%s", out)
}

func TestSearchSubdirectoryAndRelativePath(t *testing.T) {
	workDir := setupTestWorkspace(t)

	out, isErr := runSearch("TARGET_STRING_ALPHA", "util", "literal", 10, workDir)
	if isErr {
		t.Fatalf("runSearch subpath failed: %s", out)
	}

	if strings.Contains(out, "main.go") {
		t.Fatalf("search scoped to 'util' unexpectedly matched 'main.go':\n%s", out)
	}
	if !strings.Contains(out, "util/helper.go") {
		t.Fatalf("expected 'util/helper.go' in scoped search, got:\n%s", out)
	}

	t.Logf("Search Subdirectory Scoping OK")
}

func TestSearchPureGoFallback(t *testing.T) {
	workDir := setupTestWorkspace(t)

	out, isErr := runManualSearch("TARGET_STRING_ALPHA", "literal", workDir, workDir, 10, 0)
	if isErr {
		t.Fatalf("runManualSearch failed: %s", out)
	}

	if !strings.Contains(out, "main.go:4:") || !strings.Contains(out, "util/helper.go:3:") {
		t.Fatalf("pure Go fallback failed to find target matches, got:\n%s", out)
	}

	if strings.Contains(out, ".env") || strings.Contains(out, "large.bin") {
		t.Fatalf("pure Go fallback leaked ignored/binary files:\n%s", out)
	}

	t.Logf("Manual Fallback OK:\n%s", out)
}

func TestSearchSafetyRestrictions(t *testing.T) {
	workDir := setupTestWorkspace(t)

	out, isErr := runSearch("root", "../", "literal", 10, workDir)
	if !isErr {
		t.Fatalf("expected security error when searching outside workDir, got: %s", out)
	}
	if !strings.Contains(out, "outside the working directory") {
		t.Fatalf("unexpected error message for directory traversal: %s", out)
	}

	out, isErr = runSearch("", "", "literal", 10, workDir)
	if !isErr || !strings.Contains(out, "cannot be empty") {
		t.Fatalf("expected empty query validation error, got: %s", out)
	}
	out, isErr = runSearch("NON_EXISTENT_STRING_XYZ_123", "", "literal", 10, workDir)
	if isErr || out != noMatches {
		t.Fatalf("expected 'No matches found.', got err=%v, out=%s", isErr, out)
	}

	t.Logf("Safety and Validation OK")
}
