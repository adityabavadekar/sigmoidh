package sigmoidh

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ask_user: a BLOCKING question that ends the agent's turn; the user's next
// REPL message is the answer. Implemented as an external handler that sets a
// pending flag; run() checks it after tool execution and stops cleanly.

type askUserArgs struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

func (s *Sigmoidh) handleAskUser(_ context.Context, raw json.RawMessage, _ string) (string, bool) {
	var a askUserArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err.Error(), true
	}
	q := strings.TrimSpace(a.Question)
	if q == "" {
		return "question is required", true
	}
	if s.IsAway() {
		return "Away mode: user must not be interrupted. Don't ask; continue with conservative, reversible choices. Away never authorizes destructive, irreversible, external or wider-scope actions, nor bypassing safeguards. If confirmation needed: skip the action, continue safe work, report the limit.", false
	}

	card := "❓ " + q
	if len(a.Options) > 0 {
		card += "\n" + fmt.Sprintf("options: %s", strings.Join(a.Options, " / "))
	}
	s.observer.OnTriggerFired(card)

	s.pendingAsk = &card
	return "question delivered to the user. End your turn now; their next message is the answer.", false
}
