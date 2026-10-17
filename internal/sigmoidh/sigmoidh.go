package sigmoidh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	agenttools "github.com/adityabavadekar/sigmoidh/tools"
	"github.com/voocel/litellm"
)

const (
	// Anthropic requires max_tokens; this is the practical ceiling for
	// frontier models (sonnet-4.x class). Thinking counts against it.
	defaultAnthropicMaxTokens = 64000
	maxTokens                 = 64000
	maxParallelTools          = 8
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
	backgroundCtx   context.Context
	backgroundEnd   context.CancelFunc
	agentEvents     chan string
	client          *litellm.Client
	model           string
	workDir         string
	system          string
	messages        []litellm.Message
	historyFile     *os.File
	histPath        string
	title           string
	lastTexts       []string
	observer        AgentObserver
	runMu           sync.Mutex
	eventWorkerOnce sync.Once
	stateMu         sync.RWMutex
	debug           bool
	stats           *RunStats
	depth           int
	noHistory       bool
	excludeTools    map[string]bool

	providers          []providerAlt // [0] = primary, rest are failovers
	providerIndex      int
	attemptsOnProvider int
	streamTainted      bool     // true once any delta reached the UI this attempt
	triggers           []string // pending run_complete notification messages
	memory             *memoryStore
	pendingAsk         *string // set by ask_user; ends the turn after tools
	away               bool
	caveman            bool
	plan               *planStore
	tasks              *taskScheduler
	scrub              *thinkScrubber
	budget             *iterationBudget
	cleanAnswer        strings.Builder
	maxTurns           int // per-run turn cap; 0 = unlimited (loop guards + cancel still apply)
	maxTokens          int // per-turn output tokens cap; 0 = auto
	contextWindow      int // model context size for compaction; 0 = defaultContextWindow
	lastPromptTokens   int // provider-reported prompt size of the last turn
	pendingNotes       []string
	noThinking         map[string]bool // models that rejected the thinking option
}

// AddNote queues a short note for the model, delivered with the next user
// message (e.g. "the user reverted your changes with /undo").
func (s *Sigmoidh) AddNote(note string) {
	s.stateMu.Lock()
	s.pendingNotes = append(s.pendingNotes, note)
	s.stateMu.Unlock()
}

type historyEntry struct {
	Timestamp string            `json:"ts"`
	Title     string            `json:"title,omitempty"`
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
	backgroundCtx, backgroundEnd := context.WithCancel(context.Background())
	s := &Sigmoidh{
		backgroundCtx: backgroundCtx,
		backgroundEnd: backgroundEnd,
		agentEvents:   make(chan string, 64),
		client:        client,
		model:         model,
		workDir:       workDir,
		system:        system,
		messages:      []litellm.Message{litellm.System(system)},
		observer:      observer,
		debug:         *debug,
		stats:         &RunStats{},
		memory:        newMemoryStore(),
		plan:          newPlanStore(),
		tasks:         newTaskScheduler(),
		scrub:         &thinkScrubber{},
		budget:        newIterationBudget(0),
	}
	agenttools.SetReadOnlyMode(*readOnlyMode)
	registerHostTools(s)
	agenttools.SetExecSessionDoneHandler(func(result agenttools.ExecSessionResult) {
		if s.backgroundCtx.Err() != nil {
			return
		}
		output := strings.TrimSpace(result.Output)
		if output == "" {
			output = "(no output)"
		} else if runes := []rune(output); len(runes) > 4000 {
			output = string(runes[:4000]) + "\n...[truncated]"
		}
		exit := "exit status 0"
		if result.Err != nil {
			exit = result.Err.Error()
		}
		event := fmt.Sprintf("Background command session %d completed.\nExit: %s\nOutput:\n%s\nReview the result and continue any unfinished part of the user's task. Do not repeat the command unchanged if it failed.", result.ID, exit, output)
		// An exit with no output and no error has nothing for the model to
		// review; starting a whole agent turn for it wastes a run. Deliver it
		// as a note and a ping instead, and hand full turns only to
		// completions that carry output or an error.
		if result.Err == nil && strings.TrimSpace(result.Output) == "" {
			s.AddNote(event)
			s.observer.OnTriggerFired(fmt.Sprintf("Background command session %d completed (exit status 0, no output).", result.ID))
			notifyFn("Sigmoidh command finished", fmt.Sprintf("Background command session %d completed (exit status 0, no output).", result.ID))
			return
		}
		if s.IsRunning() {
			s.deliverAgentEvent(event)
			return
		}
		message := fmt.Sprintf("Background command session %d completed (exit: %s). The output was queued as a note for the next turn; check status or poll the session for the full output.", result.ID, exit)
		s.AddNote(event)
		notifyFn("Sigmoidh command finished", message)
		s.observer.OnTriggerFired(message)
	})

	return s
}

