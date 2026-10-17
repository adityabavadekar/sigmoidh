package sigmoidh

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	agenttools "github.com/adityabavadekar/sigmoidh/tools"
	"github.com/voocel/litellm"
)

const (
	maxAgentDepth = 2
	subMaxTurns   = 20
)

const subagentSystemPrompt = `You are a focused sub-agent spawned by Sigmoidh to complete one task autonomously.
Complete the task fully with the available terminal tools; do not ask questions back.
Keep reasoning terse. Explore only as much as the task requires, and verify changes when feasible.
End with a compact final report: what was done, key findings, and files touched.`

type subagentArgs struct {
	Prompt string `json:"prompt"`
}

// registerHostTools wires the host-instance tool implementations that need
// the LLM client or session state.
func registerHostTools(s *Sigmoidh) {
	agenttools.RegisterExternalHandler("subagent", s.runSubagent)
	agenttools.RegisterExternalHandler("trigger", s.handleTrigger)
	agenttools.RegisterExternalHandler("memory", s.handleMemory)
	agenttools.RegisterExternalHandler("plan", s.handlePlan)
	agenttools.RegisterExternalHandler("ask_user", s.handleAskUser)
	agenttools.RegisterExternalHandler("session_search", s.handleSessionSearch)
}

// runSubagent executes one child agent run and returns its final report.
func (s *Sigmoidh) runSubagent(ctx context.Context, raw json.RawMessage, workDir string) (string, bool) {
	if s.depth+1 > maxAgentDepth {
		return fmt.Sprintf("Refused: sub-agent nesting depth limit (%d) reached.", maxAgentDepth), true
	}
	var a subagentArgs
	if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Prompt) == "" {
		return "subagent requires a non-empty 'prompt' string", true
	}

	childSystem, err := generateSystemPrompt(subagentSystemPrompt, false, workDir)
	if err != nil {
		return err.Error(), true
	}

	child := &Sigmoidh{
		client:       s.client,
		model:        s.model,
		workDir:      workDir,
		system:       childSystem,
		messages:     []litellm.Message{litellm.System(childSystem)},
		observer:     subObserverFor(s),
		debug:        s.debug,
		stats:        &RunStats{},
		depth:        s.depth + 1,
		noHistory:    true,
		memory:       newMemoryStore(),
		plan:         newPlanStore(),
		scrub:        &thinkScrubber{},
		budget:       newIterationBudget(subMaxTurns),
		triggers:     []string{},
		excludeTools: map[string]bool{"subagent": true},
		providers:    s.providers,
		maxTokens:    s.maxTokens,
	}

	runErr := child.run(ctx, a.Prompt, subMaxTurns)

	s.stats.merge(child.stats)
	s.stats.SubagentRuns++

	recap := fmt.Sprintf("[sub-agent] %d steps · %d tool calls · %s tokens",
		child.stats.TotalTurns, child.stats.TotalToolCalls, humanNumber(child.stats.TotalTokens))

	out := strings.TrimSpace(child.lastText())
	if runErr != nil {
		if out == "" {
			return recap + "\n\nsub-agent failed: " + runErr.Error(), true
		}
		out += "\n\n(sub-agent stopped early: " + runErr.Error() + ")"
	}
	if out == "" {
		out = "(sub-agent produced no final text)"
	}
	return recap + "\n\n" + out, false
}

// nopObserver swallows all UI events; sub-agents render nothing themselves.
type nopObserver struct{}

func (nopObserver) OnBootstrap(string, string, string, bool)         {}
func (nopObserver) OnRequestStart()                                  {}
func (nopObserver) OnRequestDone()                                   {}
func (nopObserver) OnThinkingStart()                                 {}
func (nopObserver) OnReasoningDelta(string)                          {}
func (nopObserver) OnThinkingEnd(time.Duration)                      {}
func (nopObserver) OnAssistantTextStart()                            {}
func (nopObserver) OnAssistantDelta(string)                          {}
func (nopObserver) OnAssistantText(string)                           {}
func (nopObserver) OnToolCall(string, json.RawMessage)               {}
func (nopObserver) OnToolResult(string, string, bool, time.Duration) {}
func (nopObserver) OnProviderFallback(string, string)                {}
func (nopObserver) OnRetryWait(string, int, time.Duration)           {}
func (nopObserver) OnTriggerFired(string)                            {}
func (nopObserver) OnTurnUsage(litellm.Usage)                        {}
func (nopObserver) OnRunComplete(*RunStats)                          {}
func (nopObserver) OnPrintUsageStats(*RunStats)                      {}
func (nopObserver) SetThinkingVisible(bool)                          {}
func (nopObserver) ThinkingVisible() bool                            { return false }
func (nopObserver) SetUsageVisible(bool)                             {}
func (nopObserver) UsageVisible() bool                               { return false }

// lastText returns the concatenated assistant text of the final message.
func (s *Sigmoidh) lastText() string {
	for i := len(s.messages) - 1; i >= 0; i-- {
		m := s.messages[i]
		if m.Role != litellm.RoleAssistant {
			continue
		}
		var b strings.Builder
		for _, blk := range m.Blocks {
			if tb, ok := blk.(litellm.TextBlock); ok {
				b.WriteString(tb.Text)
			}
		}
		return b.String()
	}
	return ""
}

// filterExcluded removes host-excluded tools (sub-agents cannot spawn
// unbounded deeper agents).
func filterExcluded(tools []litellm.Tool, excl map[string]bool) []litellm.Tool {
	if len(excl) == 0 {
		return tools
	}
	out := make([]litellm.Tool, 0, len(tools))
	for _, t := range tools {
		if !excl[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

// subObserverFor resolves a nested observer for a child agent from whatever
// observer stack the parent uses (terminal, web, or composite).
func subObserverFor(s *Sigmoidh) AgentObserver {
	if sp, ok := s.observer.(interface{ SubagentObserver() AgentObserver }); ok {
		if child := sp.SubagentObserver(); child != nil {
			return child
		}
	}
	return nopObserver{}
}
