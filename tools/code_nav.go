package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// code_nav: symbol-level navigation for any language.
//
//	outline     the definitions in a file (or every file under a directory) —
//	            far cheaper than reading the whole file to find one function
//	definition  where a symbol is defined, across the project
//	references  where it is used (word match, definitions excluded)
//
// universal-ctags is used when installed (it knows 100+ languages); without
// it a built-in table of definition patterns covers the common languages.

const (
	navTimeout    = 30 * time.Second
	navMaxResults = 100
	navMaxFiles   = 4000
)

type navSymbol struct {
	Name string
	Kind string
	Path string
	Line int
	Text string
}

// defPattern finds definitions in one language family; the first capture
// group is the symbol name.
type defPattern struct {
	kind string
	re   *regexp.Regexp
}

var langDefPatterns = map[string][]defPattern{}

func addLang(exts string, pats ...defPattern) {
	for _, e := range strings.Fields(exts) {
		langDefPatterns[e] = append(langDefPatterns[e], pats...)
	}
}

func dp(kind, expr string) defPattern { return defPattern{kind, regexp.MustCompile(expr)} }

func init() {
	addLang(".go",
		dp("func", `^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*[\[(]`),
		dp("type", `^type\s+([A-Za-z_]\w*)\b`),
		dp("type", `^\s+([A-Z]\w*)\s+(?:struct|interface)\s*\{`),
		dp("var", `^(?:var|const)\s+([A-Za-z_]\w*)\b`))
	addLang(".py .pyi",
		dp("def", `^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)`),
		dp("class", `^\s*class\s+([A-Za-z_]\w*)`),
		dp("var", `^([A-Z_][A-Z0-9_]*)\s*[:=]`))
	addLang(".js .jsx .mjs .cjs .ts .tsx .mts .cts .vue .svelte",
		dp("function", `^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s+([A-Za-z_$][\w$]*)`),
		dp("class", `^\s*(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][\w$]*)`),
		dp("type", `^\s*(?:export\s+)?(?:declare\s+)?(?:interface|type|enum)\s+([A-Za-z_$][\w$]*)`),
		dp("const", `^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::[^=]+)?=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*=>`),
		dp("method", `^\s+(?:public\s+|private\s+|protected\s+|static\s+|async\s+|readonly\s+)*([A-Za-z_$][\w$]*)\s*\([^)]*\)\s*(?::\s*[^{]+)?\{\s*$`))
	addLang(".rs",
		dp("fn", `^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:const\s+)?(?:unsafe\s+)?fn\s+([A-Za-z_]\w*)`),
		dp("type", `^\s*(?:pub(?:\([^)]*\))?\s+)?(?:struct|enum|trait|union|type)\s+([A-Za-z_]\w*)`),
		dp("mod", `^\s*(?:pub\s+)?mod\s+([A-Za-z_]\w*)`),
		dp("macro", `^\s*macro_rules!\s+([A-Za-z_]\w*)`))
	addLang(".java .kt .kts .scala .cs .groovy",
		dp("type", `^\s*(?:[a-z]+\s+)*(?:class|interface|enum|record|object|trait)\s+([A-Za-z_]\w*)`),
		dp("fun", `^\s*(?:[a-z]+\s+)*fun\s+(?:<[^>]*>\s*)?(?:[A-Za-z_]\w*\.)?([A-Za-z_]\w*)\s*\(`),
		dp("method", `^\s*(?:(?:public|private|protected|internal|static|final|abstract|override|virtual|async|synchronized)\s+)+[\w<>\[\],.? ]+\s+([A-Za-z_]\w*)\s*\(`))
	addLang(".c .h .cc .cpp .cxx .hpp .hh .m .mm",
		dp("type", `^\s*(?:typedef\s+)?(?:struct|class|enum|union|namespace)\s+([A-Za-z_]\w*)\s*[{:]?`),
		dp("func", `^[A-Za-z_][\w\s\*&:<>,]*?\b([A-Za-z_]\w*)\s*\([^;]*\)\s*(?:const\s*)?\{?\s*$`),
		dp("macro", `^\s*#\s*define\s+([A-Za-z_]\w*)`))
	addLang(".rb .rake",
		dp("def", `^\s*def\s+(?:self\.)?([A-Za-z_]\w*[?!=]?)`),
		dp("class", `^\s*(?:class|module)\s+([A-Z]\w*)`))
	addLang(".php",
		dp("function", `^\s*(?:(?:public|private|protected|static|abstract|final)\s+)*function\s+&?\s*([A-Za-z_]\w*)`),
		dp("class", `^\s*(?:abstract\s+|final\s+)?(?:class|interface|trait|enum)\s+([A-Za-z_]\w*)`))
	addLang(".swift",
		dp("func", `^\s*(?:[a-z]+\s+)*func\s+([A-Za-z_]\w*)`),
		dp("type", `^\s*(?:[a-z]+\s+)*(?:class|struct|enum|protocol|extension|actor)\s+([A-Za-z_]\w*)`))
	addLang(".ex .exs",
		dp("def", `^\s*defp?\s+([a-z_]\w*[?!]?)`),
		dp("module", `^\s*defmodule\s+([A-Z][\w.]*)`))
	addLang(".lua", dp("function", `^\s*(?:local\s+)?function\s+([A-Za-z_][\w.:]*)`))
	addLang(".sh .bash .zsh", dp("function", `^\s*(?:function\s+)?([A-Za-z_][\w-]*)\s*\(\)\s*\{?`))
	addLang(".hs", dp("func", `^([a-z_]\w*)\s*::`), dp("type", `^(?:data|newtype|type|class)\s+([A-Z]\w*)`))
	addLang(".dart",
		dp("type", `^\s*(?:abstract\s+)?(?:class|mixin|enum|extension)\s+([A-Za-z_]\w*)`),
		dp("func", `^\s*[\w<>?]+\s+([a-z_]\w*)\s*\([^)]*\)\s*(?:async\s*)?\{`))
	addLang(".zig", dp("fn", `^\s*(?:pub\s+)?fn\s+([A-Za-z_]\w*)`), dp("const", `^\s*(?:pub\s+)?const\s+([A-Za-z_]\w*)\s*=`))
	addLang(".sql", dp("object", `(?i)^\s*create\s+(?:or\s+replace\s+)?(?:table|view|function|procedure|index|type)\s+(?:if\s+not\s+exists\s+)?([\w."]+)`))
}