// SetFallbacks installs backup providers used when the primary provider fails
// with a retryable, non-tainted error.
func (s *Sigmoidh) SetProviders(primary providerAlt, alts []providerAlt) {
	for i := range alts {
		if alts[i].sameModel { // another account: keep the model the user chose
			alts[i].Model = primary.Model
		}
	}
	s.providers = append([]providerAlt{primary}, alts...)
}

func (s *Sigmoidh) Close() {
	if s.backgroundEnd != nil {
		s.backgroundEnd()
	}
	if s.tasks != nil {
		s.tasks.Close()
	}
	if s.historyFile != nil {
		s.historyFile.Close()
	}
}

// CurrentSessionName returns the base name of the transcript this session is
// writing to, or "" before the first message is recorded.
func (s *Sigmoidh) CurrentSessionName() string {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.histPath == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(s.histPath), ".jsonl")
}

// CurrentSessionTitle returns the title generated from the first user prompt.
func (s *Sigmoidh) CurrentSessionTitle() string {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.title
}

func (s *Sigmoidh) setSessionTitle(title string) {
	s.stateMu.Lock()
	s.title = title
	s.stateMu.Unlock()
}

func (s *Sigmoidh) IsAway() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.away
}

func (s *Sigmoidh) SetAway(away bool) {
	s.stateMu.Lock()
	s.away = away
	s.stateMu.Unlock()
}

func (s *Sigmoidh) IsRunning() bool {
	if !s.runMu.TryLock() {
		return true
	}
	s.runMu.Unlock()
	return false
}

func (s *Sigmoidh) currentHistoryPath() string {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.histPath
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
		s.stateMu.Lock()
		s.histPath = fname
		s.stateMu.Unlock()
		// Let the agent re-read its own transcript: the path is under the
		// protected home dir, so it must be explicitly allowed.
		agenttools.AllowPath(fname)
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
		logError("history write failed", "path", s.currentHistoryPath(), "err", err)
	}
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "."
}

const maxIdenticalCalls = 3

// promptSize is the full prompt size of a turn. Anthropic reports cached
// tokens separately from input; OpenAI-style providers count them inside
// input already, so adding them there would double-count.
func promptSize(u litellm.Usage) int {
	cached := u.CacheReadTokens + u.CacheWriteTokens
	if cached > u.InputTokens {
		return u.InputTokens + cached
	}
	return u.InputTokens
}

// firstLine returns s's first line, cut to n runes.
func firstLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// callSignature fingerprints a whole turn's tool calls.
func callSignature(calls []litellm.ToolUseBlock) string {
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		parts = append(parts, c.Name+":"+string(c.Arguments))
	}
	return strings.Join(parts, "|")
}

const changedResultAdvisory = 4

// resultSignature fingerprints a whole turn's tool outcomes, trimmed per
// result so huge outputs don't dominate the comparison.
func resultSignature(outcomes []toolOutcome) string {
	var b strings.Builder
	for _, o := range outcomes {
		if o.isErr {
			b.WriteString("E|")
		} else {
			b.WriteString("O|")
		}
		r := []rune(strings.TrimSpace(o.out))
		if len(r) > 300 {
			r = r[:300]
		}
		b.WriteRune(rune(0x1f)) // unit separator between results
		b.WriteString(string(r))
	}
	return b.String()
}

