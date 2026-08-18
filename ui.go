package main

import (
	"encoding/json"
	"github.com/voocel/litellm"
	"io"
	"os"
)

type AgentObserver interface {
	OnBootstrap(version, provider, model string, readOnly bool)
	OnTurnStart(turn int, model string, usage litellm.Usage)
	OnReasoning(text string)
	OnToolCall(name string, args json.RawMessage)
	OnToolResult(name string, out string, isError bool)
	OnAssistantText(text string)
	OnRunComplete(stats *RunStats)
	OnPrintUsageStats(stats *RunStats)
}

func NewUi(w io.Writer, debug bool) AgentObserver {
	return NewTerminalLogger(os.Stderr, debug)
}