func runCodeNav(ctx context.Context, op, path, symbol string, workDir string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, navTimeout)
	defer cancel()
	switch op {
	case "outline":
		if path == "" {
			return "outline needs path (a file or directory)", true
		}
		target := resolveToolPath(path, workDir)
		if isSensitiveFile(target) {
			return refusalReadSensitive, true
		}
		syms, err := collectSymbols(ctx, target, workDir)
		if err != nil {
			return err.Error(), true
		}
		return formatOutline(syms, workDir), false
	case "definition", "references":
		symbol = strings.TrimSpace(symbol)
		if symbol == "" {
			return op + " needs symbol", true
		}
		root := workDir
		if path != "" {
			root = resolveToolPath(path, workDir)
		}
		syms, err := collectSymbols(ctx, root, workDir)
		if err != nil {
			return err.Error(), true
		}
		var defs []navSymbol
		for _, s := range syms {
			if s.Name == symbol || strings.HasSuffix(s.Name, "."+symbol) || strings.HasSuffix(s.Name, ":"+symbol) {
				defs = append(defs, s)
			}
		}
		if op == "definition" {
			if len(defs) == 0 {
				return fmt.Sprintf("no definition of %q found (searched %s). Try search for usages, or a different spelling.", symbol, relOrSelf(root, workDir)), false
			}
			return formatSymbols(fmt.Sprintf("definitions of %s:", symbol), defs, workDir), false
		}
		return findReferences(ctx, symbol, root, workDir, defs)
	default:
		return `op must be "outline", "definition" or "references"`, true
	}
}

// collectSymbols lists definitions in a file or directory tree, via ctags
// when available, else the built-in patterns.
func collectSymbols(ctx context.Context, target, workDir string) ([]navSymbol, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if syms, ok := ctagsSymbols(ctx, target, info.IsDir()); ok {
		return syms, nil
	}
	var files []string
	if info.IsDir() {
		_ = filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != target && (defaultIgnoredDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if _, ok := langDefPatterns[strings.ToLower(filepath.Ext(p))]; ok && !isIgnoredFile(d.Name()) {
				files = append(files, p)
			}
			if len(files) >= navMaxFiles || ctx.Err() != nil {
				return fs.SkipAll
			}
			return nil
		})
	} else {
		files = []string{target}
	}
	var syms []navSymbol
	for _, f := range files {
		syms = append(syms, patternSymbols(f)...)
	}
	return syms, nil
}

func patternSymbols(path string) []navSymbol {
	pats := langDefPatterns[strings.ToLower(filepath.Ext(path))]
	if len(pats) == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var syms []navSymbol
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxScannerBuf)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		for _, p := range pats {
			if m := p.re.FindStringSubmatch(line); m != nil && !navKeyword[m[1]] {
				syms = append(syms, navSymbol{Name: m[1], Kind: p.kind, Path: path, Line: n, Text: sanitizeLine(line)})
				break
			}
		}
	}
	return syms
}

// navKeyword filters control-flow words the loose method patterns can catch.
var navKeyword = map[string]bool{"if": true, "for": true, "while": true, "switch": true, "catch": true, "return": true, "else": true, "do": true, "try": true, "new": true, "sizeof": true}