func outcomesAllErrors(outcomes []toolOutcome) bool {
	for _, o := range outcomes {
		if !o.isErr {
			return false
		}
	}
	return true
}

func truncateSig(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

// SetMaxTurns caps agent turns per run; n <= 0 (the default) means unlimited.
func (s *Sigmoidh) SetMaxTurns(n int) { s.maxTurns = n }

// SetMaxTokens caps model completion tokens per turn; n <= 0 (the default)
// enables provider-specific auto behavior (omitted for OpenRouter/OpenAI,
// 64k for Anthropic).
func (s *Sigmoidh) SetMaxTokens(n int) { s.maxTokens = n }

// currentProviderName returns the provider key (e.g. "openrouter", "anthropic", "openai").
func (s *Sigmoidh) currentProviderName() string {
	if len(s.providers) > 0 && s.providerIndex < len(s.providers) && s.providers[s.providerIndex].Name != "" {
		return s.providers[s.providerIndex].Name
	}
	if s.client != nil {
		return s.client.ProviderName()
	}
	return ""
}

// effectiveMaxTokens determines the max_tokens limit for a request.
// Anthropic strictly requires max_tokens (defaults to 64000).
// OpenRouter and OpenAI omit max_tokens by default (nil) to avoid reserving
// an overly large output buffer against the model's total context window.
func (s *Sigmoidh) effectiveMaxTokens() *int {
	if s.maxTokens > 0 {
		return litellm.IntPtr(s.maxTokens)
	}
	prov := strings.ToLower(s.currentProviderName())
	if strings.HasPrefix(prov, "anthropic") { // also "anthropic (account 2)"
		return litellm.IntPtr(defaultAnthropicMaxTokens)
	}
	return nil
}

// isThinkingUnsupported recognizes a provider/client refusing the thinking
// (reasoning) option for the current model.
func isThinkingUnsupported(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "thinking is only supported") ||
		(strings.Contains(msg, "reasoning") && (strings.Contains(msg, "not supported") || strings.Contains(msg, "unsupported"))) ||
		strings.Contains(msg, "thinking is not supported")
}

// isOutputTokenLimitError identifies errors where requested output tokens
// caused context length rejection (e.g. OpenRouter context length error).
func isOutputTokenLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return (strings.Contains(msg, "output") || strings.Contains(msg, "max_tokens") || strings.Contains(msg, "completion tokens")) &&
		(strings.Contains(msg, "maximum context length") || strings.Contains(msg, "context_length_exceeded") || strings.Contains(msg, "reduce the length"))
}

func (s *Sigmoidh) Run(ctx context.Context, input string) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.run(ctx, input, s.maxTurns)
}

func (s *Sigmoidh) runScheduledTask(input string) {
	s.executeScheduledTask(input, true)
}

func (s *Sigmoidh) runCronTask(input string) {
	s.executeScheduledTask(input, false)
}

func (s *Sigmoidh) deliverAgentEvent(event string) bool {
	if s.agentEvents == nil {
		return false
	}
	s.eventWorkerOnce.Do(func() { go s.processAgentEvents() })
	ctx := s.backgroundCtx
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case s.agentEvents <- strings.TrimSpace(event):
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Sigmoidh) processAgentEvents() {
	ctx := s.backgroundCtx
	if ctx == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-s.agentEvents:
			if strings.TrimSpace(event) != "" {
				s.processAgentEvent(ctx, event)
			}
		}
	}
}

func (s *Sigmoidh) processAgentEvent(ctx context.Context, event string) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	prompt := "Background event: " + event
	notifyUserInput(s.observer, prompt)
	s.observer.OnTriggerFired("Resuming the agent to process a background command completion.")
	err := s.run(ctx, prompt, s.maxTurns)
	s.OnRunComplete()
	s.fireRunCompleteTriggers()
	if err != nil {
		s.observer.OnTriggerFired("Background event follow-up failed: " + err.Error())
		return
	}
}

