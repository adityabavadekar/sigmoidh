package tools

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var textLikeTypes = []string{
	"text/", "application/json", "application/xml", "application/yaml",
	"application/x-yaml", "application/toml", "application/x-sh",
	"application/javascript", "application/x-javascript",
}

// isTextFile sniffs content and rejects anything that isn't text, JSON, YAML,
// CSV, or similar.
func isTextFile(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	ct := http.DetectContentType(data)
	for _, prefix := range textLikeTypes {
		if strings.HasPrefix(ct, prefix) {
			return true
		}
	}
	return false
}

func isWithinPath(path, root string) bool {
	path = resolveRealPath(resolvePath(path))
	root = resolveRealPath(resolvePath(root))

	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// isEnvFile matches .env and its variants (.env.local, .env.production, ...)
func isEnvFile(path string) bool {
	base := filepath.Base(path)
	return base == ".env" || strings.HasPrefix(base, ".env.")
}

var sensitiveNames = map[string]bool{
	"id_rsa": true, "id_ed25519": true, "id_ecdsa": true,
	".npmrc": true, ".pypirc": true, ".netrc": true,
	"credentials": true, "shadow": true,
}

// isSensitiveFile flags .env files and common credential filenames.
func isSensitiveFile(path string) bool {
	if isEnvFile(path) {
		return true
	}
	base := filepath.Base(path)
	if sensitiveNames[base] {
		return true
	}
	return strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key")
}

// protectedRoots are top-level directories (plus the user's home) that a
// destructive command should never be allowed to target, whether named
// directly or as an ancestor of the given path. Everything under one of
// these is protected, not just the root itself.
var protectedRoots = []string{
	"/", "/etc", "/usr", "/var", "/root", "/home", "/boot",
	"/bin", "/sbin", "/lib", "/lib64", "/sys", "/proc", "/dev",
	"/run", "/opt", "/srv",
}

// shallowProtectedRoots are mount-point directories that exist specifically
// to have other filesystems mounted under them.
var shallowProtectedRoots = []string{
	"/mnt", "/media",
}

// resolvePath expands a leading ~ and returns a cleaned absolute path.
func resolvePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + strings.TrimPrefix(p, "~")
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return filepath.Clean(abs)
}

// resolveRealPath takes the absolute path from
// resolvePath and resolves it through the filesystem so a symlink cannot be
// used to reach a protected location under an innocuous-looking name.
func resolveRealPath(cleaned string) string {
	if cleaned == "/" {
		return "/"
	}
	remainder := ""
	cur := cleaned
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Clean(real + remainder)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return cleaned
		}
		remainder = "/" + filepath.Base(cur) + remainder
		cur = parent
	}
}

// allowedPaths are absolute, resolved paths exempt from protection (e.g. the
// host's persistent memory note). Exact matches only — never directories.
var allowedPaths []string

// SetAllowedPaths registers exact file paths that file tools may access even
// though they resolve under a protected root.
func SetAllowedPaths(paths ...string) {
	allowedPaths = allowedPaths[:0]
	for _, p := range paths {
		if p == "" {
			continue
		}
		allowedPaths = append(allowedPaths, resolveRealPath(filepath.Clean(p)))
	}
}

func isAllowedPath(r string) bool {
	for _, a := range allowedPaths {
		if r == a {
			return true
		}
	}
	return false
}

// AllowPath adds one exact file path to the allow-list without clearing the
// others. Used for paths that only become known at runtime (e.g. the current
// session transcript, whose name is chosen when the first message is written).
func AllowPath(p string) {
	if p == "" {
		return
	}
	r := resolveRealPath(filepath.Clean(p))
	for _, a := range allowedPaths {
		if a == r {
			return
		}
	}
	allowedPaths = append(allowedPaths, r)
}

// isProtectedPath reports whether p resolves to a system root, the user's
// home directory, or anything nested inside one of them.
func isProtectedPath(p string) bool {
	r := resolveRealPath(resolvePath(p))
	if isAllowedPath(r) {
		return false
	}
	if r == "/" {
		return true
	}
	for _, root := range protectedRoots {
		if root == "/" {
			continue
		}
		if r == root || strings.HasPrefix(r, root+"/") {
			return true
		}
	}
	for _, root := range shallowProtectedRoots {
		if r == root {
			return true
		}
	}
	if home, err := os.UserHomeDir(); err != nil || home == "" {
		if onHomeDirUnknown != nil {
			onHomeDirUnknown(p)
		}
	} else if r == home {
		return true
	}
	return false
}

