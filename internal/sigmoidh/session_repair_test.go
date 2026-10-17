package sigmoidh

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voocel/litellm"
)

// Regression: a process killed mid-turn leaves assistant tool calls with no
// result entry; the trailing user messages then trip litellm's validator
// ("user message follows unresolved tool use") and the session can never be
// resumed. The loader must synthesize error results for those calls.
func TestLoadSessionRepairsUnresolvedToolUses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	path := writeFakeSession(t, dir, "20260825-112117.jsonl", []historyEntry{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "build it"},
		{Role: "assistant", Content: "Planning.", ToolCalls: []historyToolCall{
			{ID: "plan-1", Name: "plan", Args: json.RawMessage(`{"op":"write"}`)},
		}},
		// crash here: no tool entry for plan-1 …
		{Role: "user", Content: "continue;"},
	})

	msgs, err := loadSessionMessages(path)
	if err != nil {
		t.Fatal(err)
	}
	// user, assistant(plan), tool(synthetic), user — in that order
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages after repair, got %d", len(msgs))
	}
	tool := msgs[2]
	if tool.Role != litellm.RoleTool {
		t.Fatalf("expected synthetic tool message at [2], got %s", tool.Role)
	}
	rb, ok := tool.Blocks[0].(litellm.ToolResultBlock)
	if !ok {
		t.Fatalf("expected ToolResultBlock, got %T", tool.Blocks[0])
	}
	if rb.ToolUseID != "plan-1" || !rb.IsError {
		t.Fatalf("synthetic result should reference plan-1 as an error, got %+v", rb)
	}

	// and the repaired history must satisfy the same invariant litellm's
	// request validator enforces (the rule that bricked resumption)
	if err := assertNoUnresolvedToolUse(sanitizeForRequest(msgs)); err != nil {
		t.Fatalf("repaired messages still invalid: %v", err)
	}
}

// assertNoUnresolvedToolUse mirrors the litellm validation rule that rejected
// crashed transcripts ("user message follows unresolved tool use").
func assertNoUnresolvedToolUse(msgs []litellm.Message) error {
	open := map[string]bool{}
	for i, m := range msgs {
		for _, b := range m.Blocks {
			switch bb := b.(type) {
			case litellm.ToolUseBlock:
				open[bb.ID] = true
			case litellm.ToolResultBlock:
				delete(open, bb.ToolUseID)
			}
		}
		if m.Role == litellm.RoleUser && len(open) > 0 {
			return fmt.Errorf("messages[%d]: user message follows unresolved tool use", i)
		}
	}
	if len(open) > 0 {
		return errors.New("unresolved tool use at end of history")
	}
	return nil
}

// A transcript that just ends mid-turn (no trailing user message) must also
// get its dangling calls resolved.
func TestLoadSessionRepairsTrailingToolUse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	path := writeFakeSession(t, dir, "20260824-010834.jsonl", []historyEntry{
		{Role: "user", Content: "go"},
		{Role: "assistant", Content: "", ToolCalls: []historyToolCall{
			{ID: "bash-1", Name: "bash", Args: json.RawMessage(`{"cmd":"ls"}`)},
			{ID: "bash-2", Name: "bash", Args: json.RawMessage(`{"cmd":"pwd"}`)},
		}},
	})

	msgs, err := loadSessionMessages(path)
	if err != nil {
		t.Fatal(err)
	}
	var last = msgs[len(msgs)-1]
	if last.Role != litellm.RoleTool {
		t.Fatalf("expected transcript to end with synthetic results, got %s", last.Role)
	}
	if len(last.Blocks) != 2 {
		t.Fatalf("expected one synthetic result per unresolved call, got %d", len(last.Blocks))
	}
}

func TestPlanStoreAlwaysInitialized(t *testing.T) {
	s := &Sigmoidh{} // simulates any future wiring that skips NewSigmoidh
	out, isErr := s.handlePlan(nil, json.RawMessage(`{"op":"write","items":["a","b"]}`), "")
	if isErr {
		t.Fatalf("plan write failed: %s", out)
	}
	if s.plan == nil || len(s.plan.items) != 2 {
		t.Fatalf("plan store not lazily initialized")
	}
}