func (s *Sigmoidh) executeScheduledTask(input string, wait bool) {
	if wait {
		s.runMu.Lock()
	} else if !s.runMu.TryLock() {
		s.observer.OnTriggerFired("Scheduled task skipped because another run is still active: " + input)
		return
	}
	defer s.runMu.Unlock()
	s.observer.OnTriggerFired("Starting scheduled task: " + input)
	notifyUserInput(s.observer, input)
	ctx := s.backgroundCtx
	if ctx == nil {
		ctx = context.Background()
	}
	err := s.run(ctx, input, s.maxTurns)
	s.OnRunComplete()
	s.fireRunCompleteTriggers()
	if err != nil {
		s.observer.OnTriggerFired("Scheduled task failed: " + err.Error())
		return
	}
	notifyFn("Sigmoidh scheduled task finished", input)
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
	logInfo("run start", "input_chars", len(input), "turns_max", turnLimitLabel(maxTurns))
	defer func() { logInfo("run end", "err", errString(err), "took_ms", time.Since(runStart).Milliseconds()) }()

	if s.depth == 0 {
		// one checkpoint per user prompt: /undo reverts a whole run
		agenttools.BeginCheckpoint(firstLine(input, 60))
	}
	if s.CurrentSessionTitle() == "" {
		s.setSessionTitle(sessionTitle(input))
	}
	if directives := s.modeDirectives(); directives != "" {
		input = directives + "\n\n" + input
	}
	s.stateMu.Lock()
	if len(s.pendingNotes) > 0 {
		input = strings.Join(s.pendingNotes, "\n") + "\n\n" + input
		s.pendingNotes = nil
	}
	s.stateMu.Unlock()
	s.messages = append(s.messages, litellm.UserText(input))
	s.writeHistory(historyEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Title:     s.CurrentSessionTitle(),
		Role:      "user",
		Content:   input,
	})
	s.pendingAsk = nil

	retriedEmpty := false
	compactedForLimit := false
	var lastCallSig string
	identicalStreak := 0
	var lastResultSig string
	changedResultStreak := 0
	failedCalls := map[string]int{} // tool name+args → failures this run

	// No hard turn cap by default: a long build is legitimate work, and the
	// degenerate-loop guard below plus esc/ctrl+C handle runaway agents.
	for turn := 0; maxTurns <= 0 || turn < maxTurns; turn++ {
		s.maybeCompact(ctx)
		resp, streamErr := s.streamTurn(ctx)
		if streamErr != nil && isContextLengthError(streamErr) && !compactedForLimit {
			// the real window is smaller than configured: compact and retry
			compactedForLimit = true
			if _, _, cerr := s.Compact(ctx); cerr == nil {
				turn--
				continue
			}
		}
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
		s.lastPromptTokens = promptSize(usage) + usage.OutputTokens // the output is in the next prompt

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
				if len(call.Arguments) > 0 && !json.Valid(call.Arguments) {
					outcomes[i] = toolOutcome{out: fmt.Sprintf("the arguments of this %s call were not valid JSON (probably cut off). Nothing ran; send the call again with complete JSON arguments.", call.Name), isErr: true}
					return
				}
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

		// a call that keeps failing unchanged is a dead end: say so, so the
		// model changes approach instead of burning turns on retries
		for i, call := range calls {
			if !outcomes[i].isErr {
				continue
			}
			sig := call.Name + ":" + string(call.Arguments)
			failedCalls[sig]++
			if n := failedCalls[sig]; n >= 2 {
				outcomes[i].out += fmt.Sprintf("\n\n[note: this exact call has now failed %d times. Don't retry it unchanged: change the arguments or try a different approach.]", n)
			}
		}

		if len(calls) > 0 {
			callSig := callSignature(calls)
			resultSig := resultSignature(outcomes)
			if callSig == lastCallSig {
				if resultSig == lastResultSig || outcomesAllErrors(outcomes) {
					identicalStreak++
					changedResultStreak = 0
				} else {
					identicalStreak = 1
					changedResultStreak++
				}
			} else {
				identicalStreak, changedResultStreak = 1, 0
			}
			lastCallSig, lastResultSig = callSig, resultSig
			if changedResultStreak >= changedResultAdvisory && changedResultStreak%changedResultAdvisory == 0 {
				outcomes[0].out += fmt.Sprintf("\n\n[note: this exact call repeated %d times with changing results. Waiting on background exec? Notified on exit — stop repeating. If not progressing, change approach.]", changedResultStreak)
			}
		} else {
			lastCallSig, lastResultSig = "", ""
			identicalStreak, changedResultStreak = 0, 0
		}

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

		// Loop guard: the model repeating the byte-identical tool call AND
		// getting the same output back is a degenerate state — stop it with
		// feedback instead of burning turns. Polling calls whose results
		// genuinely change (e.g. status timestamps) are not loops.
		if identicalStreak >= maxIdenticalCalls {
			return fmt.Errorf("agent repeated an identical tool call %d times with identical results (possible loop); last: %s %s",
				identicalStreak, calls[0].Name, truncateSig(string(calls[0].Arguments)))
		}

		if len(calls) == 0 {
			if reason := degenerateRepetition(textResp); reason != "" {
				return fmt.Errorf("model output degenerated into repetition (%s); stopping the run", reason)
			}
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

	return fmt.Errorf("stopped after %d turns (raise with -max-turns, or 0 for unlimited)", maxTurns)
}

// turnLimitLabel renders the configured cap for logs.
func turnLimitLabel(n int) string {
	if n <= 0 {
		return "unlimited"
	}
	return strconv.Itoa(n)
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

	maxTok := s.effectiveMaxTokens()
	request := func(maxTok *int) litellm.Request {
		req := litellm.Request{
			Model:     s.model,
			Messages:  sanitizeForRequest(s.messages),
			Tools:     filterExcluded(agenttools.GetAvailableTools(), s.excludeTools),
			MaxTokens: maxTok,
		}
		if !s.noThinking[s.model] {
			req.Thinking = &litellm.Thinking{Mode: litellm.ThinkingEnabled}
		}
		return req
	}
	stream, err := s.client.Stream(ctx, request(maxTok))
	if err != nil && isThinkingUnsupported(err) && !s.noThinking[s.model] {
		// models that cannot reason reject the option: drop it for this
		// model from now on instead of failing every request
		logInfo("model does not support thinking; disabling it", "model", s.model)
		if s.noThinking == nil {
			s.noThinking = map[string]bool{}
		}
		s.noThinking[s.model] = true
		stream, err = s.client.Stream(ctx, request(maxTok))
	}
	if err != nil {
		if maxTok != nil && isOutputTokenLimitError(err) {
			logWarn("output token limit exceeded context length, retrying without max_tokens", "err", err)
			stream, err = s.client.Stream(ctx, request(nil))
		}
		if err != nil {
			return nil, false, err
		}
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

// repairToolArgs replaces invalid JSON tool arguments (a model cut off
// mid-call) with {}: clients reject a history containing them, which made
// every later request in the session fail, not just the bad call.
func repairToolArgs(m litellm.Message) litellm.Message {
	if m.Role != litellm.RoleAssistant {
		return m
	}
	var blocks []litellm.Block
	for i, blk := range m.Blocks {
		tu, ok := blk.(litellm.ToolUseBlock)
		if !ok || len(tu.Arguments) == 0 || json.Valid(tu.Arguments) {
			continue
		}
		if blocks == nil {
			blocks = append([]litellm.Block{}, m.Blocks...)
		}
		tu.Arguments = json.RawMessage(`{}`)
		blocks[i] = tu
	}
	if blocks != nil {
		m.Blocks = blocks
	}
	return m
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
		m = repairToolArgs(m)
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
