package sigmoidh

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// loadDotEnv reads KEY=VALUE pairs from .env in the working directory into the
// environment. Existing environment variables always win, and nothing is ever
// logged. Missing file is not an error.
// loadDotEnvGlobal loads .env from the working directory and then the global
// ~/.sigmoidh/.env; per-directory values are applied first so they win.
func loadDotEnvGlobal() {
	if f := os.Getenv("SIGMOIDH_ENV_FILE"); f != "" {
		loadDotEnv(f) // explicit key file wins
	}
	loadDotEnv(".env")
	loadDotEnv(filepath.Join(homeDir(), ".sigmoidh", ".env"))
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		os.Setenv(key, val)
	}
}
