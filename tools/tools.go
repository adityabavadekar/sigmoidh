package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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

// RunTool runs a tool by name and returns its output and whether it failed. If the output is empty, it returns a specific string indicating that there was no output.
func RunTool(ctx context.Context, name string, raw json.RawMessage, workDir string) (string, bool) {
	output, isErr := runToolDirect(ctx, name, raw, workDir)
	if output == "" {
		output = emptyOutputStr
	}
	return output, isErr
}

func runToolDirect(ctx context.Context, name string, raw json.RawMessage, workDir string) (string, bool) {
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
