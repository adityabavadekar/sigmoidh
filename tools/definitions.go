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
			"path":     map[string]any{"type": "string"},
			"offset":   map[string]any{"type": "integer", "description": "1-based first line to read"},
			"limit":    map[string]any{"type": "integer", "description": "max number of lines to read"},
			"numbered": map[string]any{"type": "boolean", "description": "if true, prefix each line with its line number"},
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
	mustTool("edit", "Replace an exact substring in a text file. old_str must match exactly once in the file; the call is refused if it matches zero times (nothing to replace) or more than once (ambiguous which occurrence to change). Refuses binary files and credential/secret files, same as read, and refuses protected system paths, same as write.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"old_str": map[string]any{"type": "string", "description": "exact text to find; must occur exactly once in the file"},
			"new_str": map[string]any{"type": "string", "description": "text to replace it with"},
		},
		"required": []string{"path", "old_str", "new_str"},
	}),
	mustTool("search", "Search project text/code and return matching file paths, line numbers, and columns. Searches the working directory without reading credential/secret files.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Text or regular expression to search for",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "File or directory to search; defaults to the working directory",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{"literal", "regex"},
				"description": "Search mode; defaults to literal",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Maximum number of matching lines to return",
			},
		},
		"required": []string{"query"},
	}),
	mustTool("list_files", "List files/directories, or find files by glob pattern (pattern param supports **). Skips .git, node_modules and other vendored/cache dirs, plus credential/secret files.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":        map[string]any{"type": "string", "description": "directory to list; defaults to the working directory"},
			"pattern":     map[string]any{"type": "string", "description": "glob filter, e.g. **/*.go or src/*.ts — returns matching FILES"},
			"max_results": map[string]any{"type": "integer", "description": "maximum entries to return; defaults to 200"},
		},
	}),
	mustTool("batch_read", "Read several text files at once. Uses the same guards and truncation behavior as read.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"paths":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"offset":   map[string]any{"type": "integer", "description": "1-based first line to read from each file"},
			"limit":    map[string]any{"type": "integer", "description": "max number of lines to read from each file"},
			"numbered": map[string]any{"type": "boolean", "description": "if true, prefix each line with its line number"},
		},
		"required": []string{"paths"},
	}),
	mustTool("apply_patch", "Apply a unified diff patch in the working directory. Prefer this for code edits instead of rewriting whole files.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"patch":   map[string]any{"type": "string", "description": "unified diff text"},
			"strip":   map[string]any{"type": "integer", "description": "patch -p strip count; defaults to 0"},
			"reverse": map[string]any{"type": "boolean", "description": "apply the patch in reverse"},
		},
		"required": []string{"patch"},
	}),
	mustTool("exec", "Start a persistent shell command session and return its session id plus recent output. Use write_stdin to interact with it.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":       map[string]any{"type": "string", "description": "shell command to start"},
			"yield_time_ms": map[string]any{"type": "integer", "description": "milliseconds to wait before returning initial output"},
		},
		"required": []string{"command"},
	}),
	mustTool("write_stdin", "Send input to a persistent exec session and return recent output. Send empty input to poll.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_id":    map[string]any{"type": "integer"},
			"chars":         map[string]any{"type": "string", "description": "input to write; omit or empty to poll"},
			"close":         map[string]any{"type": "boolean", "description": "close stdin after writing"},
			"yield_time_ms": map[string]any{"type": "integer", "description": "milliseconds to wait before returning output"},
		},
		"required": []string{"session_id"},
	}),
	mustTool("web", "Fetch a URL or run a simple web search. Returns extracted text, truncated.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":   map[string]any{"type": "string", "description": "URL to fetch"},
			"query": map[string]any{"type": "string", "description": "search query; used when url is empty"},
		},
	}),
	mustTool("subagent", "Spawn a focused sub-agent that completes one task autonomously with its own context and tools (except spawning further sub-agents beyond the depth limit). Use it for self-contained subtasks like broad exploration, large refactors, or research that would flood the main context. Provide a complete, standalone prompt describing the goal and constraints. The sub-agent's final report is returned as the tool result.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{"type": "string", "description": "standalone task description with goal, relevant paths/context, and expected output format"},
		},
		"required": []string{"prompt"},
	}),
	mustTool("trigger", "Schedule a notification for the user. Use this when you start something long-running (a background command or the rest of the task) and the user asked to be pinged when it finishes. The desktop notification fires automatically; do not wait for it.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"when": map[string]any{
				"type":        "string",
				"enum":        []string{"run_complete", "command_done"},
				"description": "event to notify on: run_complete = when the current task/run finishes; command_done = when an exec session's command exits",
			},
			"message":    map[string]any{"type": "string", "description": "short notification text (default: Task completed.)"},
			"session_id": map[string]any{"type": "integer", "description": "exec session id; required when when=command_done"},
		},
		"required": []string{"when"},
	}),
	mustTool("memory", "Your working memory across turns: store distilled facts, decisions, file paths, and findings so older tool output can be dropped without losing essentials. Keep values terse. Ops: list (default), write (key+value), read (key), delete (key), clear.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"op":    map[string]any{"type": "string", "enum": []string{"list", "write", "read", "delete", "clear"}, "description": "operation; defaults to list"},
			"key":   map[string]any{"type": "string", "description": "short identifier, e.g. 'auth_flow'"},
			"value": map[string]any{"type": "string", "description": "terse note content; required for write"},
		},
	}),
	mustTool("plan", "Maintain the task checklist for multi-step work. Rewrite it whenever scope changes and mark items done immediately after finishing them. Ops: list (default), write (items array replaces the whole plan), update (index+status: pending|in_progress|done). Keep exactly one item in_progress while working.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"op":     map[string]any{"type": "string", "enum": []string{"list", "write", "update"}, "description": "defaults to list"},
			"items":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "write: full checklist in order"},
			"index":  map[string]any{"type": "integer", "description": "update: 0-based item index"},
			"status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "done"}},
		},
	}),
	mustTool("session_search", "Search YOUR OWN past conversations with this user (transcripts of every prior session). Use for cross-session recall: 'what did we decide about X last week', 'how did I fix that bug before'. Returns ranked snippets with source file paths for deeper reading.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "all words must appear in the same message"},
			"limit": map[string]any{"type": "integer", "description": "max hits (default 8)"},
		},
		"required": []string{"query"},
	}),
	mustTool("ask_user", "Ask the user a BLOCKING clarifying question, then end your turn. Use when requirements are ambiguous or a decision is needed before proceeding. The user's next message is the answer.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]any{"type": "string"},
			"options":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "suggested answers, if any"},
		},
		"required": []string{"question"},
	}),
}

var readOnlyTools = []string{"read", "search", "list_files", "batch_read", "web"}

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
