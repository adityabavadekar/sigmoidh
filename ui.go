package main

import (
	"encoding/json"
	"io"
	"time"

	"github.com/voocel/litellm"
)

type AgentObserver interface {
	OnBootstrap(version, provider, model string, readOnly bool)
	OnRequestStart()
	OnRequestDone()
	OnThinkingStart()
	OnReasoningDelta(text string)
	OnThinkingEnd(took time.Duration)
	OnAssistantTextStart()
	OnAssistantDelta(text string)
	OnAssistantText(text string)
	OnToolCall(name string, args json.RawMessage)
	OnToolResult(name string, out string, isError bool, took time.Duration)
	OnProviderFallback(provider, model string)
	OnRetryWait(provider string, attempt int, delay time.Duration)
	OnTriggerFired(message string)
	OnTurnUsage(usage litellm.Usage)
	OnRunComplete(stats *RunStats)
	OnPrintUsageStats(stats *RunStats)

	SetThinkingVisible(visible bool)
	ThinkingVisible() bool
	SetUsageVisible(visible bool)
	UsageVisible() bool
}

func NewUi(w io.Writer, debug bool) AgentObserver {
	return NewTerminalLogger(w, debug)
}

var _ AgentObserver = (*TerminalLogger)(nil)

// StreamUsageObserver is optional: observers that implement it receive
// mid-stream usage events (input tokens at message_start, cumulative output
// while generating) so token counters can tick live instead of only after a
// turn completes.
type StreamUsageObserver interface {
	OnStreamUsage(usage litellm.Usage)
}

// notifyStreamUsage fans one mid-stream usage event out to the observer when
// it opts in; plain observers simply never see partial usage.
func notifyStreamUsage(o AgentObserver, usage litellm.Usage) {
	if so, ok := o.(StreamUsageObserver); ok {
		so.OnStreamUsage(usage)
		return
	}
	if mo, ok := o.(*multiObserver); ok {
		mo.each(func(sub AgentObserver) { notifyStreamUsage(sub, usage) })
	}
}