// ctagsSymbols uses universal-ctags (JSON output) when it is installed.
func ctagsSymbols(ctx context.Context, target string, dir bool) ([]navSymbol, bool) {
	bin, err := exec.LookPath("ctags")
	if err != nil {
		return nil, false
	}
	args := []string{"--output-format=json", "--fields=+nK", "-f", "-"}
	if dir {
		args = append(args, "-R", "--exclude=.git", "--exclude=node_modules", "--exclude=vendor", "--exclude=dist", "--exclude=build")
	}
	out, err := exec.CommandContext(ctx, bin, append(args, target)...).Output()
	if err != nil {
		return nil, false // e.g. exuberant ctags without JSON: fall back
	}
	var syms []navSymbol
	for _, ln := range strings.Split(string(out), "\n") {
		var t struct {
			Type string `json:"_type"`
			Name string `json:"name"`
			Path string `json:"path"`
			Line int    `json:"line"`
			Kind string `json:"kind"`
			Pat  string `json:"pattern"`
		}
		if json.Unmarshal([]byte(ln), &t) != nil || t.Type != "tag" {
			continue
		}
		text := strings.TrimSuffix(strings.TrimPrefix(t.Pat, "/^"), "$/")
		syms = append(syms, navSymbol{Name: t.Name, Kind: t.Kind, Path: t.Path, Line: t.Line, Text: sanitizeLine(text)})
	}
	return syms, true
}

// findReferences lists word matches of symbol, excluding its definitions.
func findReferences(ctx context.Context, symbol, root, workDir string, defs []navSymbol) (string, bool) {
	isDef := map[string]bool{}
	for _, d := range defs {
		isDef[fmt.Sprintf("%s:%d", d.Path, d.Line)] = true
	}
	re := `\b` + regexp.QuoteMeta(symbol) + `\b`
	raw, isErr := runSearchContext(re, relOrSelf(root, workDir), "regex", navMaxResults*2, 0, workDir)
	if isErr {
		return raw, true
	}
	if raw == noMatches {
		return fmt.Sprintf("no references to %q found", symbol), false
	}
	var refs []navSymbol
	for _, ln := range strings.Split(raw, "\n") {
		parts := strings.SplitN(ln, ":", 4)
		if len(parts) != 4 {
			continue
		}
		var n int
		fmt.Sscan(parts[1], &n)
		abs := filepath.Join(workDir, parts[0])
		if isDef[fmt.Sprintf("%s:%d", abs, n)] || isDef[fmt.Sprintf("%s:%d", parts[0], n)] {
			continue
		}
		refs = append(refs, navSymbol{Path: abs, Line: n, Text: parts[3]})
		if len(refs) >= navMaxResults {
			break
		}
	}
	if len(refs) == 0 {
		return fmt.Sprintf("no references to %q outside its definition", symbol), false
	}
	return formatSymbols(fmt.Sprintf("references to %s (%d):", symbol, len(refs)), refs, workDir), false
}

func formatOutline(syms []navSymbol, workDir string) string {
	if len(syms) == 0 {
		return "no definitions found (unsupported language, or the file has none)"
	}
	return formatSymbols(fmt.Sprintf("%d definitions (complete list):", len(syms)), syms, workDir)
}

// formatSymbols groups symbols by file: "path\n  12 kind name — line text".
func formatSymbols(header string, syms []navSymbol, workDir string) string {
	sort.SliceStable(syms, func(i, j int) bool {
		if syms[i].Path != syms[j].Path {
			return syms[i].Path < syms[j].Path
		}
		return syms[i].Line < syms[j].Line
	})
	var b strings.Builder
	b.WriteString(header)
	last := ""
	for i, s := range syms {
		if i >= navMaxResults*3 {
			fmt.Fprintf(&b, "\n… %d more", len(syms)-i)
			break
		}
		if s.Path != last {
			fmt.Fprintf(&b, "\n%s (%d lines)", relOrSelf(s.Path, workDir), countLines(s.Path))
			last = s.Path
		}
		// the definition line itself names the symbol; repeat kind and
		// name only when the line does not show them
		switch {
		case s.Text != "" && (s.Name == "" || strings.Contains(s.Text, s.Name)):
			fmt.Fprintf(&b, "\n  %d: %s", s.Line, s.Text)
		case s.Text != "":
			fmt.Fprintf(&b, "\n  %d: %s %s: %s", s.Line, s.Kind, s.Name, s.Text)
		default:
			fmt.Fprintf(&b, "\n  %d: %s %s", s.Line, s.Kind, s.Name)
		}
	}
	return truncate(b.String())
}

func countLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := strings.Count(string(data), "\n")
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

func relOrSelf(path, workDir string) string {
	if workDir == "" {
		return path
	}
	if rel, err := filepath.Rel(workDir, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}
