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
