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

	card := "❓ " + q
	if len(a.Options) > 0 {
		card += "\n" + fmt.Sprintf("options: %s", strings.Join(a.Options, " / "))
	}
	s.observer.OnTriggerFired(card)

	s.pendingAsk = &card
	return "question delivered to the user. End your turn now; their next message is the answer.", false
}
