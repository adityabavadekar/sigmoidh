package sigmoidh

import "testing"

func TestIsConcurrentSlashCommand(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"/help", true},
		{"/usage", true},
		{"/caveman", true},
		{"copy", true}, // leading slash optional
		{"/deep-research", false},
		{"/compact", false},
		{"/exit", false},
		{"/nope", false},
	}
	for _, c := range cases {
		if got := isConcurrentSlashCommand(c.name); got != c.want {
			t.Errorf("isConcurrentSlashCommand(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSlashCommandName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/compact now", "/compact"},
		{"/exit", "/exit"},
		{"  /help \t", "/help"},
		{"hello", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := slashCommandName(c.in); got != c.want {
			t.Errorf("slashCommandName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSlashQueueFIFO(t *testing.T) {
	var tui TUI
	if got := tui.popSlash(); got != "" {
		t.Fatalf("popSlash on empty = %q, want empty", got)
	}
	for i, line := range []string{"/compact", "/undo", "/resume"} {
		if got := tui.enqueueSlash(line); got != i+1 {
			t.Errorf("enqueueSlash(%q) position = %d, want %d", line, got, i+1)
		}
	}
	for _, want := range []string{"/compact", "/undo", "/resume"} {
		if got := tui.popSlash(); got != want {
			t.Fatalf("popSlash = %q, want %q", got, want)
		}
	}
	if got := tui.popSlash(); got != "" {
		t.Fatalf("popSlash after drain = %q, want empty", got)
	}
}
