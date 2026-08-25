package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	agenttools "github.com/adityabavadekar/sigmoidh/tools"
	"github.com/voocel/litellm"
)

const (
	maxTurns = 30
	// Anthropic requires max_tokens; this is the practical ceiling for
	// frontier models (sonnet-4.x class). Thinking counts against it.
	maxTokens        = 64000
	maxParallelTools = 8
)

type toolOutcome struct {
	out   string
	isErr bool
	took  time.Duration
}

type RunStats struct {
	TotalTurns            int
	TotalToolCalls        int
	TotalInputTokens      int
	TotalOutputTokens     int
	TotalTokens           int
	TotalReasoningTokens  int
	TotalCacheReadTokens  int
	TotalCacheWriteTokens int
	TotalCachedTokens     int
	TotalDuration         time.Duration
	SubagentRuns          int
}

func (st *RunStats) merge(o *RunStats) {
	if st == nil || o == nil {
		return
	}
	st.TotalTurns += o.TotalTurns
	st.TotalToolCalls += o.TotalToolCalls
	st.TotalInputTokens += o.TotalInputTokens
	st.TotalOutputTokens += o.TotalOutputTokens
	st.TotalTokens += o.TotalTokens
	st.TotalReasoningTokens += o.TotalReasoningTokens
	st.TotalCacheReadTokens += o.TotalCacheReadTokens
	st.TotalCacheWriteTokens += o.TotalCacheWriteTokens
	st.TotalCachedTokens += o.TotalCachedTokens
	st.TotalDuration += o.TotalDuration
}

type Sigmoidh struct {
	client       *litellm.Client
	model        string
	workDir      string
	system       string
	messages     []litellm.Message
	historyFile  *os.File
	histPath     string
	lastTexts    []string
	observer     AgentObserver
	debug        bool
	stats        *RunStats
	depth        int
	noHistory    bool
	excludeTools map[string]bool

	providers          []providerAlt // [0] = primary, rest are failovers
	providerIndex      int
	attemptsOnProvider int
	streamTainted      bool     // true once any delta reached the UI this attempt
	triggers           []string // pending run_complete notification messages
	memory             *memoryStore
	pendingAsk         *string // set by ask_user; ends the turn after tools
	plan               *planStore
	scrub              *thinkScrubber
	budget             *iterationBudget
	cleanAnswer        strings.Builder
}

