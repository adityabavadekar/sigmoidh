package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/litellm"
)

const (
	bashTimeout = 2 * time.Minute
	maxOutput   = 8000
)

var tools = []litellm.Tool{
	mustTool("bash", "Run a shell command and return its combined output. Refuses destructive commands (rm/dd/mkfs/etc) targeting system paths or the home directory.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "shell command to execute"},
		},
		"required": []string{"command"},
	}),
	mustTool("read", "Read a text file (text, JSON, YAML, CSV, code). Refuses binary files and credential/secret files (.env, keys). Optionally reads a specific line range.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string"},
			"offset": map[string]any{"type": "integer", "description": "1-based first line to read"},
			"limit":  map[string]any{"type": "integer", "description": "max number of lines to read"},
		},
		"required": []string{"path"},
	}),
	mustTool("write", "Write content to a file, creating or overwriting it.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		},
		"required": []string{"path", "content"},
	}),
}

const (
	refusalBashDestructive = `Refused: destructive command targets a protected path or pattern. Use a specific working-directory or /tmp path.`

	refusalReadSensitive = `Refused: sensitive credential/secret file. Ask the user for required values instead.`

	refusalReadBinary = `Refused: binary file. This tool only reads text-based files.`

	refusalWriteSensitive = `Refused: sensitive credential/secret file. Direct access is not permitted.`

	refusalWriteProtected = `Refused: protected system path. Use a working-directory or /tmp path.`
)

func mustTool(name, desc string, schema map[string]any) litellm.Tool {
	t, err := litellm.NewTool(name, desc, schema)
	if err != nil {
		panic(err)
	}
	return t
}

type bashArgs struct {
	Command string `json:"command"`
}

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// runTool dispatches a call by name and reports whether it failed.
func runTool(ctx context.Context, name string, raw json.RawMessage) (string, bool) {
	switch name {
	case "bash":
		var a bashArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runBash(ctx, a.Command)
	case "read":
		var a readArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runRead(a.Path, a.Offset, a.Limit)
	case "write":
		var a writeArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runWrite(a.Path, a.Content)
	default:
		return "unknown tool: " + name, true
	}
}

func runBash(ctx context.Context, command string) (string, bool) {
	if _, blocked := destructiveCommandOnProtectedPath(command); blocked {
		return refusalBashDestructive, true
	}

	ctx, cancel := context.WithTimeout(ctx, bashTimeout)
	defer cancel()
	workingDir, err := os.Getwd()

	cmd := exec.CommandContext(
		ctx,
		"bwrap",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind", "/lib64", "/lib64",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--bind", workingDir, workingDir,
		"--chdir", workingDir,
		"--unshare-pid",
		"--unshare-uts",
		"--unshare-ipc",
		"--die-with-parent",
		"--new-session",
		"--",
		"sh", "-c", command,
	)
	out, err := cmd.CombinedOutput()
	text := truncate(string(out))

	if ctx.Err() == context.DeadlineExceeded {
		return text + "\n[timed out]", true
	}
	if err != nil {
		return text + fmt.Sprintf("\n[%v]", err), true
	}
	return text, false
}

func runRead(path string, offset, limit int) (string, bool) {
	if isSensitiveFile(path) {
		return refusalReadSensitive, true
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error(), true
	}
	if !isTextFile(data) {
		return refusalReadBinary, true
	}

	lines := strings.Split(string(data), "\n")
	start := 0
	if offset > 1 {
		start = offset - 1
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}

	return truncate(strings.Join(lines[start:end], "\n")), false
}

func runWrite(path, content string) (string, bool) {
	if isSensitiveFile(path) {
		return refusalWriteSensitive, true
	}
	if isProtectedPath(path) {
		return refusalWriteProtected, true
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err.Error(), true
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err.Error(), true
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), false
}

func truncate(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + fmt.Sprintf("\n...[truncated, %d bytes total]", len(s))
}
