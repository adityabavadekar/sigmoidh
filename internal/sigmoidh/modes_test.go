package sigmoidh

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDegenerateRepetition(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"normal prose", "Here is a normal answer with several varied words and no runaway loop at all.", false},
		{"short answer", "yes", false},
		{"word run", "the the the the the the the result", true},
		{"word run with punctuation", "Done done, done done done done done.", true},
		{"phrase run", strings.Repeat("I will do it ", 6) + "now", true},
		{"two similar words", "this that this that this that", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := degenerateRepetition(c.text) != ""
			if got != c.want {
				t.Fatalf("degenerateRepetition(%q)=%v, want %v (reason %q)", c.text, got, c.want, degenerateRepetition(c.text))
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Fix the Failing Test!": "fix-the-failing-test",
		"  spaced   out  ":      "spaced-out",
		"!!!":                   "skill",
		"":                      "skill",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Fatalf("slugify(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestModeDirectives(t *testing.T) {
	s := &Sigmoidh{}
	if s.modeDirectives() != "" {
		t.Fatalf("no modes should mean no directives, got %q", s.modeDirectives())
	}
	s.SetCaveman(true)
	s.SetAway(true)
	d := s.modeDirectives()
	if !strings.Contains(d, "Away mode") || !strings.Contains(d, "Caveman mode") {
		t.Fatalf("expected both away and caveman directives, got %q", d)
	}
	s.SetCaveman(false)
	if strings.Contains(s.modeDirectives(), "Caveman") {
		t.Fatalf("caveman directive should be gone after disabling")
	}
}

func TestSkillSteps(t *testing.T) {
	activities := []skillActivity{
		{name: "read", args: json.RawMessage(`{"path":"a.go"}`)},
		{name: "bash", args: json.RawMessage(`{"command":"go build ./..."}`)},
		{name: "bash", args: json.RawMessage(`{"command":"go build ./..."}`)},
		{name: "edit", args: json.RawMessage(`{"path":"main.go"}`)},
		{name: "subagent", args: json.RawMessage(`{"prompt":"research X"}`)},
	}
	steps := skillSteps(activities)
	if len(steps) != 3 {
		t.Fatalf("expected 3 deduped steps (bash/edit/subagent), got %d: %v", len(steps), steps)
	}
	if steps[0] != "Run `go build ./...`" || steps[1] != "Edit `main.go`" {
		t.Fatalf("unexpected steps: %v", steps)
	}
}

func TestCavemanSlashToggles(t *testing.T) {
	s := &Sigmoidh{}
	if _, err := handleSlashCommand(context.Background(), s, "/caveman"); err != nil {
		t.Fatalf("caveman: %v", err)
	}
	if !s.Caveman() {
		t.Fatal("caveman should be on after first /caveman")
	}
	if _, err := handleSlashCommand(context.Background(), s, "/caveman"); err != nil {
		t.Fatalf("caveman: %v", err)
	}
	if s.Caveman() {
		t.Fatal("caveman should be off after second /caveman")
	}
	if _, err := handleSlashCommand(context.Background(), s, "/caveman on"); err != nil {
		t.Fatalf("caveman on: %v", err)
	}
	if !s.Caveman() {
		t.Fatal("caveman should be on after '/caveman on'")
	}
}