type historyEntry struct {
	Timestamp string            `json:"ts"`
	Role      string            `json:"role"`
	Content   string            `json:"content,omitempty"`
	Reasoning string            `json:"reasoning,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	ToolCalls []historyToolCall `json:"tool_calls,omitempty"`
	Results   []historyResult   `json:"results,omitempty"`
}

type historyToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type historyResult struct {
	ToolUseID string `json:"tool_use_id"`
	Output    string `json:"output"`
	IsError   bool   `json:"is_error"`
}

func (s *Sigmoidh) AppDir() string {
	return filepath.Join(homeDir(), ".sigmoidh")
}

func (s *Sigmoidh) HistoryFilePath() string {
	histDir := filepath.Join(s.AppDir(), "history")
	os.MkdirAll(histDir, 0o755)
	fname := filepath.Join(histDir, time.Now().Format("20060102-150405")+".jsonl")
	return fname
}

func (s *Sigmoidh) ReplHistoryFilePath() string {
	histDir := filepath.Join(s.AppDir(), "repl_history")
	os.MkdirAll(histDir, 0o755)
	fname := filepath.Join(histDir, ".repl_history")
	return fname
}

func NewSigmoidh(client *litellm.Client, model, system, workDir string, observer AgentObserver, readOnlyMode *bool, debug *bool) *Sigmoidh {
	s := &Sigmoidh{
		client:   client,
		model:    model,
		workDir:  workDir,
		system:   system,
		messages: []litellm.Message{litellm.System(system)},
		observer: observer,
		debug:    *debug,
		stats:    &RunStats{},
		memory:   newMemoryStore(),
		plan:     newPlanStore(),
		scrub:    &thinkScrubber{},
		budget:   newIterationBudget(maxTurns),
	}
	agenttools.SetReadOnlyMode(*readOnlyMode)
	registerHostTools(s)

	return s
}

// SetFallbacks installs backup providers used when the primary provider fails
// with a retryable, non-tainted error.
func (s *Sigmoidh) SetProviders(primary providerAlt, alts []providerAlt) {
	s.providers = append([]providerAlt{primary}, alts...)
}

func (s *Sigmoidh) Close() {
	if s.historyFile != nil {
		s.historyFile.Close()
	}
}

// CurrentSessionName returns the base name of the transcript this session is
// writing to, or "" before the first message is recorded.
func (s *Sigmoidh) CurrentSessionName() string {
	if s.histPath == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(s.histPath), ".jsonl")
}

func (s *Sigmoidh) PrintStats() {
	s.observer.OnPrintUsageStats(s.stats)
}

func (s *Sigmoidh) OnRunComplete() {
	s.observer.OnRunComplete(s.stats)
}

// writeHistory appends an entry to the session transcript. The file is
// created lazily on first write so that e.g. `sigmoidh -continue` never sees
// its own just-created empty session as the latest one. The system prompt is
// seeded automatically when the file is created.
func (s *Sigmoidh) writeHistory(entry historyEntry) {
	if s.noHistory {
		return
	}
	if s.historyFile == nil {
		fname := s.HistoryFilePath()
		f, err := os.OpenFile(fname, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		s.historyFile = f
		s.histPath = fname
		if entry.Role != "system" && s.system != "" {
			s.writeRawHistory(historyEntry{
				Timestamp: time.Now().Format(time.RFC3339),
				Role:      "system",
				Content:   s.system,
			})
		}
	}
	if entry.Cwd == "" {
		entry.Cwd = s.workDir
	}
	s.writeRawHistory(entry)
}

func (s *Sigmoidh) writeRawHistory(entry historyEntry) {
	data, err := json.Marshal(entry)
	if err != nil {
		logError("history marshal failed", "role", entry.Role, "err", err)
		return
	}
	if _, err := s.historyFile.Write(append(data, '\n')); err != nil {
		logError("history write failed", "path", s.histPath, "err", err)
	}
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "."
}

const maxIdenticalCalls = 3

// callSignature fingerprints a whole turn's tool calls.
func callSignature(calls []litellm.ToolUseBlock) string {
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		parts = append(parts, c.Name+":"+string(c.Arguments))
	}
	return strings.Join(parts, "|")
}

func truncateSig(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

func (s *Sigmoidh) Run(ctx context.Context, input string) error {
	return s.run(ctx, input, maxTurns)
}

// runToolSafe executes one tool call, converting a panic inside the tool (or
// anything it spawns) into an error result instead of taking the process down:
// tool goroutines are outside the reach of the TUI's recover.
func (s *Sigmoidh) runToolSafe(ctx context.Context, name string, args json.RawMessage) (out string, isErr bool, panicErr error) {
	defer func() {
		if r := recover(); r != nil {
			logPanic("tool:"+name, r, debugStack())
			panicErr = fmt.Errorf("%v", r)
		}
	}()
	out, isErr = agenttools.RunTool(ctx, name, args, s.workDir)
	return out, isErr, nil
}

// debugStack captures the current goroutine stack for panic logs.
func debugStack() []byte {
	buf := make([]byte, 8192)
	n := runtime.Stack(buf, false)
	return buf[:n]
}

func (s *Sigmoidh) run(ctx context.Context, input string, maxTurns int) (err error) {
	runStart := time.Now()
	defer func() { s.stats.TotalDuration += time.Since(runStart) }()
	defer func() {
		if r := recover(); r != nil {
			logPanic("run", r, debugStack())
			panic(r)
		}
	}()
	logInfo("run start", "input_chars", len(input), "turns_max", maxTurns)
	defer func() { logInfo("run end", "err", errString(err), "took_ms", time.Since(runStart).Milliseconds()) }()

	s.messages = append(s.messages, litellm.UserText(input))
	s.writeHistory(historyEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Role:      "user",
		Content:   input,
	})

	retriedEmpty := false
	var lastCallSig string
	identicalStreak := 0

	for turn := 0; turn < maxTurns; turn++ {
		resp, streamErr := s.streamTurn(ctx)
		if streamErr != nil {
			logWarn("run ended with stream error", "turn", turn+1, "err", streamErr)
			return streamErr
		}

		s.messages = append(s.messages, litellm.Assistant(resp.Blocks...))

		usage := resp.Usage
		s.stats.TotalInputTokens += usage.InputTokens
		s.stats.TotalOutputTokens += usage.OutputTokens
		s.stats.TotalTokens += usage.TotalTokens
		s.stats.TotalReasoningTokens += usage.ReasoningTokens
		s.stats.TotalCacheReadTokens += usage.CacheReadTokens
		s.stats.TotalCacheWriteTokens += usage.CacheWriteTokens
		s.stats.TotalCachedTokens += usage.CacheReadTokens + usage.CacheWriteTokens
		s.stats.TotalTurns++

		textResp := resp.Text()
		calls := resp.ToolCalls()

		var reasoning strings.Builder
		for _, blk := range resp.Blocks {
			if rb, ok := blk.(litellm.ReasoningBlock); ok {
				reasoning.WriteString(rb.Text)
			}
		}
		entry := historyEntry{
			Timestamp: time.Now().Format(time.RFC3339),
			Role:      "assistant",
			Content:   textResp,
			Reasoning: reasoning.String(),
		}
		for _, c := range calls {
			entry.ToolCalls = append(entry.ToolCalls, historyToolCall{
				ID: c.ID, Name: c.Name, Args: c.Arguments,
			})
		}
		s.writeHistory(entry)
		s.stats.TotalToolCalls += len(calls)
		logInfo("turn complete", "turn", s.stats.TotalTurns, "tools", len(calls),
			"in", usage.InputTokens, "out", usage.OutputTokens, "reasoning", usage.ReasoningTokens,
			"finish", finishLabel(resp))

		results := make([]litellm.Block, len(calls))
		resultEntries := make([]historyResult, len(calls))

		for _, call := range calls {
			s.observer.OnToolCall(call.Name, call.Arguments)
		}

		outcomes := make([]toolOutcome, len(calls))
		var wg sync.WaitGroup
		sem := make(chan struct{}, maxParallelTools)
		for i, call := range calls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				start := time.Now()
				out, isErr, perr := s.runToolSafe(ctx, call.Name, call.Arguments)
				if perr != nil {
					logError("tool panicked", "tool", call.Name, "err", perr)
					out, isErr = fmt.Sprintf("tool %q crashed internally: %v", call.Name, perr), true
				}
				if out == "" && !isErr {
					out = "(no output)"
				}
				outcomes[i] = toolOutcome{out: out, isErr: isErr, took: time.Since(start)}
			}()
		}
		wg.Wait()

		for i, call := range calls {
			s.observer.OnToolResult(call.Name, outcomes[i].out, outcomes[i].isErr, outcomes[i].took)
			logInfo("tool result", "tool", call.Name, "is_err", outcomes[i].isErr,
				"took_ms", outcomes[i].took.Milliseconds(), "out_chars", len(outcomes[i].out))
			results[i] = litellm.ToolResultBlock{
				ToolUseID: call.ID,
				Content:   []litellm.Block{litellm.Text(outcomes[i].out)},
				IsError:   outcomes[i].isErr,
			}
			resultEntries[i] = historyResult{
				ToolUseID: call.ID,
				Output:    outcomes[i].out,
				IsError:   outcomes[i].isErr,
			}
		}
		s.messages = append(s.messages, litellm.Message{Role: litellm.RoleTool, Blocks: results})
		s.writeHistory(historyEntry{
			Timestamp: time.Now().Format(time.RFC3339),
			Role:      "tool",
			Results:   resultEntries,
		})
		s.observer.OnTurnUsage(usage)

		if s.pendingAsk != nil {
			if textResp != "" {
				s.lastTexts = append(s.lastTexts, textResp)
			}
			return nil // user answers via their next message
		}

		// Loop guard: the model repeating the byte-identical tool call is a
		// degenerate state — stop it with feedback instead of burning turns.
		if len(calls) > 0 {
			sig := callSignature(calls)
			if sig == lastCallSig {
				identicalStreak++
			} else {
				identicalStreak = 1
				lastCallSig = sig
			}
			if identicalStreak >= maxIdenticalCalls {
				return fmt.Errorf("agent repeated an identical tool call %d times (possible loop); last: %s %s",
					identicalStreak, calls[0].Name, truncateSig(string(calls[0].Arguments)))
			}
		} else {
			lastCallSig = ""
			identicalStreak = 0
		}

		if len(calls) == 0 {
			if textResp == "" {
				if !retriedEmpty {
					retriedEmpty = true
					s.messages = s.messages[:len(s.messages)-1]
					s.budget.Refund()
					continue
				}
				return fmt.Errorf("model returned an empty response (finish_reason=%s)", finishLabel(resp))
			}
			if textResp != "" {
				s.lastTexts = append(s.lastTexts, textResp)
			}
			return nil
		}
	}

	return fmt.Errorf("stopped after %d turns", maxTurns)
}

// errString renders a nil-safe error for logs.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

const (
	retriesPerProvider = 3
	retryBaseDelay     = 1 * time.Second
	retryMaxDelay      = 8 * time.Second
)

// streamTurn issues one streaming request and dispatches reasoning/text deltas
// to the observer. Transient failures retry the SAME provider with exponential
// backoff; once a provider's retry budget is spent, the next configured
// provider takes over. Nothing is retried after UI output started, so
// responses can never duplicate.
func (s *Sigmoidh) streamTurn(ctx context.Context) (*litellm.Response, error) {
	s.observer.OnRequestStart()
	defer s.observer.OnRequestDone()

	if len(s.providers) == 0 {
		s.providers = []providerAlt{{Name: "primary", Client: s.client, Model: s.model}}
	}

	for {
		resp, tainted, err := s.tryStreamTurn(ctx)
		if err == nil || tainted {
			return resp, err
		}
		if !isTransientProviderError(err) {
			return nil, err
		}
		cur := s.providers[s.providerIndex]
		if s.attemptsOnProvider < retriesPerProvider {
			s.attemptsOnProvider++
			delay := retryDelay(s.attemptsOnProvider)
			logWarn("transient provider error, retrying", "provider", cur.Name, "attempt",
				s.attemptsOnProvider, "delay_ms", delay.Milliseconds(), "err", err)
			s.observer.OnRetryWait(cur.Name, s.attemptsOnProvider, delay)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		if s.providerIndex >= len(s.providers)-1 {
			return nil, fmt.Errorf("all providers exhausted after retries: %w", err)
		}
		s.providerIndex++
		next := s.providers[s.providerIndex]
		logWarn("provider exhausted, falling back", "from", cur.Name, "to", next.Name, "model", next.Model, "last_err", err)
		s.client = next.Client
		s.model = next.Model
		s.attemptsOnProvider = 0
		s.observer.OnProviderFallback(next.Name, next.Model)
	}
}

// retryDelay returns exponential backoff (1s,2s,4s…) with jitter, capped.
func retryDelay(attempt int) time.Duration {
	d := retryBaseDelay << (attempt - 1)
	if d > retryMaxDelay {
		d = retryMaxDelay
	}
	jitter := time.Duration(rand.Int63n(int64(d/4) + 1))
	return d - jitter/2 + jitter
}

// isTransientProviderError reports whether the error is worth retrying:
// rate limits, overload, network/timeouts, and 429/5xx-class provider errors
// (including message-wrapped ones like OpenRouter upstream limits).
func isTransientProviderError(err error) bool {
	var le *litellm.LiteLLMError
	if errors.As(err, &le) {
		switch le.Type {
		case litellm.ErrorTypeRateLimit, litellm.ErrorTypeOverloaded,
			litellm.ErrorTypeNetwork, litellm.ErrorTypeTimeout:
			return true
		case litellm.ErrorTypeProvider:
			return le.StatusCode == 0 || le.StatusCode == 429 || le.StatusCode >= 500
		}
	}
	msg := strings.ToLower(err.Error())
	for _, hint := range []string{"rate-limited", "rate limit", "temporarily", "overloaded", "try again", "connection reset"} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

func (s *Sigmoidh) tryStreamTurn(ctx context.Context) (resp *litellm.Response, tainted bool, err error) {
	s.streamTainted = false
	defer func() { tainted = s.streamTainted }()

	stream, err := s.client.Stream(ctx, litellm.Request{
		Model:     s.model,
		Messages:  sanitizeForRequest(s.messages),
		Tools:     filterExcluded(agenttools.GetAvailableTools(), s.excludeTools),
		MaxTokens: litellm.IntPtr(maxTokens),
		Thinking:  &litellm.Thinking{Mode: litellm.ThinkingEnabled},
	})
	if err != nil {
		return nil, false, err
	}

	thinkingOpen := false
	var thinkingStart time.Time
	closeThinking := func() {
		if thinkingOpen {
			thinkingOpen = false
			s.observer.OnThinkingEnd(time.Since(thinkingStart))
		}
	}
	textStarted := false

	resp, herr := litellm.Handle(stream, func(event litellm.Event) error {
		switch e := event.(type) {
		case litellm.ContentDelta:
			if e.Text == "" {
				return nil
			}
			closeThinking()
			if !textStarted {
				textStarted = true
				s.observer.OnAssistantTextStart()
			}
			s.streamTainted = true
			s.observer.OnAssistantDelta(e.Text)
		case litellm.ReasoningDelta:
			if e.Text == "" {
				return nil
			}
			if !thinkingOpen {
				thinkingOpen = true
				thinkingStart = time.Now()
				s.observer.OnThinkingStart()
			}
			s.streamTainted = true
			s.observer.OnReasoningDelta(e.Text)
		case litellm.UsageEvent:
			// mid-stream usage (Anthropic: input at message_start, output
			// deltas on message_delta) drives the live token counter.
			logDebug("stream usage", "in", e.Usage.InputTokens, "out", e.Usage.OutputTokens,
				"total", e.Usage.TotalTokens)
			notifyStreamUsage(s.observer, e.Usage)
		}
		return nil
	})

	closeErr := stream.Close()
	if herr != nil {
		return nil, s.streamTainted, herr
	}
	if closeErr != nil {
		return nil, s.streamTainted, closeErr
	}

	closeThinking()

	if textStarted {
		s.observer.OnAssistantText(resp.Text())
	} else if text := resp.Text(); text != "" {
		s.observer.OnAssistantText(text)
	}

	return resp, false, nil
}

// finishLabel extracts a readable finish reason for diagnostics.
func finishLabel(resp *litellm.Response) string {
	if resp == nil {
		return "none"
	}
	if resp.FinishReasonRaw != "" {
		return resp.FinishReasonRaw
	}
	if resp.FinishReason != "" {
		return string(resp.FinishReason)
	}
	return "unknown"
}

// sanitizeForRequest strips display-only blocks (replayed reasoning) before
// hitting the provider: reasoning is transcript history, not request history.
func sanitizeForRequest(msgs []litellm.Message) []litellm.Message {
	out := make([]litellm.Message, 0, len(msgs))
	for _, m := range msgs {
		hasReasoning := false
		for _, blk := range m.Blocks {
			if _, ok := blk.(litellm.ReasoningBlock); ok {
				hasReasoning = true
				break
			}
		}
		if !hasReasoning {
			out = append(out, m)
			continue
		}
		kept := make([]litellm.Block, 0, len(m.Blocks))
		for _, blk := range m.Blocks {
			if _, ok := blk.(litellm.ReasoningBlock); !ok {
				kept = append(kept, blk)
			}
		}
		if len(kept) > 0 {
			m.Blocks = kept
			out = append(out, m)
		}
	}
	// an assistant message with no blocks is provider-invalid
	clean := out[:0]
	for _, m := range out {
		if m.Role == litellm.RoleAssistant && len(m.Blocks) == 0 {
			continue
		}
		clean = append(clean, m)
	}
	return clean
}