func TestPlanWriteMarksActiveInSameCall(t *testing.T) {
	s := &Sigmoidh{plan: newPlanStore()}

	// absent active -> everything pending (0 must count as a real index)
	out, isErr := s.handlePlan(nil, json.RawMessage(`{"op":"write","items":["a","b"]}`), "")
	if isErr || strings.Contains(out, "[>]") {
		t.Fatalf("no active should stay all-pending, got err=%v out=%q", isErr, out)
	}

	// active is 1-based like the display: active:2 starts item 2 in one call
	out, isErr = s.handlePlan(nil, json.RawMessage(`{"op":"write","items":["a","b"],"active":2}`), "")
	if isErr {
		t.Fatalf("plan write+active failed: %s", out)
	}
	board := s.plan.render()
	if !strings.Contains(board, "[>] b") || strings.Contains(board, "[>] a") {
		t.Fatalf("expected only item 2 in progress:\n%s", board)
	}

	// out-of-range active is a tool error, plan untouched
	s2 := &Sigmoidh{plan: newPlanStore()}
	if _, isErr := s2.handlePlan(nil, json.RawMessage(`{"op":"write","items":["a"],"active":5}`), ""); !isErr {
		t.Fatal("expected out-of-range active to be an error")
	}
}

func TestQueuePersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SIGMOIDH_PENDING_PROMPTS", filepath.Join(dir, "pending_prompts.jsonl"))

	tu := NewHeadlessTUI("p", "m", false)

	tu.queueMu.Lock()
	tu.queue = []string{"first", "second"}
	tu.queueMu.Unlock()
	tu.persistQueueAppend("first")
	tu.persistQueueAppend("second")

	got := loadPendingPrompts()
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("round-trip mismatch: %v", got)
	}

	// drain one -> file shrinks to the remainder
	tu.queueMu.Lock()
	tu.queue = tu.queue[1:]
	tu.queueMu.Unlock()
	tu.persistQueueRewrite()

	got = loadPendingPrompts()
	if len(got) != 1 || got[0] != "second" {
		t.Fatalf("rewrite mismatch: %v", got)
	}

	// drain all -> sidecar removed
	tu.queueMu.Lock()
	tu.queue = nil
	tu.queueMu.Unlock()
	tu.persistQueueRewrite()
	if _, err := os.Stat(pendingPromptsPath()); !os.IsNotExist(err) {
		t.Fatalf("sidecar should be gone when queue empties, err=%v", err)
	}
}

func TestLiveStreamUsageCounters(t *testing.T) {
	tu := NewHeadlessTUI("p", "m", false)
	core := tu.core

	// mid-stream events are cumulative per request: input first, then output growth
	core.OnStreamUsage(litellm.Usage{InputTokens: 1000})
	core.OnStreamUsage(litellm.Usage{InputTokens: 1000, OutputTokens: 40, TotalTokens: 1040, ReasoningTokens: 30})

	snap := core.snapshot()
	if snap.liveTotal != 1040 || snap.tokTotal != 0 {
		t.Fatalf("live counters wrong: live=%d committed=%d", snap.liveTotal, snap.tokTotal)
	}
	if snap.lastInputTokens != 1000 { // context meter gets real input immediately
		t.Fatalf("lastInputTokens not updated from stream usage: %d", snap.lastInputTokens)
	}

	// turn completes: fold into committed totals, clear live so nothing counts twice
	core.OnTurnUsage(litellm.Usage{InputTokens: 1000, OutputTokens: 55, TotalTokens: 1055, ReasoningTokens: 35})
	snap = core.snapshot()
	if snap.tokTotal != 1055 || snap.liveTotal != 0 {
		t.Fatalf("post-turn totals wrong: total=%d live=%d", snap.tokTotal, snap.liveTotal)
	}

	// next request resets stale live values defensively
	core.OnStreamUsage(litellm.Usage{InputTokens: 2000, TotalTokens: 2000})
	core.OnRequestStart()
	if snap := core.snapshot(); snap.liveTotal != 0 {
		t.Fatalf("OnRequestStart should clear live usage, got %d", snap.liveTotal)
	}
}

func TestPlanStripNeverShowsBareHeader(t *testing.T) {
	core := NewHeadlessTUI("p", "m", false).core

	// a plan tool ERROR must not become the board...
	core.OnToolResult("plan", "index 5 out of range (3 items)", true, time.Millisecond)
	if txt := core.planBoardText(); txt != "" {
		t.Fatalf("error output leaked into plan strip:\n%s", txt)
	}

	// ...and non-board text already stored renders nothing (header suppressed)
	core.diffMu.Lock()
	core.planBoard = "status must be pending|in_progress|done"
	core.diffMu.Unlock()
	if txt := core.planBoardText(); txt != "" {
		t.Fatalf("bare header shown for unparseable board:\n%s", txt)
	}

	// real boards still render header + rows
	core.diffMu.Lock()
	core.planBoard = "plan set:\n1. [ ] scaffold\n2. [~] theme"
	core.diffMu.Unlock()
	txt := core.planBoardText()
	if !strings.Contains(txt, "plan") || !strings.Contains(txt, "scaffold") || !strings.Contains(txt, "theme") {
		t.Fatalf("valid board lost rows:\n%s", txt)
	}
}
