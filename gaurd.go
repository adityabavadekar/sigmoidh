package main

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
// CSV, or similar. CSV/TSV have no distinct MIME signature and read back as
// text/plain, so they pass without special-casing the extension.
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

// isEnvFile matches .env and its variants (.env.local, .env.production, ...)
// by filename, regardless of directory.
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
// to have other filesystems mounted under them. The bare mount point itself
// is protected (deleting /mnt or /media would be destructive), but what's
// mounted under it is a separate, arbitrary filesystem the guard has no
// basis to treat as part of the protected system tree — e.g. /mnt/data is
// an ordinary mounted volume, not part of the host OS, and should not be
// blocked just because /mnt is.
var shallowProtectedRoots = []string{
	"/mnt", "/media",
}

// resolvePath expands a leading ~ and returns a cleaned absolute path.
// filepath.Abs/Clean operate on the string without any fixed-size buffers,
// so arbitrarily long paths are handled the same as short ones.
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

// isProtectedPath reports whether p resolves to a system root, the user's
// home directory, or anything nested inside one of them. Mount-point
// directories (/mnt, /media) are protected only at the bare mount point —
// a path one or more levels under them is an arbitrary mounted volume, not
// part of the protected system tree, and is not flagged here.
func isProtectedPath(p string) bool {
	r := resolvePath(p)
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
	if home, err := os.UserHomeDir(); err == nil && home != "" && r == home {
		return true
	}
	return false
}

type shellToken struct {
	text   string
	quoted bool
}

// tokenizeShell splits cmd on unquoted whitespace and operators, tracking
// which tokens came from inside a quote. It is not a full shell parser: it
// exists only to keep pattern matching from firing on quoted text such as
// echo 'rm -rf /' > file.txt, and to isolate each &&/||/;/|/&-separated,
// newline-separated, or parenthesized/substituted segment so a dangerous
// command anywhere in a chain — including inside (...), $(...), or
// backticks — is not hidden by a safe one elsewhere.
//
// Parens, backticks, and $( are treated only as segment boundaries, not as
// balanced grouping constructs: this guard never needs to reconstruct real
// shell structure, it only needs every run of unquoted words that could be
// a command name followed by its arguments to appear as its own segment.
// Treating "(" and ")" as boundaries (rather than tracking nesting depth)
// is deliberately conservative — it can only ever split a dangerous command
// into more, still-individually-checked pieces, never hide one by merging
// it with something else.
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
			// A newline separates independent commands just like ";" —
			// unlike a plain space, it must also break apart whatever
			// segment came before it, not merely end the current token.
			flush()
			tokens = append(tokens, shellToken{"\n", false})
		case ch == '$' && i+1 < len(runes) && runes[i+1] == '(':
			// Start of a $(...) command substitution. The "$" carries no
			// command-word meaning on its own; treat the "(" that follows
			// (handled by the next iteration) as the actual boundary and
			// simply drop the "$" so it isn't glued onto the next token.
			flush()
		case ch == '(' || ch == ')':
			// Subshell / substitution delimiters. See function doc: these
			// are boundaries only, nesting is not tracked.
			flush()
			tokens = append(tokens, shellToken{string(ch), false})
		case ch == '`':
			// Backtick command substitution delimiter — same treatment as
			// parens: a boundary marking off whatever is inside from
			// whatever is outside, in both directions.
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

// shellSegments groups unquoted tokens into independent command segments,
// split at &&, ||, ;, |, &, newlines, and subshell/substitution delimiters
// ( ) $( `. Quoted tokens are dropped entirely: they are argument data,
// never command words, so they cannot trigger a match.
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
// doing anything destructive themselves. When one appears at the head of a
// segment, it is stripped (along with its own flags/assignments) so the
// real command underneath can be matched. Wrappers can stack, e.g.
// "sudo env -- rm -rf /", so stripping runs in a loop.
var wrapperCommands = map[string]bool{
	"sudo": true, "env": true, "command": true,
}

// stripWrappers removes leading wrapper commands (and sudo, since it is
// checked separately below for its own destructive-target semantics) along
// with their flags and VAR=val assignment arguments, returning the
// remaining words with the real command first. It never strips past a
// non-flag, non-assignment, non-wrapper word, so it cannot accidentally eat
// into a destructive command's own arguments.
func stripWrappers(seg []string) []string {
	for len(seg) > 0 && wrapperCommands[seg[0]] {
		rest := seg[1:]
		i := 0
		stop := false
		for i < len(rest) {
			a := rest[i]
			switch {
			case a == "--":
				// "--" ends the wrapper's own option list; the word right
				// after it is the real command, not consumed flag data.
				i++
				stop = true
			case strings.HasPrefix(a, "-"):
				i++
				continue
			case isAssignment(a):
				i++
				continue
			default:
				// First bare word: either the real command, or (for
				// stacked wrappers like "sudo env rm ...") another
				// wrapper, which the outer loop will strip on its next
				// pass.
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

// devTargetingCommands take a raw block device path (as a bare argument, or
// via of=/if=) rather than an ordinary file path, so they need the /dev/
// substring check in addition to (or instead of) the protected-root check.
var devTargetingCommands = map[string]bool{
	"dd": true, "mkfs": true, "wipefs": true,
}

// mkfsPrefix catches mkfs.ext4, mkfs.xfs, mkfs.btrfs, etc: mkfs is normally
// invoked as one of these filesystem-specific binaries, not bare "mkfs".
func isMkfsVariant(name string) bool {
	return name == "mkfs" || strings.HasPrefix(name, "mkfs.")
}

// forkBombPattern matches the classic fork-bomb shape independent of the
// identifier used (":", a named function, or no name at all before the
// parens) and independent of whitespace:
//
//	[NAME] '(' ')' '{' NAME '|' NAME '&' '}' ';' NAME
//
// The leading NAME is optional — "(){ :|:& };:" is a valid variant with
// nothing before the parens — but when present it must agree with the two
// names inside the braces, and the trailing invocation after ";" must also
// match whichever name was used inside. This runs against the raw, unsplit
// command string rather than per-segment output, because shellSegments now
// treats "(" and ")" as boundaries and would otherwise never present the
// bomb's defining "(){ ... }" shape as a single contiguous piece to check.
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
//
// Segments are derived from shellSegments, which already breaks the
// command apart at &&, ||, ;, |, &, newlines, and subshell/substitution
// delimiters, so a destructive command hidden inside (...), $(...), or
// backticks is checked exactly like one at the top level.
func destructiveCommandOnProtectedPath(cmd string) (string, bool) {
	if isForkBomb(cmd) {
		return strings.TrimSpace(cmd), true
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
			// Normalize mkfs.ext4 / mkfs.xfs / mkfs.btrfs / etc. to "mkfs"
			// for the devTargetingCommands lookup below. The message
			// returned to the caller still uses rawSeg, so the original
			// name (e.g. "mkfs.ext4") is preserved there.
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
				if isProtectedPath(a) {
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
			if isProtectedPath(a) {
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

