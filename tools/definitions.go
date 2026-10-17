package tools

import (
	"time"

	"github.com/voocel/litellm"
)

const (
	bashTimeout = 2 * time.Minute
	maxOutput   = 8000
)

var definitions = []litellm.Tool{
	mustTool("bash", "Run shell command in workspace, wait for output (2 min timeout). For short commands and waits (sleep 10). exec only for processes staying alive in background — it returns before they finish. Destructive commands on system/home paths refused.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "shell command to execute"},
		},
		"required": []string{"command"},
	}),
	mustTool("read", "Read a text file. Use offset/limit for large files. Secret files (.env, keys) and binaries are refused. Prefer code_nav outline to find a definition without reading the whole file.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":     map[string]any{"type": "string", "description": "file path, relative to the working directory or absolute"},
			"offset":   map[string]any{"type": "integer", "description": "first line to read, 1-based"},
			"limit":    map[string]any{"type": "integer", "description": "max number of lines"},
			"numbered": map[string]any{"type": "boolean", "description": "prefix each line with its line number (do not copy numbers into edits)"},
		},
		"required": []string{"path"},
	}),
	mustTool("write", "Create a file or replace its whole content. For changes to an existing file prefer edit. The result reports problems found by the project's linters.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string", "description": "complete new file content"},
		},
		"required": []string{"path", "content"},
	}),
	mustTool("edit", "Replace text in a file. old_str must identify exactly one place (include nearby lines if it repeats); indentation differences are tolerated. For several changes in one file pass edits instead; they apply in order, all or nothing. The result shows the changed lines with line numbers and any linter problems, so there is no need to re-read the file to verify.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"old_str": map[string]any{"type": "string", "description": "existing text to replace, copied from the file"},
			"new_str": map[string]any{"type": "string", "description": "replacement text"},
			"edits": map[string]any{
				"type":        "array",
				"description": "several replacements in one call, instead of old_str/new_str",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"old_str": map[string]any{"type": "string"},
						"new_str": map[string]any{"type": "string"},
					},
					"required": []string{"old_str", "new_str"},
				},
			},
		},
		"required": []string{"path"},
	}),
	mustTool("search", "Search file contents in the project (ripgrep). Returns matching lines as path:line:col:text; set context to also get surrounding lines (path-line-text). Case-insensitive unless the query has capitals. Secret files are skipped.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query":       map[string]any{"type": "string", "description": "text, or a regular expression when mode=regex"},
			"path":        map[string]any{"type": "string", "description": "file or directory to search; default: working directory"},
			"mode":        map[string]any{"type": "string", "enum": []string{"literal", "regex"}, "description": "default literal"},
			"context":     map[string]any{"type": "integer", "description": "lines of context around each match, 0-10; default 0"},
			"max_results": map[string]any{"type": "integer", "description": "max matching lines; default 50"},
		},
		"required": []string{"query"},
	}),
	mustTool("list_files", "List a directory, or find files by glob (pattern, ** allowed). Skips .git, node_modules, other vendored/cache directories and secret files.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":        map[string]any{"type": "string", "description": "directory; default: working directory"},
			"pattern":     map[string]any{"type": "string", "description": "glob such as **/*.go or src/*.ts; returns matching files"},
			"max_results": map[string]any{"type": "integer", "description": "default 200"},
		},
	}),
	mustTool("batch_read", "Read several text files in one call; same options and guards as read.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"paths":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"offset":   map[string]any{"type": "integer", "description": "first line to read from each file, 1-based"},
			"limit":    map[string]any{"type": "integer", "description": "max lines per file"},
			"numbered": map[string]any{"type": "boolean", "description": "prefix each line with its line number"},
		},
		"required": []string{"paths"},
	}),
	mustTool("apply_patch", "Apply a unified diff (several files and hunks in one call). The result shows the changed lines and any linter problems. For a few replacements, edit is simpler and more forgiving.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"patch":   map[string]any{"type": "string", "description": "unified diff text with ---/+++ headers and @@ hunks"},
			"strip":   map[string]any{"type": "integer", "description": "leading path components to strip, like patch -p (use 1 for a/ b/ prefixes); default 0"},
			"reverse": map[string]any{"type": "boolean", "description": "apply in reverse (undo the patch)"},
		},
		"required": []string{"patch"},
	}),
	mustTool("code_nav", "Navigate code by symbol in any language. outline: definitions in a file or directory (cheaper than reading it). definition: where a symbol is defined. references: where it is used.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"op":     map[string]any{"type": "string", "enum": []string{"outline", "definition", "references"}},
			"path":   map[string]any{"type": "string", "description": "outline: file or directory (required). definition/references: optional directory to limit the search"},
			"symbol": map[string]any{"type": "string", "description": "definition/references: identifier name, e.g. parseConfig"},
		},
		"required": []string{"op"},
	}),
	mustTool("run_tests", "Run the project's test command and get a short summary: pass/fail, failing test names and error locations (15 min timeout). Find the command first (AGENTS.md, README, CI config, Makefile, package.json scripts); add the runner's own filter to run a subset.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "exact test command, e.g. go test ./... or npm test -- -t login; may be omitted only if the project pins one"},
		},
	}),
	mustTool("exec", "Start background command (server, watcher, REPL); returns before it finishes, with session id and initial output. write_stdin sends input. Notification on exit; run auto-resumes with captured output if any — no need to keep checking. Plain sleeps refused (use bash). Periodic wake-up: schedule op=once, check status on each hit. Timer/reminder: schedule op=once.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":       map[string]any{"type": "string"},
			"yield_time_ms": map[string]any{"type": "integer", "description": "ms to wait for initial output; default 250, max 5000"},
		},
		"required": []string{"command"},
	}),
	mustTool("write_stdin", "Send characters to exec session stdin (include \\n for enter); returns new output. close_input closes stdin so the command can finish.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_id":    map[string]any{"type": "integer"},
			"chars":         map[string]any{"type": "string", "description": "input to send; include \n to press enter"},
			"close":         map[string]any{"type": "boolean", "description": "close stdin after writing"},
			"yield_time_ms": map[string]any{"type": "integer", "description": "ms to wait for output; default 250, max 5000"},
		},
		"required": []string{"session_id"},
	}),
	mustTool("web", "Search the web or fetch a page. Search returns numbered results (title, URL, snippet); to read one, call again with its URL as url. Fetch returns the page's readable text.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":   map[string]any{"type": "string", "description": "page to fetch (http/https)"},
			"query": map[string]any{"type": "string", "description": "search terms; used when url is empty"},
		},
	}),
	mustTool("subagent", "Delegate a self-contained task (broad exploration, research, a large isolated change) to a sub-agent with its own context; its final report is returned. The prompt must stand alone: goal, relevant paths, constraints, expected output.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{"type": "string", "description": "complete standalone task description"},
		},
		"required": []string{"prompt"},
	}),
	mustTool("notify_user", "Request desktop notification when this run finishes. Only if user asked to be told later.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{"type": "string", "description": "Notification text; defaults to Task completed."},
		},
	}),
	mustTool("schedule", "Create or cancel scheduled agent tasks: op=once (delay + task prompt, runs as agent turn when due), op=cron (recurring expression + task); cancel by id. Listing is status's job. Lives only while this process is open.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"op":         map[string]any{"type": "string", "enum": []string{"once", "cron", "cancel"}},
			"delay":      map[string]any{"type": "string", "description": "once: delay such as 15m, 2h, or 1h30m"},
			"expression": map[string]any{"type": "string", "description": "cron: five local-time fields, for example '0 9 * * 1-5'; supports numbers, *, */step, ranges, and comma lists"},
			"task":       map[string]any{"type": "string", "description": "task prompt to run on schedule"},
			"id":         map[string]any{"type": "integer", "description": "cancel: scheduled task id"},
		},
		"required": []string{"op"},
	}),
	mustTool("status", "Read scheduled agent tasks and exec session states (active/completed).", map[string]any{
		"type": "object",
	}),
	mustTool("memory", "Scratchpad for long tasks: save facts you will need later (decisions, paths, findings). It survives context compaction, so older tool output can be dropped safely. Not needed for short tasks. Ops: write (key, value), read (key), delete (key), list, clear.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"op":    map[string]any{"type": "string", "enum": []string{"write", "read", "delete", "list", "clear"}, "description": "default list"},
			"key":   map[string]any{"type": "string", "description": "short name, e.g. auth_flow"},
			"value": map[string]any{"type": "string", "description": "terse note; required for write"},
		},
	}),
	mustTool("plan", "Checklist for tasks with 3 or more steps; skip it for simple tasks. write: set the items (statuses of unchanged items are kept; active starts one). next: mark the current item done and start the next — use it after finishing each step. update: set one item's status. Item numbers are 1-based, as shown.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"op":     map[string]any{"type": "string", "enum": []string{"write", "next", "update", "list"}},
			"items":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "write: all steps in order, short phrases"},
			"active": map[string]any{"type": "integer", "description": "write: item number to start now (1-based)"},
			"index":  map[string]any{"type": "integer", "description": "update: item number (1-based)"},
			"status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "done"}},
		},
		"required": []string{"op"},
	}),
	mustTool("session_search", "Search transcripts of your past sessions with this user, for things like 'what did we decide about X' or 'how did we fix that bug'. Returns ranked snippets with file paths.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "all words must appear in the same message"},
			"limit": map[string]any{"type": "integer", "description": "max hits; default 8"},
		},
		"required": []string{"query"},
	}),
	mustTool("skill", "Reusable recipes for common non-code tasks (slides, video, PDF/DOCX, spreadsheets, diagrams, images, audio), each naming the concrete tooling to use. op=list shows what is available; load the one matching the task before starting it; save persists a new recipe to the project.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"op":   map[string]any{"type": "string", "enum": []string{"list", "load", "save"}, "description": "default list"},
			"name": map[string]any{"type": "string", "description": "load/save: skill name or alias, e.g. slides"},
			"body": map[string]any{"type": "string", "description": "save: recipe markdown"},
		},
	}),
	mustTool("ask_user", "Ask blocking question only when you cannot proceed without the user's decision; turn ends, reply comes as next message. Don't ask what you can find out yourself. Away mode: no questions — conservative, reversible choices only; away never authorizes destructive, irreversible, external or wider-scope actions, nor bypassing safeguards.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{"type": "string"},
			"options":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "suggested answers, if any"},
		},
		"required": []string{"question"},
	}),
}

var readOnlyTools = []string{"read", "search", "list_files", "batch_read", "code_nav", "web", "status"}

// For O(1) lookup of read-only tools.
var readOnlyToolSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(readOnlyTools))
	for _, name := range readOnlyTools {
		set[name] = struct{}{}
	}
	return set
}()

func IsReadOnlyTool(name string) bool {
	_, ok := readOnlyToolSet[name]
	return ok
}

func Definitions(readOnlyMode bool) []litellm.Tool {
	if !readOnlyMode {
		return definitions
	}

	filtered := make([]litellm.Tool, 0, len(readOnlyTools))
	for _, tool := range definitions {
		if _, ok := readOnlyToolSet[tool.Name]; ok {
			filtered = append(filtered, tool)
		}
	}

	return filtered
}

func mustTool(name, desc string, schema map[string]any) litellm.Tool {
	t, err := litellm.NewTool(name, desc, schema)
	if err != nil {
		panic(err)
	}
	return t
}
