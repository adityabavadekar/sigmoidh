package sigmoidh

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
	Index  int      `json:"index"` // 1-based, as displayed
	Status string   `json:"status"`
	// Optional with op=write: 1-based item to start now, saving a follow-up
	// call. Pointer distinguishes "absent" from a real value.
	Active *int `json:"active,omitempty"`
}

func (p *planStore) render() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.renderLocked()
}

// renderLocked must be called with p.mu already held. Numbers are 1-based
// and are exactly what index/active take: a 0-based API under a 1-based
// display made models update the wrong item.
func (p *planStore) renderLocked() string {
	if len(p.items) == 0 {
		return "(plan empty)"
	}
	done := 0
	for _, it := range p.items {
		if it.status == planDone {
			done++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "plan %d/%d done:\n", done, len(p.items))
	for i, it := range p.items {
		box := "[ ]"
		switch it.status {
		case planInProgress:
			box = "[>]"
		case planDone:
			box = "[x]"
		}
		fmt.Fprintf(&b, "%d. %s %s\n", i+1, box, it.text)
	}
	if done == len(p.items) {
		b.WriteString("all done\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func validPlanStatus(s string) (planStatus, bool) {
	switch planStatus(strings.ToLower(strings.TrimSpace(s))) {
	case planPending, "todo":
		return planPending, true
	case planInProgress, "in-progress", "active", "doing":
		return planInProgress, true
	case planDone, "complete", "completed":
		return planDone, true
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
	if st == nil { // defensive: never nil-deref again even if wiring changes
		st = newPlanStore()
		s.plan = st
	}
	st.mu.Lock()
	defer st.mu.Unlock()

	switch a.Op {
	case "write":
		if len(a.Items) == 0 {
			return "items array required for op=write", true
		}
		// rewriting keeps the status of items whose text is unchanged, so a
		// scope change does not wipe finished work
		prev := map[string]planStatus{}
		for _, it := range st.items {
			prev[it.text] = it.status
		}
		var items []planItem
		for _, t := range a.Items {
			if t = strings.TrimSpace(t); t != "" {
				items = append(items, planItem{text: t, status: prev[t]})
			}
		}
		if len(items) == 0 {
			return "plan needs at least one non-empty item", true
		}
		for i := range items {
			if items[i].status == "" {
				items[i].status = planPending
			}
		}
		if a.Active != nil {
			if *a.Active < 1 || *a.Active > len(items) {
				return fmt.Sprintf("active %d out of range: use 1..%d", *a.Active, len(items)), true
			}
			setActive(items, *a.Active-1)
		}
		st.items = items
		return st.renderLocked(), false

	case "next":
		// finish the current item and start the next pending one: the
		// common step transition in one call
		cur := -1
		for i, it := range st.items {
			if it.status == planInProgress {
				cur = i
				break
			}
		}
		if cur >= 0 {
			st.items[cur].status = planDone
		}
		for i := max(cur, 0); i < len(st.items); i++ {
			if st.items[i].status == planPending {
				st.items[i].status = planInProgress
				break
			}
		}
		if len(st.items) == 0 {
			return "no plan yet: use op=write first", true
		}
		return st.renderLocked(), false

	case "update":
		status, ok := validPlanStatus(a.Status)
		if !ok {
			return `status must be pending, in_progress or done`, true
		}
		if a.Index < 1 || a.Index > len(st.items) {
			return fmt.Sprintf("index %d out of range: use 1..%d", a.Index, len(st.items)), true
		}
		i := a.Index - 1
		if status == planInProgress {
			setActive(st.items, i)
		} else {
			st.items[i].status = status
		}
		return st.renderLocked(), false

	case "list", "":
		return st.renderLocked(), false

	default:
		return `op must be one of: write, next, update, list`, true
	}
}

// setActive makes item i the single in-progress item; one that was already
// in progress is completed (moving on means it is finished), not reset.
func setActive(items []planItem, i int) {
	for k := range items {
		if k != i && items[k].status == planInProgress {
			items[k].status = planDone
		}
	}
	items[i].status = planInProgress
}

func newPlanStore() *planStore { return &planStore{} }
