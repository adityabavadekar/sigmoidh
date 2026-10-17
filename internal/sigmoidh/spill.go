package sigmoidh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	agenttools "github.com/adityabavadekar/sigmoidh/tools"
)

// Spillover: instead of truncating large tool outputs to nothing, persist the
// FULL text under ~/.sigmoidh/spill/ and hand the model a preview plus the
// absolute path so it can read specific parts on demand. Nothing is lost.

const (
	spillPreviewHead = 1500
	spillPreviewTail = 400
)

func init() {
	agenttools.SetSpillHandler(spillOutput)
}

func spillDir() string {
	return filepath.Join(homeDir(), ".sigmoidh", "spill")
}

func spillOutput(toolName, output string) string {
	os.MkdirAll(spillDir(), 0o755)
	name := fmt.Sprintf("%s_%s.txt", time.Now().Format("20060102_150405.000"), toolName)
	path := filepath.Join(spillDir(), filepath.Base(name))
	if err := os.WriteFile(path, []byte(output), 0o644); err != nil {
		return output[:spillPreviewHead] // fall back to plain truncation
	}
	head := output
	if len(head) > spillPreviewHead {
		head = head[:spillPreviewHead]
	}
	tail := ""
	if len(output) > spillPreviewHead+spillPreviewTail {
		tail = "\n…\n" + output[len(output)-spillPreviewTail:]
	}
	return fmt.Sprintf("%s%s\n\n[output truncated — %d bytes total. Full text saved to: %s (use read with offset/limit on this path)]",
		strings.TrimRight(head, "\n"), tail, len(output), path)
}

// pruneSpillFiles removes spill files older than 24h; best-effort, called at startup.
func pruneSpillFiles() {
	dir := spillDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
