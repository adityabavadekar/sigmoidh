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
	mustTool("list_files", "List files and directories under the working directory. Skips .git and credential/secret files.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":        map[string]any{"type": "string", "description": "directory to list; defaults to the working directory"},
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
