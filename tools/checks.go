package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Post-edit checks: after write/edit/apply_patch changes a file, run the
// project's own linters (or a cheap syntax check) on it and append any
// problems to the tool result, so the agent fixes breakage in the same turn
// instead of discovering it later.
//
// Selection is data-driven, most specific first:
//  1. rules from .sigmoidh/config.json (any language, any command);
//  2. linters the project has configured itself — a config file such as
//     eslint.config.js or ruff.toml plus the binary (PATH, node_modules/.bin
//     or a local venv);
//  3. built-in syntax checks, only where the needed binary exists.
//
// Nothing is required: unknown file types and missing binaries are skipped.

const (
	checkTimeout   = 25 * time.Second
	checkOutputCap = 1500
)

var postEditChecksOn = os.Getenv("SIGMOIDH_NO_CHECKS") == ""

// SetPostEditChecks turns post-edit checks on or off.
func SetPostEditChecks(on bool) { postEditChecksOn = on }

type checker struct {
	name    string
	exts    []string
	bin     string   // "" = internal check (see internalChecks)
	args    []string // placeholders: {file} {dir} {root}
	markers []string // project files enabling it ("file" or "file:substring"); empty = always
	lint    bool     // a project-configured linter: when one applies, syntax checks are skipped
	inDir   bool     // run in the file's directory instead of the root
	// outputIsProblem: the tool exits 0 but prints when something is wrong
	outputIsProblem bool
	env             []string
}

var builtinCheckers = []checker{
	// linters the project configured
	{name: "eslint", exts: []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".vue", ".svelte"}, bin: "eslint", args: []string{"{file}"}, lint: true,
		markers: []string{"eslint.config.js", "eslint.config.mjs", "eslint.config.cjs", "eslint.config.ts", ".eslintrc", ".eslintrc.js", ".eslintrc.cjs", ".eslintrc.json", ".eslintrc.yml", ".eslintrc.yaml", "package.json:\"eslintConfig\""}},
	{name: "biome", exts: []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".json", ".jsonc", ".css"}, bin: "biome", args: []string{"lint", "{file}"}, lint: true,
		markers: []string{"biome.json", "biome.jsonc"}},
	{name: "tsc", exts: []string{".ts", ".tsx", ".mts", ".cts"}, bin: "tsc", args: []string{"--noEmit", "--pretty", "false", "-p", "{root}"}, lint: true,
		markers: []string{"tsconfig.json"}},
	{name: "ruff", exts: []string{".py", ".pyi"}, bin: "ruff", args: []string{"check", "--quiet", "{file}"}, lint: true,
		markers: []string{"ruff.toml", ".ruff.toml", "pyproject.toml:[tool.ruff"}},
	{name: "mypy", exts: []string{".py", ".pyi"}, bin: "mypy", args: []string{"--no-error-summary", "{file}"}, lint: true,
		markers: []string{"mypy.ini", ".mypy.ini", "pyproject.toml:[tool.mypy", "setup.cfg:[mypy"}},
	{name: "golangci-lint", exts: []string{".go"}, bin: "golangci-lint", args: []string{"run", "--fast", "."}, lint: true, inDir: true,
		markers: []string{".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json"}},
	{name: "rubocop", exts: []string{".rb", ".rake"}, bin: "rubocop", args: []string{"--format", "simple", "{file}"}, lint: true,
		markers: []string{".rubocop.yml"}},
	{name: "clippy", exts: []string{".rs"}, bin: "cargo", args: []string{"clippy", "--quiet", "--message-format", "short"}, lint: true,
		markers: []string{"clippy.toml", ".clippy.toml"}},
	{name: "shellcheck", exts: []string{".sh", ".bash"}, bin: "shellcheck", args: []string{"-f", "gcc", "{file}"}, lint: true,
		markers: []string{".shellcheckrc"}},
	{name: "stylelint", exts: []string{".css", ".scss", ".less"}, bin: "stylelint", args: []string{"{file}"}, lint: true,
		markers: []string{".stylelintrc", ".stylelintrc.json", ".stylelintrc.js", "stylelint.config.js", "stylelint.config.mjs"}},

	// built-in syntax / build checks
	{name: "gofmt", exts: []string{".go"}, bin: "gofmt", args: []string{"-l", "-e", "{file}"}, outputIsProblem: true},
	{name: "go vet", exts: []string{".go"}, bin: "go", args: []string{"vet", "."}, inDir: true, markers: []string{"go.mod"},
		env: []string{"GOPROXY=off", "GOFLAGS=-mod=mod"}},
	{name: "python", exts: []string{".py", ".pyi"}, bin: "python3",
		args: []string{"-c", "import ast,sys; ast.parse(open(sys.argv[1], encoding='utf-8').read(), sys.argv[1])", "{file}"}},
	{name: "node", exts: []string{".js", ".mjs", ".cjs"}, bin: "node", args: []string{"--check", "{file}"}},
	{name: "cargo check", exts: []string{".rs"}, bin: "cargo", args: []string{"check", "--quiet", "--message-format", "short"}, markers: []string{"Cargo.toml"}},
	{name: "bash", exts: []string{".sh", ".bash"}, bin: "bash", args: []string{"-n", "{file}"}},
	{name: "ruby", exts: []string{".rb", ".rake"}, bin: "ruby", args: []string{"-wc", "{file}"}},
	{name: "php", exts: []string{".php"}, bin: "php", args: []string{"-l", "{file}"}},
	{name: "perl", exts: []string{".pl", ".pm"}, bin: "perl", args: []string{"-c", "{file}"}},
	{name: "lua", exts: []string{".lua"}, bin: "luac", args: []string{"-p", "{file}"}},
	{name: "json", exts: []string{".json"}},
	{name: "terraform", exts: []string{".tf"}, bin: "terraform", args: []string{"fmt", "-check", "-diff", "{file}"}},
}

