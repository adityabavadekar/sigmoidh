package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Plan: the agent's visible checklist for multi-step work. Session-scoped,
// mutex-guarded like memory, rendered through normal tool-result plumbing so
// both terminal and web views show progress.

type planStatus string

const (
	planPending    planStatus = "pending"
	planInProgress planStatus = "in_progress"
	planDone       planStatus = "done"
)

type planItem struct {
	text   string
	status planStatus
}

type planStore struct {
	mu    sync.Mutex
	items []planItem
}

type planArgs struct {
	Op     string   `json:"op"`
	Items  []string `json:"items"`
	Index  int      `json:"index"`
	Status string   `json:"status"`
}

func (p *planStore) render() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.renderLocked()
}

// renderLocked must be called with p.mu already held.
func (p *planStore) renderLocked() string {
	if len(p.items) == 0 {
		return "(plan empty)"
	}
	var b strings.Builder
	for i, it := range p.items {
		box := "[ ]"
		switch it.status {
		case planInProgress:
			box = "[~]"
		case planDone:
			box = "[x]"
		}
		fmt.Fprintf(&b, "%d. %s %s\n", i+1, box, it.text)
	}
	return strings.TrimRight(b.String(), "\n")
}

func validPlanStatus(s string) (planStatus, bool) {
	switch planStatus(s) {
	case planPending, planInProgress, planDone:
		return planStatus(s), true
	}
	return "", false
}

func (s *Sigmoidh) handlePlan(_ context.Context, raw json.RawMessage, _ string) (string, bool) {
	var a planArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err.Error(), true
	}
	a.Op = strings.ToLower(strings.TrimSpace(a.Op))
	st := s.plan

	switch a.Op {
	case "write":
		if len(a.Items) == 0 {
			return "items array required for op=write", true
		}
		st.mu.Lock()
		st.items = st.items[:0]
		for _, t := range a.Items {
			t = strings.TrimSpace(t)
			if t != "" {
				st.items = append(st.items, planItem{text: t, status: planPending})
			}
		}
		n := len(st.items)
		st.mu.Unlock()
		if n == 0 {
			return "plan needs at least one non-empty item", true
		}
		return fmt.Sprintf("plan set:\n%s", st.render()), false

	case "update":
		status, ok := validPlanStatus(a.Status)
		if a.Status == "" || !ok {
			return `status must be pending|in_progress|done`, true
		}
		st.mu.Lock()
		defer st.mu.Unlock()
		if a.Index < 0 || a.Index >= len(st.items) {
			return fmt.Sprintf("index %d out of range (%d items)", a.Index, len(st.items)), true
		}
		// exactly one in_progress at a time
		for i := range st.items {
			if i == a.Index {
				st.items[i].status = status
			} else if status == planInProgress {
				st.items[i].status = planPending
			}
		}
		snap := st.renderLocked()
		return fmt.Sprintf("updated:\n%s", snap), false

	case "list", "":
		return st.render(), false

	default:
		return `op must be one of: list, write, update`, true
	}
}

type planStoreInit struct{}

func newPlanStore() *planStore { return &planStore{} }
