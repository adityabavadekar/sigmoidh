package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/voocel/litellm"
)

const (
	refusalBashDestructive = `Refused: destructive command targets a protected path or pattern. Use a specific working-directory or /tmp path.`
	refusalReadSensitive   = `Refused: sensitive credential/secret file. Ask the user for required values instead.`
	refusalReadBinary      = `Refused: binary file. This tool only reads text-based files.`
	refusalWriteSensitive  = `Refused: sensitive credential/secret file. Direct access is not permitted.`
	refusalWriteProtected  = `Refused: protected system path. Use a working-directory or /tmp path.`
	refusalEditSensitive   = `Refused: sensitive credential/secret file. Ask the user for required values instead.`
	refusalEditBinary      = `Refused: binary file. This tool only edits text-based files.`
	refusalEditProtected   = `Refused: protected system path. Use a working-directory or /tmp path.`
	editErrNotFound        = `Edit failed: old_str was not found in the file. No changes were made.`
	editErrAmbiguous       = `Edit failed: old_str matches more than once in the file. Include more surrounding context so it matches exactly one location. No changes were made.`
	editErrEmptyOldStr     = `Edit failed: old_str is empty. Provide the exact text to replace.`
	readOnlyModeErr        = `Refused: READ-ONLY mode is enabled. This tool is not allowed.`
	emptyOutputStr         = `(no output)`
)

type bashArgs struct {
	Command string `json:"command"`
}

