package sigmoidh

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agenttools "github.com/adityabavadekar/sigmoidh/tools"
)

// triggerArgs remains for compatibility with internal notification tests.
type triggerArgs struct {
	When      string `json:"when"`
	Message   string `json:"message"`
	SessionID int    `json:"session_id"`
}

// notifyFn is swappable so tests can capture notifications.
var notifyFn = func(title, message string) {
	SendNotification(title, message)
}

func (s *Sigmoidh) handleTrigger(_ context.Context, raw json.RawMessage, _ string) (string, bool) {
	var a triggerArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err.Error(), true
	}
	a.When = strings.ToLower(strings.TrimSpace(a.When))
	msg := strings.TrimSpace(a.Message)
	if msg == "" {
		msg = PING_NOTIFICATION_MESSAGE
	}

	switch a.When {
	case "run_complete":
		s.triggers = append(s.triggers, msg)
		return fmt.Sprintf("Will notify the user when this run completes (%s).", msg), false

	case "command_done":
		if a.SessionID <= 0 {
			return "session_id is required when when=command_done (use the id returned by exec)", true
		}
		ok := agenttools.OnExecSessionDone(a.SessionID, func(result agenttools.ExecSessionResult) {
			text := msg
			if result.Err != nil {
				text = fmt.Sprintf("%s (command exited with error: %v)", msg, result.Err)
			}
			notifyFn(PING_NOTIFICATION_TITLE, text)
			s.observer.OnTriggerFired(text)
		})
		if !ok {
			return fmt.Sprintf("no exec session %d — it may have already finished; check its output instead", a.SessionID), true
		}
		return fmt.Sprintf("Will notify the user when exec session %d exits.", a.SessionID), false

	default:
		return `when must be "run_complete" or "command_done"`, true
	}
}

type notifyUserArgs struct {
	Message string `json:"message"`
}

func (s *Sigmoidh) handleNotifyUser(_ context.Context, raw json.RawMessage, _ string) (string, bool) {
	var a notifyUserArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err.Error(), true
	}
	message := strings.TrimSpace(a.Message)
	if message == "" {
		message = PING_NOTIFICATION_MESSAGE
	}
	s.triggers = append(s.triggers, message)
	return fmt.Sprintf("Will notify the user when this run completes (%s).", message), false
}

// fireRunCompleteTriggers sends and clears pending run_complete notifications.
func (s *Sigmoidh) fireRunCompleteTriggers() {
	for _, msg := range s.triggers {
		notifyFn(PING_NOTIFICATION_TITLE, msg)
		s.observer.OnTriggerFired(msg)
	}
	s.triggers = nil
}