// internalChecks need no external binary.
var internalChecks = map[string]func(path string) string{
	"json": func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil || len(bytes.TrimSpace(data)) == 0 {
			return ""
		}
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return "invalid JSON: " + err.Error()
		}
		return ""
	},
}

// postEditChecks runs the checks that apply to path and returns a report of
// problems, or "" when everything passed or nothing applied.
func postEditChecks(ctx context.Context, path, workDir string) string {
	if !postEditChecksOn {
		return ""
	}
	root := workDir
	if root == "" {
		root = filepath.Dir(path)
	}
	var problems []string
	add := func(name, out string) {
		if out = strings.TrimSpace(out); out != "" {
			out = strings.ReplaceAll(out, root+string(filepath.Separator), "")
			if len(out) > checkOutputCap {
				out = out[:checkOutputCap] + "\n…(truncated)"
			}
			problems = append(problems, fmt.Sprintf("[%s]\n%s", name, out))
		}
	}

	// 1. explicit project rules
	if rules := matchingRules(loadProjectConfig(root).Checks, path, root); rules != nil {
		for _, r := range rules {
			if strings.TrimSpace(r.Command) == "" {
				continue // rule disables checks for these files
			}
			cmd := expandPlaceholders(r.Command, path, root, true)
			out, failed := runCheckCommand(ctx, root, nil, "sh", "-c", cmd)
			if failed {
				add(r.Command, out)
			}
		}
		return formatProblems(problems)
	}

	// 2/3. project linters, else syntax checks
	ext := strings.ToLower(filepath.Ext(path))
	var lints, syntax []checker
	for _, c := range builtinCheckers {
		if !hasExt(c.exts, ext) || !markersPresent(c.markers, path, root) {
			continue
		}
		if c.bin != "" && resolveCheckBin(c.bin, root) == "" {
			continue
		}
		if c.lint {
			lints = append(lints, c)
		} else {
			syntax = append(syntax, c)
		}
	}
	chosen := syntax
	if len(lints) > 0 {
		chosen = lints
	}
	for _, c := range chosen {
		if fn, ok := internalChecks[c.name]; ok && c.bin == "" {
			add(c.name, fn(path))
			continue
		}
		args := make([]string, len(c.args))
		for i, a := range c.args {
			args[i] = expandPlaceholders(a, path, root, false)
		}
		dir := root
		if c.inDir {
			dir = filepath.Dir(path)
		}
		out, failed := runCheckCommand(ctx, dir, c.env, resolveCheckBin(c.bin, root), args...)
		if c.outputIsProblem && strings.TrimSpace(out) != "" {
			if c.name == "gofmt" && strings.TrimSpace(out) == path {
				out = "file is not gofmt-formatted (run gofmt -w on it)"
			}
			failed = true
		}
		if failed {
			add(c.name, out)
		}
	}
	return formatProblems(problems)
}