type readArgs struct {
	Path     string `json:"path"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
	Numbered bool   `json:"numbered"`
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type editArgs struct {
	Path   string `json:"path"`
	OldStr string `json:"old_str"`
	NewStr string `json:"new_str"`
}

type searchArgs struct {
	Query      string `json:"query"`
	Path       string `json:"path"`
	Mode       string `json:"mode"`
	MaxResults int    `json:"max_results"`
}

type listFilesArgs struct {
	Path       string `json:"path"`
	Pattern    string `json:"pattern"`
	MaxResults int    `json:"max_results"`
}

type batchReadArgs struct {
	Paths    []string `json:"paths"`
	Offset   int      `json:"offset"`
	Limit    int      `json:"limit"`
	Numbered bool     `json:"numbered"`
}

type applyPatchArgs struct {
	Patch   string `json:"patch"`
	Strip   int    `json:"strip"`
	Reverse bool   `json:"reverse"`
}

type execArgs struct {
	Command     string `json:"command"`
	YieldTimeMS int    `json:"yield_time_ms"`
}

type writeStdinArgs struct {
	SessionID   int    `json:"session_id"`
	Chars       string `json:"chars"`
	Close       bool   `json:"close"`
	YieldTimeMS int    `json:"yield_time_ms"`
}

type webArgs struct {
	URL   string `json:"url"`
	Query string `json:"query"`
}

var (
	readOnlyMode   bool
	sandboxEnabled = true // bwrap isolation for bash/exec; toggled via SetSandboxEnabled
)

// SetSandboxEnabled turns the bubblewrap jail on/off for bash and exec.
// Disabling is a deliberate trust decision: guards (destructive-command and
// path checks) still apply, but the process runs with full host access.
func SetSandboxEnabled(enabled bool) {
	sandboxEnabled = enabled
}

func SandboxEnabled() bool { return sandboxEnabled }

// externalHandlers lets the host application supply tool implementations that
// live outside this package (e.g. a sub-agent runner needing the LLM client).
// They bypass the read-only gate: the host decides their policy.
var externalHandlers = map[string]func(ctx context.Context, raw json.RawMessage, workDir string) (string, bool){}

// RegisterExternalHandler wires an out-of-package tool implementation.
func RegisterExternalHandler(name string, fn func(ctx context.Context, raw json.RawMessage, workDir string) (string, bool)) {
	externalHandlers[name] = fn
}

var spillFn func(toolName, output string) string

// SetSpillHandler registers a host callback that persists oversized outputs
// (returning a preview + file reference). Nil disables spilling.
func SetSpillHandler(fn func(toolName, output string) string) { spillFn = fn }

const SpillThresholdChars = 8000

// RunTool runs a tool by name and returns its output and whether it failed. If the output is empty, it returns a specific string indicating that there was no output.
func RunTool(ctx context.Context, name string, raw json.RawMessage, workDir string) (string, bool) {
	output, isErr := runToolDirect(ctx, name, raw, workDir)
	if output == "" {
		output = emptyOutputStr
	}
	if !isErr && len(output) > SpillThresholdChars && spillFn != nil {
		output = spillFn(name, output)
	}
	return output, isErr
}

func SetReadOnlyMode(readOnly bool) {
	readOnlyMode = readOnly
}

func GetAvailableTools() []litellm.Tool {
	return Definitions(readOnlyMode)
}

func runToolDirect(ctx context.Context, name string, raw json.RawMessage, workDir string) (string, bool) {
	if fn, ok := externalHandlers[name]; ok {
		return fn(ctx, raw, workDir)
	}

	if readOnlyMode && !IsReadOnlyTool(name) {
		return readOnlyModeErr, true
	}

	switch name {
	case "bash":
		var a bashArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runBash(ctx, a.Command, workDir)
	case "read":
		var a readArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runRead(a.Path, a.Offset, a.Limit, a.Numbered, workDir)
	case "write":
		var a writeArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runWrite(a.Path, a.Content, workDir)
	case "edit":
		var a editArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runEdit(a.Path, a.OldStr, a.NewStr, workDir)
	case "search":
		var a searchArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runSearch(a.Query, a.Path, a.Mode, a.MaxResults, workDir)
	case "list_files":
		var a listFilesArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		if a.Pattern != "" {
			return runGlob(a.Pattern, a.Path, a.MaxResults, workDir)
		}
		return runListFiles(a.Path, a.MaxResults, workDir)
	case "batch_read":
		var a batchReadArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runBatchRead(a.Paths, a.Offset, a.Limit, a.Numbered, workDir)
	case "apply_patch":
		var a applyPatchArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runApplyPatch(ctx, a.Patch, a.Strip, a.Reverse, workDir)
	case "exec":
		var a execArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return startExecSession(a.Command, a.YieldTimeMS, workDir)
	case "write_stdin":
		var a writeStdinArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return writeExecStdin(a.SessionID, a.Chars, a.Close, a.YieldTimeMS)
	case "web":
		var a webArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runWeb(ctx, a.URL, a.Query)
	default:
		return "unknown tool: " + name, true
	}
}

func resolveToolPath(path string, workDir ...string) string {
	if path == "" || filepath.IsAbs(path) || len(workDir) == 0 || workDir[0] == "" {
		return path
	}
	return filepath.Join(workDir[0], path)
}

func truncate(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + fmt.Sprintf("\n...[truncated, %d bytes total]", len(s))
}

// externalDescriptions lets the host describe handler-based tools for -list-tools.
var externalDescriptions = map[string]string{
	"subagent":       "Spawn a focused sub-agent that completes one task autonomously and returns its report.",
	"trigger":        "Schedule a desktop notification (run_complete / command_done events).",
	"memory":         "Session working memory: terse key/value scratchpad across turns.",
	"plan":           "Task checklist with statuses; keep exactly one item in_progress.",
	"ask_user":       "Ask the user a blocking clarifying question, then end turn.",
	"session_search": "Full-text search across all past session transcripts.",
}

func PrintAvailableTools() {
	fmt.Println("Available tools:")
	tools := GetAvailableTools()
	for _, tool := range tools {
		desc := tool.Description
		if d, ok := externalDescriptions[tool.Name]; ok && d != "" {
			desc = d
		}
		fmt.Printf("- %s: %s\n", tool.Name, desc)
	}
}
