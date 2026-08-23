package main

import (
	"os"
	"path/filepath"
)

// memoryFilePath returns the absolute path of the agent's persistent markdown
// memory note. It lives outside any workdir, so it is registered as an allowed
// path with the tools guards at startup.
func memoryFilePath() string {
	return filepath.Join(homeDir(), ".sigmoidh", "MEMORY.md")
}

// projectMemoryPath returns the per-project persistent note, living inside
// the workspace so file tools need no special permissions for it.
func projectMemoryPath(workDir string) string {
	return filepath.Join(workDir, ".sigmoidh", "MEMORY.md")
}

func ensureMemoryFileDir() {
	os.MkdirAll(filepath.Dir(memoryFilePath()), 0o755)
	// Seed so first read never fails and the file is self-describing.
	if _, err := os.Stat(memoryFilePath()); os.IsNotExist(err) {
		os.WriteFile(memoryFilePath(), []byte("# Permanent memory\n\n"), 0o644)
	}
}
