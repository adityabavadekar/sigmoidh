package sigmoidh

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	agenttools "github.com/adityabavadekar/sigmoidh/tools"
)

type scheduledTaskArgs struct {
	Op         string `json:"op"`
	Delay      string `json:"delay"`
	Expression string `json:"expression"`
	Task       string `json:"task"`
	ID         int    `json:"id"`
}

func (s *Sigmoidh) handleSchedule(_ context.Context, raw json.RawMessage, _ string) (string, bool) {
	var a scheduledTaskArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err.Error(), true
	}
	switch a.Op {
	case "cancel":
		return s.cancelScheduledTask(a.ID)
	case "once":
		d, err := time.ParseDuration(strings.TrimSpace(a.Delay))
		if err != nil || d <= 0 {
			return "duration must be positive, for example 15m, 2h, or 1h30m", true
		}
		if strings.TrimSpace(a.Task) == "" {
			return "task prompt is required", true
		}
		id := s.tasks.AddTimer(d, a.Task, s.runScheduledTask)
		return fmt.Sprintf("Scheduled one-time task #%d for %s local time. It will run only while Sigmoidh stays open.", id, time.Now().Add(d).Format(time.RFC3339)), false
	case "cron":
		expr := strings.TrimSpace(a.Expression)
		if _, err := parseCron(expr); err != nil {
			return err.Error(), true
		}
		if strings.TrimSpace(a.Task) == "" {
			return "task prompt is required", true
		}
		next, err := nextCron(expr, time.Now())
		if err != nil {
			return err.Error(), true
		}
		id := s.tasks.AddCron(expr, a.Task, s.runCronTask)
		return fmt.Sprintf("Scheduled recurring task #%d (%s); next run: %s local time. It will run only while Sigmoidh stays open.", id, expr, next.Format(time.RFC3339)), false
	default:
		return `op must be "once", "cron", or "cancel" (status lists tasks and sessions)`, true
	}
}

func (s *Sigmoidh) handleStatus(_ context.Context, _ json.RawMessage, _ string) (string, bool) {
	scheduled := "none"
	if s.tasks != nil {
		scheduled = s.tasks.Status()
	}
	return fmt.Sprintf("Scheduled tasks: %s\nExec sessions: %s", scheduled, agenttools.ExecSessionsStatus()), false
}

func (s *Sigmoidh) cancelScheduledTask(id int) (string, bool) {
	if id < 1 || s.tasks == nil || !s.tasks.Cancel(id) {
		return fmt.Sprintf("no scheduled task with id %d", id), true
	}
	return fmt.Sprintf("Cancelled scheduled task #%d", id), false
}
