package sigmoidh

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/voocel/litellm"
)

func TestSubagentDepthGuard(t *testing.T) {
	s := &Sigmoidh{depth: maxAgentDepth, stats: &RunStats{}}
	out, isErr := s.runSubagent(context.Background(), json.RawMessage(`{"prompt":"hi"}`), ".")
	if !isErr {
		t.Fatal("depth limit must refuse the call")
	}
	if !strings.Contains(out, "depth limit") {
		t.Fatalf("refusal text missing: %q", out)
	}
	if s.stats.SubagentRuns != 0 {
		t.Fatal("refused run must not be counted")
	}
}

func TestSubagentArgsValidation(t *testing.T) {
	s := &Sigmoidh{depth: 0, stats: &RunStats{}}
	cases := []string{
		`{}`,
		`{"prompt":""}`,
		`{"prompt":"   "}`,
		`not json`,
	}
	for _, c := range cases {
		out, isErr := s.runSubagent(context.Background(), json.RawMessage(c), ".")
		if !isErr {
			t.Fatalf("args %q must fail", c)
		}
		if !strings.Contains(out, "prompt") {
			t.Fatalf("args %q: unexpected message %q", c, out)
		}
	}
}

func TestFilterExcludedTools(t *testing.T) {
	tools := []litellm.Tool{
		{Name: "bash"}, {Name: "subagent"}, {Name: "read"},
	}
	got := filterExcluded(tools, map[string]bool{"subagent": true})
	if len(got) != 2 || got[0].Name != "bash" || got[1].Name != "read" {
		t.Fatalf("filter wrong: %+v", got)
	}

	same := filterExcluded(tools, nil)
	if len(same) != 3 {
		t.Fatalf("nil exclusion must keep all tools")
	}
}

func TestRunStatsMerge(t *testing.T) {
	a := &RunStats{TotalTurns: 1, TotalToolCalls: 2, TotalTokens: 100,
		TotalInputTokens: 60, TotalOutputTokens: 40, TotalReasoningTokens: 5,
		TotalDuration: 2 * time.Second, SubagentRuns: 0}
	b := &RunStats{TotalTurns: 3, TotalToolCalls: 4, TotalTokens: 250,
		TotalInputTokens: 150, TotalOutputTokens: 100, TotalReasoningTokens: 25,
		TotalDuration: 8 * time.Second}

	a.merge(b)
	if a.TotalTurns != 4 || a.TotalToolCalls != 6 || a.TotalTokens != 350 ||
		a.TotalInputTokens != 210 || a.TotalOutputTokens != 140 ||
		a.TotalReasoningTokens != 30 || a.TotalDuration != 10*time.Second {
		t.Fatalf("merge wrong: %+v", a)
	}
}

func TestSummaryLineMentionsSubagents(t *testing.T) {
	buf := &bytes.Buffer{}
	l := &TerminalLogger{w: buf, outTTY: false, showThinking: true}
	l.PrintSummaryLine(&RunStats{TotalTurns: 5, TotalToolCalls: 7,
		TotalTokens: 900, TotalInputTokens: 600, TotalOutputTokens: 300,
		SubagentRuns: 1, TotalDuration: 4 * time.Second})

	if !strings.Contains(buf.String(), "1 sub-agent") {
		t.Fatalf("summary should mention sub-agent:\n%s", buf.String())
	}
}

var _ AgentObserver = nopObserver{}

func TestNopObserverSatisfiesInterfaceAndStaysQuiet(t *testing.T) {
	n := nopObserver{}
	n.OnRequestStart()
	n.OnThinkingStart()
	n.OnReasoningDelta("x")
	n.OnThinkingEnd(time.Second)
	n.OnAssistantTextStart()
	n.OnAssistantDelta("y")
	n.OnAssistantText("z")
	n.OnToolCall("bash", json.RawMessage(`{}`))
	n.OnToolResult("bash", "", false, time.Second)
	n.OnRequestDone()
	if n.ThinkingVisible() {
		t.Fatal("nop observer must report thinking hidden")
	}
}