func isProtectedCommandPath(path, workingDir string) bool {
	// The workspace is explicitly allowed.
	if isWithinPath(path, workingDir) {
		return false
	}

	return isProtectedPath(path)
}

// onHomeDirUnknown, if set, is called whenever isProtectedPath cannot
// determine the user's home directory and therefore could not check
// whether the given path falls inside it.
var onHomeDirUnknown func(checkedPath string)

type shellToken struct {
	text   string
	quoted bool
}

// tokenizeShell splits cmd on unquoted whitespace and operators, tracking
// which tokens came from inside a quote. It is not a full shell parser.
func tokenizeShell(cmd string) []shellToken {
	var tokens []shellToken
	var cur strings.Builder
	var quote rune
	var quoted bool

	runes := []rune(cmd)
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, shellToken{cur.String(), quoted})
			cur.Reset()
			quoted = false
		}
	}

	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			} else {
				cur.WriteRune(ch)
			}
		case ch == '\'' || ch == '"':
			quote = ch
			quoted = true
		case ch == ' ' || ch == '\t':
			flush()
		case ch == '\n' || ch == '\r':
			flush()
			tokens = append(tokens, shellToken{"\n", false})
		case ch == '$' && i+1 < len(runes) && runes[i+1] == '(':
			flush()
		case ch == '(' || ch == ')':
			flush()
			tokens = append(tokens, shellToken{string(ch), false})
		case ch == '`':
			flush()
			tokens = append(tokens, shellToken{"`", false})
		case ch == '&' || ch == '|' || ch == ';':
			flush()
			tokens = append(tokens, shellToken{string(ch), false})
		default:
			cur.WriteRune(ch)
		}
	}
	flush()
	return tokens
}

func shellSegments(cmd string) [][]string {
	var segs [][]string
	var cur []string
	for _, t := range tokenizeShell(cmd) {
		if !t.quoted && isShellOperator(t.text) {
			if len(cur) > 0 {
				segs = append(segs, cur)
				cur = nil
			}
			continue
		}
		if t.quoted {
			continue
		}
		cur = append(cur, t.text)
	}
	if len(cur) > 0 {
		segs = append(segs, cur)
	}
	return segs
}

func isShellOperator(s string) bool {
	switch s {
	case "&&", "||", ";", "|", "&", "\n", "(", ")", "`":
		return true
	}
	return false
}

// wrapperCommands are programs that invoke another command rather than
// doing anything destructive themselves.
var wrapperCommands = map[string]bool{
	"sudo": true, "env": true, "command": true,
}

// stripWrappers removes leading wrapper commands (and sudo, since it is
// checked separately below for its own destructive-target semantics) along
// with their flags and VAR=val assignment arguments, returning the
// remaining words with the real command first.
func stripWrappers(seg []string) []string {
	for len(seg) > 0 && wrapperCommands[seg[0]] {
		rest := seg[1:]
		i := 0
		stop := false
		for i < len(rest) {
			a := rest[i]
			switch {
			case a == "--":
				i++
				stop = true
			case strings.HasPrefix(a, "-"):
				i++
				continue
			case isAssignment(a):
				i++
				continue
			default:
				stop = true
			}
			if stop {
				break
			}
		}
		seg = rest[i:]
		if len(seg) == 0 {
			break
		}
	}
	return seg
}

