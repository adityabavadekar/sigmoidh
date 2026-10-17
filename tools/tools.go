package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/voocel/litellm"
)

const (
	refusalBashDestructive   = `Refused: destructive command targets a protected path or pattern. Use a specific working-directory or /tmp path.`
	refusalReadSensitive     = `Refused: sensitive credential/secret file. Ask the user for required values instead.`
	refusalReadBinary        = `Refused: binary file. This tool only reads text-based files.`
	refusalWriteSensitive    = `Refused: sensitive credential/secret file. Direct access is not permitted.`
	refusalWriteProtected    = `Refused: protected system path. Use a working-directory or /tmp path.`
	refusalEditSensitive     = `Refused: sensitive credential/secret file. Ask the user for required values instead.`
	refusalEditBinary        = `Refused: binary file. This tool only edits text-based files.`
	refusalEditProtected     = `Refused: protected system path. Use a working-directory or /tmp path.`
	editErrNotFound          = `Edit failed: old_str was not found in the file. No changes were made.`
	editErrAmbiguous         = `Edit failed: old_str matches more than once in the file. Include more surrounding context so it matches exactly one location. No changes were made.`
	editErrEmptyOldStr       = `Edit failed: old_str is empty. Provide the exact text to replace.`
	readOnlyModeErr          = `Refused: READ-ONLY mode is enabled. This tool is not allowed.`
	emptyOutputStr           = `(no output)`
	refusalBashGitPushCommit = `Refused: git push and git commit are blocked by the active guard. Only the user can change this protection setting.`
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
	Path   string   `json:"path"`
	OldStr string   `json:"old_str"`
	NewStr string   `json:"new_str"`
	Edits  []editOp `json:"edits"`
}

type searchArgs struct {
	Query      string `json:"query"`
	Path       string `json:"path"`
	Mode       string `json:"mode"`
	MaxResults int    `json:"max_results"`
	Context    int    `json:"context"`
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

type codeNavArgs struct {
	Op     string `json:"op"`
	Path   string `json:"path"`
	Symbol string `json:"symbol"`
}

type runTestsArgs struct {
	Command string `json:"command"`
}

type webArgs struct {
	URL   string `json:"url"`
	Query string `json:"query"`
}

var (
	readOnlyMode       bool
	sandboxEnabled     = true // bwrap isolation for bash/exec; toggled via /sandbox
	gitPushCommitGuard = true
)

// SetSandboxEnabled turns the bubblewrap jail on/off for bash and exec.
// Disabling is a deliberate trust decision: guards (destructive-command and
// path checks) still apply, but the process runs with full host access.
func SetSandboxEnabled(enabled bool) {
	sandboxEnabled = enabled
}

func SandboxEnabled() bool { return sandboxEnabled }

// SetGitPushCommitGuard controls the shell guard for git push and git commit.
func SetGitPushCommitGuard(enabled bool) { gitPushCommitGuard = enabled }

func GitPushCommitGuardEnabled() bool { return gitPushCommitGuard }

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
		return runWrite(ctx, a.Path, a.Content, workDir)
	case "edit":
		var a editArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		edits := a.Edits
		if a.OldStr != "" || len(edits) == 0 {
			edits = append([]editOp{{OldStr: a.OldStr, NewStr: a.NewStr}}, edits...)
		}
		return runEdit(ctx, a.Path, edits, workDir)
	case "search":
		var a searchArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runSearchContext(a.Query, a.Path, a.Mode, a.MaxResults, a.Context, workDir)
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
	case "code_nav":
		var a codeNavArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runCodeNav(ctx, a.Op, a.Path, a.Symbol, workDir)
	case "run_tests":
		var a runTestsArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runTests(ctx, a.Command, workDir)
	case "web":
		var a webArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return err.Error(), true
		}
		return runWeb(ctx, a.URL, a.Query)
	default:
		return unknownToolMessage(name), true
	}
}

// toolAliases maps names models commonly reach for (from other harnesses or
// shell habits) to the correct sigmoidh tool.
var toolAliases = map[string]string{
	"find": "list_files", "glob": "list_files", "ls": "list_files", "list": "list_files", "list_dir": "list_files",
	"grep": "search", "rg": "search", "ripgrep": "search", "search_files": "search", "find_in_files": "search",
	"cat": "read", "view": "read", "open": "read", "read_file": "read", "open_file": "read",
	"shell": "bash", "sh": "bash", "run": "bash", "terminal": "bash", "execute": "bash", "run_command": "bash",
	"str_replace": "edit", "replace": "edit", "edit_file": "edit", "str_replace_editor": "edit", "multi_edit": "edit",
	"create": "write", "create_file": "write", "write_file": "write",
	"patch": "apply_patch", "diff": "apply_patch",
	"fetch": "web", "browse": "web", "web_search": "web", "web_fetch": "web", "curl": "web",
	"test": "run_tests", "tests": "run_tests",
	"todo": "plan", "todos": "plan", "todo_write": "plan", "task": "plan",
	"symbols": "code_nav", "outline": "code_nav", "goto_definition": "code_nav", "find_references": "code_nav",
}

// unknownToolMessage tells the model what exists instead, with a likely
// substitute.
func unknownToolMessage(name string) string {
	var names []string
	for _, t := range GetAvailableTools() {
		names = append(names, t.Name)
	}
	msg := fmt.Sprintf("unknown tool %q.", name)
	if alt, ok := toolAliases[strings.ToLower(name)]; ok {
		msg += fmt.Sprintf(" Use %s instead.", alt)
	}
	return msg + " Available tools: " + strings.Join(names, ", ")
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
	"notify_user":    "Request desktop notification when this run finishes.",
	"schedule":       "Create or cancel scheduled agent tasks (timer/cron); cancel by id.",
	"status":         "Read scheduled tasks and exec session states.",
	"memory":         "Session working memory: terse key/value scratchpad across turns.",
	"plan":           "Task checklist with statuses; keep exactly one item in_progress.",
	"ask_user":       "Ask blocking question; away mode forbids interrupting the user.",
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
