package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/voocel/litellm"
)

// -web: a read-only live view of the agent. The WebObserver implements
// AgentObserver purely by broadcasting events to browsers over SSE; the
// terminal observer keeps working exactly as before via multiObserver.

type webHub struct {
	mu      sync.Mutex
	subs    map[chan []byte]struct{}
	started time.Time
}

func newWebHub() *webHub {
	return &webHub{subs: map[chan []byte]struct{}{}, started: time.Now()}
}

func (h *webHub) broadcast(event map[string]any) {
	event["ts"] = time.Since(h.started).Seconds()
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	data = append(data, '\n')
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- data:
		default: // slow subscriber: drop rather than block the agent
		}
	}
}

func (h *webHub) subscribe() chan []byte {
	ch := make(chan []byte, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *webHub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subs, ch)
	close(ch)
	h.mu.Unlock()
}

func (h *webHub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, webPageHTML)
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")

		ch := h.subscribe()
		defer func() { h.unsubscribe(ch) }()

		fmt.Fprint(w, ": connected\n\n")
		fl.Flush()
		keep := time.NewTicker(15 * time.Second)
		defer keep.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-keep.C:
				fmt.Fprint(w, ": ping\n\n")
				fl.Flush()
			case data := <-ch:
				fmt.Fprintf(w, "data: %s\n\n", strings.TrimRight(string(data), "\n"))
				fl.Flush()
			}
		}
	})
	return mux
}

// WebObserver forwards agent events to browsers.
type WebObserver struct {
	hub   *webHub
	scope string // "" = main agent; "subagent" = nested run
}

func (o *WebObserver) emit(ev map[string]any) {
	if o.scope != "" {
		ev["scope"] = o.scope
	}
	o.hub.broadcast(ev)
}

// SubagentObserver derives a nested web observer tagging its events.
func (o *WebObserver) SubagentObserver() AgentObserver {
	return &WebObserver{hub: o.hub, scope: "subagent"}
}

func (o *WebObserver) OnBootstrap(version, provider, model string, readOnly bool) {
	o.emit(map[string]any{"event": "bootstrap", "version": version, "provider": provider, "model": model, "readonly": readOnly})
}
func (o *WebObserver) OnRequestStart() {}
func (o *WebObserver) OnUserInput(kind, text string) {
	o.emit(map[string]any{"event": "user_input", "kind": kind, "text": text})
}

func (o *WebObserver) OnRequestDone()   {}
func (o *WebObserver) OnThinkingStart() { o.emit(map[string]any{"event": "thinking_start"}) }
func (o *WebObserver) OnReasoningDelta(text string) {
	o.emit(map[string]any{"event": "reasoning_delta", "text": text})
}
func (o *WebObserver) OnThinkingEnd(took time.Duration) {
	o.emit(map[string]any{"event": "thinking_end", "took_ms": took.Milliseconds()})
}
func (o *WebObserver) OnAssistantTextStart() { o.emit(map[string]any{"event": "answer_start"}) }
func (o *WebObserver) OnAssistantDelta(text string) {
	o.emit(map[string]any{"event": "answer_delta", "text": text})
}
func (o *WebObserver) OnAssistantText(text string) {
	o.emit(map[string]any{"event": "answer_end", "markdown": text})
}
func (o *WebObserver) OnTriggerFired(message string) {
	o.emit(map[string]any{"event": "trigger", "message": message})
}

func (o *WebObserver) OnToolCall(name string, args json.RawMessage) {
	target, counts := toolCallSummary(name, args)
	if name == "subagent" || name == "bash" || name == "exec" {
		var a struct {
			Prompt  string `json:"prompt"`
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &a) == nil {
			if a.Prompt != "" {
				target = a.Prompt
			} else {
				target = a.Command
			}
		}
	}
	o.emit(map[string]any{"event": "tool_call", "name": name, "target": target, "counts": counts})
}

func (o *WebObserver) OnToolResult(name string, out string, isErr bool, took time.Duration) {
	o.emit(map[string]any{"event": "tool_result", "name": name, "is_error": isErr,
		"bytes": len(out), "took_ms": took.Milliseconds(), "preview": truncateRunes(out, 2000)})
}

func (o *WebObserver) OnProviderFallback(provider, model string) {
	o.emit(map[string]any{"event": "fallback", "provider": provider, "model": model})
}

func (o *WebObserver) OnRetryWait(provider string, attempt int, delay time.Duration) {
	o.emit(map[string]any{"event": "retry", "provider": provider, "attempt": attempt, "delay_ms": delay.Milliseconds()})
}

func (o *WebObserver) OnTurnUsage(usage litellm.Usage) {
	o.emit(map[string]any{"event": "usage", "total": usage.TotalTokens,
		"in": usage.InputTokens, "out": usage.OutputTokens, "reasoning": usage.ReasoningTokens})
}

func (o *WebObserver) OnRunComplete(stats *RunStats) {
	o.emit(map[string]any{"event": "done", "stats": stats})
}

func (o *WebObserver) OnPrintUsageStats(stats *RunStats) {
	o.emit(map[string]any{"event": "stats", "stats": stats})
}