// isAssignment reports whether a token looks like a shell VAR=value
// prefix, e.g. PATH=/bin:/usr/bin, as used before env/a command.
func isAssignment(tok string) bool {
	eq := strings.Index(tok, "=")
	if eq <= 0 {
		return false
	}
	name := tok[:eq]
	for i, r := range name {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

var destructiveCommands = map[string]bool{
	"rm": true, "rmdir": true, "mkfs": true, "dd": true,
	"shred": true, "truncate": true, "find": true,
	"chmod": true, "chown": true, "wipefs": true,
}

var devTargetingCommands = map[string]bool{
	"dd": true, "mkfs": true, "wipefs": true,
}

func isMkfsVariant(name string) bool {
	return name == "mkfs" || strings.HasPrefix(name, "mkfs.")
}

var forkBombPattern = regexp.MustCompile(
	`(?:function\s+)?(\S*)\s*\(\s*\)\s*\{\s*(\S+)\s*\|\s*(\S+)\s*&\s*\}\s*;\s*(\S+)`,
)

func isForkBomb(cmd string) bool {
	m := forkBombPattern.FindStringSubmatch(cmd)
	if m == nil {
		return false
	}
	leading, inner1, inner2, invoke := m[1], m[2], m[3], m[4]
	invoke = strings.TrimSuffix(invoke, ";")
	if inner1 != inner2 || inner1 != invoke {
		return false
	}
	// If a name appeared before the parens, it must be the same name used
	// throughout; if the parens had no leading name, that's fine too.
	return leading == "" || leading == inner1
}

// destructiveCommandOnProtectedPath scans each command segment for a
// destructive command whose arguments touch a protected root, sudo/env/
// command-wrapped destructive commands regardless of target, a device-
// targeting command (dd/mkfs/wipefs) pointed at /dev, or a fork bomb
// pattern. It returns the offending segment for use in the guard message.
func destructiveCommandOnProtectedPath(cmd string, cwd ...string) (string, bool) {
	workingDir := "."
	if len(cwd) > 0 && cwd[0] != "" {
		workingDir = cwd[0]
	}
	if isForkBomb(cmd) {
		return strings.TrimSpace(cmd), true
	}
	if gitPushCommitGuard {
		if segment, blocked := gitPushCommitCommand(cmd); blocked {
			return segment, true
		}
	}

	for _, rawSeg := range shellSegments(cmd) {
		if len(rawSeg) == 0 {
			continue
		}
		seg := stripWrappers(rawSeg)
		if len(seg) == 0 {
			continue
		}

		name := seg[0]
		args := seg[1:]
		if !destructiveCommands[name] && !isMkfsVariant(name) {
			continue
		}
		if isMkfsVariant(name) {
			name = "mkfs"
		}

		if name == "find" {
			hasDelete := false
			hasProtectedPath := false
			for _, a := range args {
				if a == "-delete" {
					hasDelete = true
					continue
				}
				if strings.HasPrefix(a, "-") {
					continue
				}
				if isProtectedCommandPath(a, workingDir) {
					hasProtectedPath = true
				}
			}
			if hasDelete && hasProtectedPath {
				return strings.Join(rawSeg, " "), true
			}
			continue
		}

		blocked := false
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if isProtectedCommandPath(a, workingDir) {
				blocked = true
				break
			}
		}
		if !blocked && devTargetingCommands[name] {
			for _, a := range args {
				if strings.Contains(a, "/dev/") {
					blocked = true
					break
				}
			}
		}
		if blocked {
			return strings.Join(rawSeg, " "), true
		}
	}
	return "", false
}

// gitPushCommitCommand recognizes push and commit subcommands, including common
// git global options such as -C and --git-dir before the subcommand.
func gitPushCommitCommand(cmd string) (string, bool) {
	for _, rawSeg := range shellSegments(cmd) {
		if len(rawSeg) < 2 {
			continue
		}
		seg := stripWrappers(rawSeg)
		if len(seg) < 2 || filepath.Base(seg[0]) != "git" {
			continue
		}
		for i := 1; i < len(seg); i++ {
			tok := seg[i]
			if tok == "push" || tok == "commit" {
				return strings.Join(rawSeg, " "), true
			}
			if tok == "-C" || tok == "-c" || tok == "--git-dir" || tok == "--work-tree" || tok == "--namespace" || tok == "--exec-path" {
				i++ // these global options consume a value
				continue
			}
			if !strings.HasPrefix(tok, "-") {
				break // first non-option is another subcommand
			}
		}
	}
	return "", false
}

// BlockedCommandRefusal returns the refusal message for a segment blocked by
// destructiveCommandOnProtectedPath: the git push/commit guard's message when
// that guard owns the match, otherwise the generic destructive-path refusal.
func BlockedCommandRefusal(segment string) string {
	if gitPushCommitGuard {
		if _, blocked := gitPushCommitCommand(segment); blocked {
			return refusalBashGitPushCommit
		}
	}
	return refusalBashDestructive
}
