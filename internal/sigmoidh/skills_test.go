package sigmoidh

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSkillFile(t *testing.T) {
	data := []byte("---\nname: slides\naliases: pptx, deck\ndescription: Build decks.\nwhen: user wants slides\nrequires: python-pptx\n---\n\n# Body\nhello\n")
	sk := parseSkillFile("builtin", "slides.md", data)
	if sk.Name != "slides" {
		t.Errorf("Name = %q", sk.Name)
	}
	if len(sk.Aliases) != 2 || sk.Aliases[0] != "pptx" || sk.Aliases[1] != "deck" {
		t.Errorf("Aliases = %v", sk.Aliases)
	}
	if sk.Description != "Build decks." || sk.When != "user wants slides" || sk.Requires != "python-pptx" {
		t.Errorf("fields = %+v", sk)
	}
	if !strings.Contains(sk.Body, "# Body") {
		t.Errorf("Body = %q", sk.Body)
	}
}

func TestParseSkillFileNameFallback(t *testing.T) {
	sk := parseSkillFile("builtin", "video.md", []byte("# no frontmatter"))
	if sk.Name != "video" {
		t.Errorf("Name = %q, want video", sk.Name)
	}
}

func TestBuiltinSkillsPresent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := map[string]bool{}
	for _, sk := range allSkills("") {
		got[sk.Name] = true
		if sk.Description == "" {
			t.Errorf("skill %q has no description", sk.Name)
		}
	}
	for _, want := range []string{"slides", "video", "documents", "spreadsheets", "diagrams", "images", "audio", "kaggle"} {
		if !got[want] {
			t.Errorf("builtin skill %q missing (have %v)", want, got)
		}
	}
}

func TestFindSkillByNameAndAlias(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"slides", "PPTX", "deck", "pptx"} {
		sk, ok := findSkill("", name)
		if !ok || sk.Name != "slides" {
			t.Errorf("findSkill(%q) = %q,%v; want slides", name, sk.Name, ok)
		}
	}
	if _, ok := findSkill("", "nope"); ok {
		t.Error("findSkill(nope) unexpectedly found a skill")
	}
}

func TestSkillCatalogText(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cat := skillCatalogText("")
	if !strings.Contains(cat, "- slides:") || !strings.Contains(cat, "- video:") {
		t.Errorf("catalog missing entries:\n%s", cat)
	}
}

func TestSaveSkillProjectOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	path, err := saveSkill(dir, "my-notes", "---\nname: my-notes\ndescription: custom\n---\n\n# Custom")
	if err != nil {
		t.Fatalf("saveSkill: %v", err)
	}
	if !strings.HasSuffix(path, "my-notes.md") {
		t.Errorf("path = %q", path)
	}
	sk, ok := findSkill(dir, "my-notes")
	if !ok || sk.Source != "project" || sk.Description != "custom" {
		t.Errorf("findSkill after save = %+v, ok=%v", sk, ok)
	}
}

func TestHandleSkill(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	s := &Sigmoidh{}
	call := func(body string) string {
		out, _ := s.handleSkill(context.Background(), json.RawMessage(body), dir)
		return out
	}
	if out := call(`{"op":"list"}`); !strings.Contains(out, "slides") {
		t.Errorf("list = %q", out)
	}
	if out := call(`{"op":"load","name":"pptx"}`); !strings.Contains(out, "# Skill: slides") {
		t.Errorf("load = %q", out)
	}
	if out := call(`{"op":"load","name":"missing"}`); !strings.Contains(out, "No skill named") {
		t.Errorf("load missing = %q", out)
	}
	out := call(`{"op":"save","name":"house-style","body":"# House\nUse our palette."}`)
	if !strings.Contains(out, "Saved skill to") {
		t.Errorf("save = %q", out)
	}
	if out := call(`{"op":"bogus"}`); !strings.Contains(out, "Unknown op") {
		t.Errorf("bogus = %q", out)
	}
}
