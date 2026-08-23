package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAllowedPathExemption(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir in environment")
	}
	mem := filepath.Join(home, ".sigmoidh", "MEMORY.md")

	SetAllowedPaths(mem)
	defer SetAllowedPaths()

	if isProtectedPath(mem) {
		t.Fatal("exact memory file must be exempt from protection")
	}
	sibling := filepath.Join(home, ".sigmoidh", "OTHER.md")
	if !isProtectedPath(sibling) {
		t.Fatal("sibling files under ~/.sigmoidh must stay protected")
	}
	if !isProtectedPath(home) {
		t.Fatal("home itself must stay protected")
	}
}
