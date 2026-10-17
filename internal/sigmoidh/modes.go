package sigmoidh

import "strings"

// Reply/behavior modes layered onto each run as a short prefix instruction.
// They are advisory steering only: none of them loosen guards or grant
// extra permissions. Caveman is a persistent toggle; away mode is defined in
// ask_user.go. Deep research is NOT a mode — it is a one-shot command that
// prefixes its own directive onto the supplied query (see handleSlashCommand).

const (
	awayDirective = "[Away mode: no questions to the user. Conservative, reversible choices only. Away never authorizes destructive, irreversible, external or wider-scope actions, nor bypassing safeguards. Skip actions needing confirmation; continue safe work.]"

	cavemanDirective = "[Caveman mode: reply ultra-terse, caveman style. Drop greetings, articles and filler; short words and fragments. Keep every technical fact exact — code, commands, paths, names, numbers unchanged. Still do the full work; only the prose is trimmed.]"

	researchDirective = "[Deep research mode: optimize for depth, not a tidy summary. Keep searching, reading and cross-checking until the question is genuinely answered; prefer primary sources and cite concrete paths/URLs. Never emit a generic \"Executive Summary\"; give specific, decision-useful findings and state remaining uncertainty explicitly.]"
)

func (s *Sigmoidh) Caveman() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.caveman
}

func (s *Sigmoidh) SetCaveman(on bool) {
	s.stateMu.Lock()
	s.caveman = on
	s.stateMu.Unlock()
}

// modeDirectives concatenates the prefix instructions for every active mode.
func (s *Sigmoidh) modeDirectives() string {
	var parts []string
	if s.IsAway() {
		parts = append(parts, awayDirective)
	}
	if s.Caveman() {
		parts = append(parts, cavemanDirective)
	}
	return strings.Join(parts, "\n")
}