func formatProblems(problems []string) string {
	if len(problems) == 0 {
		return ""
	}
	return "checks found problems (the change was applied; fix these next):\n" + strings.Join(problems, "\n")
}

// runCheckCommand runs one check; failed is true for a non-zero exit. A
// timeout or a missing binary is not a failure: checks are best-effort.
func runCheckCommand(ctx context.Context, dir string, env []string, name string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", false
	}
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return "", false // could not start
		}
		return string(out), true
	}
	return string(out), false
}

func expandPlaceholders(s, file, root string, quote bool) string {
	q := func(v string) string {
		if quote {
			return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
		}
		return v
	}
	return strings.NewReplacer("{file}", q(file), "{dir}", q(filepath.Dir(file)), "{root}", q(root)).Replace(s)
}

func hasExt(exts []string, ext string) bool {
	for _, e := range exts {
		if e == ext {
			return true
		}
	}
	return false
}

// markersPresent reports whether any marker exists in the file's directory
// or an ancestor up to root. "name:text" also requires the file to contain text.
func markersPresent(markers []string, path, root string) bool {
	if len(markers) == 0 {
		return true
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		for _, m := range markers {
			name, want, hasWant := strings.Cut(m, ":")
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				if _, serr := os.Stat(filepath.Join(dir, name)); serr != nil || hasWant {
					continue
				}
			}
			if !hasWant || bytes.Contains(data, []byte(want)) {
				return true
			}
		}
		if dir == root || dir == filepath.Dir(dir) || !strings.HasPrefix(dir, root) {
			return false
		}
	}
}

// resolveCheckBin finds a check binary on PATH or in project-local tool dirs.
func resolveCheckBin(bin, root string) string {
	if p, err := exec.LookPath(bin); err == nil {
		return p
	}
	for _, dir := range []string{"node_modules/.bin", ".venv/bin", "venv/bin", "vendor/bin"} {
		p := filepath.Join(root, dir, bin)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// matchingRules returns config rules whose glob matches path; nil if none.
func matchingRules(rules []CheckRule, path, root string) []CheckRule {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	rel = filepath.ToSlash(rel)
	var out []CheckRule
	for _, r := range rules {
		if globMatch(r.Glob, rel) {
			out = append(out, r)
		}
	}
	return out
}

// globMatch matches slash paths with * (within a segment), ? and ** (any
// depth). A pattern without a slash matches the base name anywhere.
func globMatch(pattern, rel string) bool {
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		ok, _ := filepath.Match(pattern, filepath.Base(rel))
		return ok
	}
	var re strings.Builder
	re.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					re.WriteString("(?:.*/)?")
				} else {
					re.WriteString(".*")
				}
			} else {
				re.WriteString("[^/]*")
			}
		case '?':
			re.WriteString("[^/]")
		default:
			re.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	re.WriteString("$")
	ok, _ := regexp.MatchString(re.String(), rel)
	return ok
}
