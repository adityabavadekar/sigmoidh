package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ProjectConfig is optional per-project tuning, read from
// <workdir>/.sigmoidh/config.json and then ~/.sigmoidh/config.json (project
// values win). Everything has working defaults, so the file is only needed
// to override detection:
//
//	{
//	  "checks": [
//	    {"glob": "*.go",  "command": "go vet ./..."},
//	    {"glob": "src/**/*.ts", "command": "npx eslint {file}"},
//	    {"glob": "*.md",  "command": ""}            // disable checks for md
//	  ],
//	  "test": "./test.sh"
//	}
//
// Placeholders: {file} (absolute path of the changed file), {dir} (its
// directory), {root} (the working directory).
type ProjectConfig struct {
	Checks []CheckRule `json:"checks"`
	Test   string      `json:"test"`
}

// CheckRule runs Command after a file matching Glob is changed. An empty
// Command disables checks for matching files.
type CheckRule struct {
	Glob    string `json:"glob"`
	Command string `json:"command"`
}

// globalConfigPath is overridable for tests.
var globalConfigPath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".sigmoidh", "config.json")
}

// loadProjectConfig merges the project config over the global one. Missing
// or malformed files are ignored (a broken config must not break tools).
func loadProjectConfig(workDir string) ProjectConfig {
	var merged ProjectConfig
	for _, path := range []string{filepath.Join(workDir, ".sigmoidh", "config.json"), globalConfigPath()} {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var c ProjectConfig
		if json.Unmarshal(data, &c) != nil {
			continue
		}
		merged.Checks = append(merged.Checks, c.Checks...) // project rules first
		if merged.Test == "" {
			merged.Test = c.Test
		}
	}
	return merged
}