func (o *WebObserver) SetThinkingVisible(bool) {}
func (o *WebObserver) ThinkingVisible() bool   { return false }
func (o *WebObserver) SetUsageVisible(bool)    {}
func (o *WebObserver) UsageVisible() bool      { return false }

var _ AgentObserver = (*WebObserver)(nil)

// multiObserver fans every event out to several observers (terminal + web).
type multiObserver struct{ observers []AgentObserver }

func (m *multiObserver) each(f func(AgentObserver)) {
	for _, o := range m.observers {
		f(o)
	}
}
func (m *multiObserver) OnBootstrap(v, p, mo string, ro bool) {
	m.each(func(o AgentObserver) { o.OnBootstrap(v, p, mo, ro) })
}
func (m *multiObserver) OnRequestStart()  { m.each(func(o AgentObserver) { o.OnRequestStart() }) }
func (m *multiObserver) OnRequestDone()   { m.each(func(o AgentObserver) { o.OnRequestDone() }) }
func (m *multiObserver) OnThinkingStart() { m.each(func(o AgentObserver) { o.OnThinkingStart() }) }
func (m *multiObserver) OnReasoningDelta(t string) {
	m.each(func(o AgentObserver) { o.OnReasoningDelta(t) })
}
func (m *multiObserver) OnThinkingEnd(d time.Duration) {
	m.each(func(o AgentObserver) { o.OnThinkingEnd(d) })
}
func (m *multiObserver) OnAssistantTextStart() {
	m.each(func(o AgentObserver) { o.OnAssistantTextStart() })
}
func (m *multiObserver) OnAssistantDelta(t string) {
	m.each(func(o AgentObserver) { o.OnAssistantDelta(t) })
}
func (m *multiObserver) OnAssistantText(t string) {
	m.each(func(o AgentObserver) { o.OnAssistantText(t) })
}
func (m *multiObserver) OnToolCall(n string, a json.RawMessage) {
	m.each(func(o AgentObserver) { o.OnToolCall(n, a) })
}
func (m *multiObserver) OnToolResult(n, out string, e bool, d time.Duration) {
	m.each(func(o AgentObserver) { o.OnToolResult(n, out, e, d) })
}
func (m *multiObserver) OnProviderFallback(p, mo string) {
	m.each(func(o AgentObserver) { o.OnProviderFallback(p, mo) })
}
func (m *multiObserver) OnRetryWait(p string, a int, d time.Duration) {
	m.each(func(o AgentObserver) { o.OnRetryWait(p, a, d) })
}
func (m *multiObserver) OnTriggerFired(msg string) {
	m.each(func(o AgentObserver) { o.OnTriggerFired(msg) })
}
func (m *multiObserver) OnTurnUsage(u litellm.Usage) {
	m.each(func(o AgentObserver) { o.OnTurnUsage(u) })
}
func (m *multiObserver) OnUserInput(kind, text string) {
	m.each(func(o AgentObserver) {
		if uo, ok := o.(interface{ OnUserInput(string, string) }); ok {
			uo.OnUserInput(kind, text)
		}
	})
}

func (m *multiObserver) OnRunComplete(s *RunStats) {
	m.each(func(o AgentObserver) { o.OnRunComplete(s) })
}
func (m *multiObserver) OnPrintUsageStats(s *RunStats) {
	m.each(func(o AgentObserver) { o.OnPrintUsageStats(s) })
}
func (m *multiObserver) SetThinkingVisible(v bool) {
	m.each(func(o AgentObserver) { o.SetThinkingVisible(v) })
}
func (m *multiObserver) ThinkingVisible() bool {
	for _, o := range m.observers {
		return o.ThinkingVisible()
	}
	return true
}
func (m *multiObserver) SetUsageVisible(v bool) {
	m.each(func(o AgentObserver) { o.SetUsageVisible(v) })
}
func (m *multiObserver) UsageVisible() bool {
	for _, o := range m.observers {
		return o.UsageVisible()
	}
	return false
}

// SubagentObserver fans out to each child's sub-observer; children that do
// not support nesting contribute nothing.
func (m *multiObserver) SubagentObserver() AgentObserver {
	var kids []AgentObserver
	for _, o := range m.observers {
		if sp, ok := o.(interface{ SubagentObserver() AgentObserver }); ok {
			if child := sp.SubagentObserver(); child != nil {
				kids = append(kids, child)
			}
		}
	}
	if len(kids) == 0 {
		return nil
	}
	return &multiObserver{observers: kids}
}

var _ AgentObserver = (*multiObserver)(nil)

// StartWeb serves the observer UI in the background; never returns nil error
// into the caller's path — listen failures are logged async like the ping.
func StartWeb(addr string) (*WebObserver, error) {
	hub := newWebHub()
	go func() {
		if err := http.ListenAndServe(addr, hub.handler()); err != nil {
			fmt.Fprintf(os.Stderr, "%s✖ web ui: %v%s\n", ansiRed, err, ansiReset)
		}
	}()
	return &WebObserver{hub: hub}, nil
}
