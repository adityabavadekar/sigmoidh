package sigmoidh

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Built-in recipes ("sensible defaults") compiled into the binary. They teach
// the agent where-from for common artifact tasks: slides, video, documents,
// spreadsheets, diagrams, images, and audio.
//
//go:embed skills/*.md
var builtinSkills embed.FS

// Skill is one recipe. Body is the markdown after the frontmatter.
type Skill struct {
	Name        string
	Aliases     []string
	Description string
	When        string
	Requires    string
	Body        string
	Source      string // "builtin", "personal", or "project"
}

// personalSkillsDir and projectSkillsDir are where user recipes live; project
// overrides personal, personal overrides built-in (matched by name).
func personalSkillsDir() string {
	return filepath.Join(homeDir(), ".sigmoidh", "skills")
}

func projectSkillsDir(workDir string) string {
	return filepath.Join(workDir, ".sigmoidh", "skills")
}

// parseSkillFile splits a `---` frontmatter block from the body. Unknown keys
// are ignored; a missing name falls back to the filename stem.
func parseSkillFile(source, filename string, data []byte) Skill {
	sk := Skill{Source: source}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	body := text
	if strings.HasPrefix(text, "---\n") {
		if end := strings.Index(text[4:], "\n---"); end >= 0 {
			front := text[4 : 4+end]
			body = strings.TrimPrefix(text[4+end+4:], "\n")
			for _, line := range strings.Split(front, "\n") {
				key, val, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				key = strings.ToLower(strings.TrimSpace(key))
				val = strings.Trim(strings.TrimSpace(val), `"'`)
				switch key {
				case "name":
					sk.Name = val
				case "aliases":
					for _, a := range strings.Split(val, ",") {
						if a = strings.TrimSpace(a); a != "" {
							sk.Aliases = append(sk.Aliases, a)
						}
					}
				case "description":
					sk.Description = val
				case "when":
					sk.When = val
				case "requires":
					sk.Requires = val
				}
			}
		}
	}
	sk.Body = strings.TrimSpace(body)
	if sk.Name == "" {
		sk.Name = strings.TrimSuffix(filename, filepath.Ext(filename))
	}
	return sk
}

func loadSkillsFromDir(source, dir string, into map[string]Skill) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if sk := parseSkillFile(source, e.Name(), data); sk.Name != "" {
			into[strings.ToLower(sk.Name)] = sk
		}
	}
}

// allSkills merges built-in, personal, and project recipes (later wins) and
// returns them sorted by name.
func allSkills(workDir string) []Skill {
	byName := map[string]Skill{}

	if entries, err := builtinSkills.ReadDir("skills"); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			data, err := builtinSkills.ReadFile("skills/" + e.Name())
			if err != nil {
				continue
			}
			if sk := parseSkillFile("builtin", e.Name(), data); sk.Name != "" {
				byName[strings.ToLower(sk.Name)] = sk
			}
		}
	}
	loadSkillsFromDir("personal", personalSkillsDir(), byName)
	if workDir != "" {
		loadSkillsFromDir("project", projectSkillsDir(workDir), byName)
	}

	out := make([]Skill, 0, len(byName))
	for _, sk := range byName {
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// findSkill resolves a name or alias (case-insensitive).
func findSkill(workDir, name string) (Skill, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, sk := range allSkills(workDir) {
		if strings.ToLower(sk.Name) == want {
			return sk, true
		}
		for _, a := range sk.Aliases {
			if strings.ToLower(a) == want {
				return sk, true
			}
		}
	}
	return Skill{}, false
}

// skillCatalogText is the compact, prompt-sized index: name + description.
func skillCatalogText(workDir string) string {
	var b strings.Builder
	for _, sk := range allSkills(workDir) {
		desc := sk.Description
		if desc == "" {
			desc = sk.When
		}
		fmt.Fprintf(&b, "- %s: %s\n", sk.Name, desc)
	}
	return strings.TrimRight(b.String(), "\n")
}

// saveSkill writes a recipe to the project's skills directory, adding
// frontmatter when the body does not already carry one.
func saveSkill(workDir, name, body string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("skill name is required")
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("skill body is required")
	}
	dir := projectSkillsDir(workDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	text := strings.TrimSpace(body) + "\n"
	if !strings.HasPrefix(text, "---\n") {
		text = fmt.Sprintf("---\nname: %s\n---\n\n%s", name, text)
	}
	path := filepath.Join(dir, skillFilename(name)+".md")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// skillFilename slugifies a name for use as a file stem.
func skillFilename(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '.':
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "skill"
	}
	return s
}

// handleSkill implements the `skill` tool: list, load, or save recipes.
func (s *Sigmoidh) handleSkill(_ context.Context, raw json.RawMessage, workDir string) (string, bool) {
	var args struct {
		Op   string `json:"op"`
		Name string `json:"name"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return err.Error(), true
	}
	switch strings.ToLower(strings.TrimSpace(args.Op)) {
	case "", "list":
		cat := skillCatalogText(workDir)
		if cat == "" {
			return "No skills are installed.", true
		}
		return "Available skills (load one with the skill tool, op=load, name=<name>):\n" + cat, true
	case "load":
		sk, ok := findSkill(workDir, args.Name)
		if !ok {
			return fmt.Sprintf("No skill named %q. Available:\n%s", args.Name, skillCatalogText(workDir)), true
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# Skill: %s\n", sk.Name)
		if sk.Requires != "" {
			fmt.Fprintf(&b, "Requires: %s\n", sk.Requires)
		}
		b.WriteString("\n")
		b.WriteString(sk.Body)
		return strings.TrimRight(b.String(), "\n"), true
	case "save":
		path, err := saveSkill(workDir, args.Name, args.Body)
		if err != nil {
			return err.Error(), true
		}
		return "Saved skill to " + path, true
	default:
		return fmt.Sprintf("Unknown op %q (use list, load, or save).", args.Op), true
	}
}
