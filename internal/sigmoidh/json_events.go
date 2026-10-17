package sigmoidh

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/voocel/litellm"
)

// -json mode: one JSON object per line on stdout for every agent event, so
// scripts, CI and other agents can drive sigmoidh without scraping terminal
// output.
//
//	{"type":"start","version":"v0.5.0","provider":"openrouter","model":"…"}
//	{"type":"thinking","text":"…","ms":420}
//	{"type":"tool_call","name":"read","args":{"path":"main.go"}}
//	{"type":"tool_result","name":"read","output":"…","is_error":false,"ms":3}
//	{"type":"text","text":"…"}
//	{"type":"usage","input_tokens":5120,"output_tokens":88,…}
//	{"type":"done","answer":"…","turns":2,"tool_calls":1,"total_tokens":5208,"ms":3100}
//	{"type":"error","message":"…"}
//
// Other types: notice, message (ask_user questions and triggers), retry,
// fallback. Consumers should ignore types they do not know.

type jsonObserver struct {
	mu     sync.Mutex
	enc    *json.Encoder
	reason strings.Builder
}

func newJSONObserver(w io.Writer) *jsonObserver {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &jsonObserver{enc: enc}
}

func (j *jsonObserver) emit(typ string, fields map[string]any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	ev := map[string]any{"type": typ}
	for k, v := range fields {
		ev[k] = v
	}
	_ = j.enc.Encode(ev)
}

func (j *jsonObserver) OnBootstrap(version, provider, model string, readOnly bool) {
	j.emit("start", map[string]any{"version": version, "provider": provider, "model": model, "read_only": readOnly})
}
func (j *jsonObserver) OnRequestStart()  {}
func (j *jsonObserver) OnRequestDone()   {}
func (j *jsonObserver) OnThinkingStart() { j.reason.Reset() }
func (j *jsonObserver) OnReasoningDelta(text string) {
	j.reason.WriteString(text)
}
func (j *jsonObserver) OnThinkingEnd(took time.Duration) {
	if t := strings.TrimSpace(j.reason.String()); t != "" {
		j.emit("thinking", map[string]any{"text": t, "ms": took.Milliseconds()})
	}
	j.reason.Reset()
}
func (j *jsonObserver) OnAssistantTextStart()   {}
func (j *jsonObserver) OnAssistantDelta(string) {}
func (j *jsonObserver) OnAssistantText(text string) {
	if t := strings.TrimSpace(text); t != "" {
		j.emit("text", map[string]any{"text": t})
	}
}
func (j *jsonObserver) OnToolCall(name string, args json.RawMessage) {
	var a any = string(args)
	if json.Valid(args) {
		a = args
	}
	j.emit("tool_call", map[string]any{"name": name, "args": a})
}
func (j *jsonObserver) OnToolResult(name, out string, isErr bool, took time.Duration) {
	j.emit("tool_result", map[string]any{"name": name, "output": out, "is_error": isErr, "ms": took.Milliseconds()})
}
func (j *jsonObserver) OnProviderFallback(provider, model string) {
	j.emit("fallback", map[string]any{"provider": provider, "model": model})
}
func (j *jsonObserver) OnRetryWait(provider string, attempt int, delay time.Duration) {
	j.emit("retry", map[string]any{"provider": provider, "attempt": attempt, "delay_ms": delay.Milliseconds()})
}
func (j *jsonObserver) OnTriggerFired(message string) {
	j.emit("message", map[string]any{"text": message})
}
func (j *jsonObserver) OnNotice(text string) { j.emit("notice", map[string]any{"text": text}) }
func (j *jsonObserver) OnTurnUsage(u litellm.Usage) {
	j.emit("usage", map[string]any{
		"input_tokens": u.InputTokens, "output_tokens": u.OutputTokens, "reasoning_tokens": u.ReasoningTokens,
		"cache_read_tokens": u.CacheReadTokens, "cache_write_tokens": u.CacheWriteTokens,
	})
}
func (j *jsonObserver) OnRunComplete(*RunStats)     {}
func (j *jsonObserver) OnPrintUsageStats(*RunStats) {}
func (j *jsonObserver) SetThinkingVisible(bool)     {}
func (j *jsonObserver) ThinkingVisible() bool       { return true }
func (j *jsonObserver) SetUsageVisible(bool)        {}
func (j *jsonObserver) UsageVisible() bool          { return false }

func (j *jsonObserver) done(answer string, st *RunStats) {
	j.emit("done", map[string]any{
		"answer": answer, "turns": st.TotalTurns, "tool_calls": st.TotalToolCalls,
		"input_tokens": st.TotalInputTokens, "output_tokens": st.TotalOutputTokens,
		"total_tokens": st.TotalTokens, "ms": st.TotalDuration.Milliseconds(),
	})
}

func (j *jsonObserver) fail(err error) {
	j.emit("error", map[string]any{"message": err.Error()})
}

var _ AgentObserver = (*jsonObserver)(nil)
