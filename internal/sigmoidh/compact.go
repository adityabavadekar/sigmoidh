package sigmoidh

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/litellm"
)

// Context compaction: when the conversation nears the model's context
// window, the older part is replaced by a model-written summary so long
// sessions keep working instead of failing (or silently degrading) at the
// limit. The recent tail is kept verbatim; working memory and the plan live
// outside the message list and are untouched.

const (
	defaultContextWindow = 64000
	compactTriggerRatio  = 0.75 // compact once the prompt reaches this share of the window
	compactKeepRatio     = 0.30 // budget for the verbatim tail kept after compacting
	compactToolResultCap = 1200 // chars of each old tool result shown to the summarizer
)

const compactPrompt = `You are compressing the earlier part of a coding-agent session so the agent can continue it with a fresh context.
Write a dense summary the agent can act on. Include, as applicable:
- the user's goals and requirements, quoted where wording matters
- decisions made and constraints learned
- files created/changed (paths) and what changed
- commands run and their important results, errors still unresolved
- the current state and the next steps that were planned
Omit pleasantries and anything already superseded. Use short bullet points. Do not invent anything.`

// SetContextWindow sets the model context size used for compaction (tokens).
func (s *Sigmoidh) SetContextWindow(n int) { s.contextWindow = n }

func (s *Sigmoidh) window() int {
	if s.contextWindow > 0 {
		return s.contextWindow
	}
	return defaultContextWindow
}

// estimateTokens approximates prompt tokens (≈4 chars per token).
func estimateTokens(msgs []litellm.Message) int {
	chars := 0
	for _, m := range msgs {
		chars += 16 // role/framing overhead
		for _, b := range m.Blocks {
			chars += blockChars(b)
		}
	}
	return chars / 4
}

func blockChars(b litellm.Block) int {
	switch v := b.(type) {
	case litellm.TextBlock:
		return len(v.Text)
	case litellm.ToolUseBlock:
		return len(v.Name) + len(v.Arguments)
	case litellm.ToolResultBlock:
		n := 0
		for _, c := range v.Content {
			n += blockChars(c)
		}
		return n
	case litellm.ReasoningBlock:
		return 0 // stripped before requests (sanitizeForRequest)
	}
	return 0
}

// promptTokens is the best current estimate of the next request's size: the
// provider's own count from the last turn when known, else the estimate.
func (s *Sigmoidh) promptTokens() int {
	return max(estimateTokens(s.messages), s.lastPromptTokens)
}

// maybeCompact compacts when the prompt has reached the trigger size.
func (s *Sigmoidh) maybeCompact(ctx context.Context) {
	if float64(s.promptTokens()) < float64(s.window())*compactTriggerRatio {
		return
	}
	if _, _, err := s.Compact(ctx); err != nil {
		logWarn("auto compaction failed", "err", err)
	}
}

// Compact summarizes all but the recent tail of the conversation. It
// returns the estimated prompt size before and after.
func (s *Sigmoidh) Compact(ctx context.Context) (before, after int, err error) {
	before = s.promptTokens()
	cut := compactCut(s.messages, int(float64(s.window())*compactKeepRatio))
	if cut <= 1 {
		return before, before, fmt.Errorf("nothing to compact yet")
	}
	head, tail := s.messages[1:cut], s.messages[cut:]

	summary, serr := s.summarize(ctx, head)
	if serr != nil {
		logWarn("compaction summary failed, using outline", "err", serr)
		summary = outlineSummary(head)
	}
	var note strings.Builder
	note.WriteString("[Context compacted: the earlier conversation was replaced by this summary. Working memory and the plan are unchanged; use the memory/plan tools to see them.]\n\n")
	note.WriteString(summary)
	if s.plan != nil {
		if board := s.plan.render(); board != "(plan empty)" {
			note.WriteString("\n\nCurrent plan:\n" + board)
		}
	}

	msgs := []litellm.Message{s.messages[0]}
	if len(tail) > 0 && tail[0].Role == litellm.RoleUser {
		// keep strict user/assistant alternation: fold into the next user turn
		merged := litellm.Message{Role: litellm.RoleUser, Blocks: append([]litellm.Block{litellm.Text(note.String() + "\n\n---\n")}, tail[0].Blocks...)}
		msgs = append(msgs, merged)
		msgs = append(msgs, tail[1:]...)
	} else {
		msgs = append(msgs, litellm.UserText(note.String()))
		msgs = append(msgs, tail...)
	}
	s.messages = msgs
	s.lastPromptTokens = 0
	after = estimateTokens(s.messages)
	notifyNotice(s.observer, fmt.Sprintf("compacted context: ~%s → ~%s tokens (%d messages summarized)",
		humanNumber(before), humanNumber(after), len(head)))
	logInfo("context compacted", "before", before, "after", after, "summarized", len(head))
	return before, after, nil
}

// compactCut picks where the verbatim tail starts: as early as the keep
// budget allows, never on a tool-result message (it must follow its call),
// preferring a user message. Returns <= 1 when nothing can be dropped.
func compactCut(msgs []litellm.Message, keepBudget int) int {
	if len(msgs) < 4 {
		return 0
	}
	best := -1
	used := 0
	for i := len(msgs) - 1; i >= 2; i-- {
		used += estimateTokens(msgs[i : i+1])
		if used > keepBudget && best != -1 {
			break
		}
		if msgs[i].Role == litellm.RoleTool {
			continue
		}
		best = i
		if msgs[i].Role == litellm.RoleUser && used > keepBudget/2 {
			break // a clean turn boundary with a reasonable tail
		}
	}
	if best == -1 {
		// only tool results near the end: keep the final call + result pair
		for i := len(msgs) - 1; i >= 2; i-- {
			if msgs[i].Role == litellm.RoleAssistant {
				return i
			}
		}
		return 0
	}
	return best
}

// summarize asks the model for a summary of msgs.
func (s *Sigmoidh) summarize(ctx context.Context, msgs []litellm.Message) (string, error) {
	if s.client == nil {
		return "", fmt.Errorf("no client")
	}
	transcript := renderTranscript(msgs, s.window()*2) // ≈ half the window in tokens
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := s.client.Chat(ctx, litellm.Request{
		Model:     s.model,
		Messages:  []litellm.Message{litellm.System(compactPrompt), litellm.UserText(transcript)},
		MaxTokens: litellm.IntPtr(2500),
	})
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(s.scrubText(resp.Text()))
	if text == "" {
		return "", fmt.Errorf("empty summary")
	}
	return text, nil
}

// scrubText drops inline <think> blocks some models put in plain replies.
func (s *Sigmoidh) scrubText(t string) string {
	for {
		i := strings.Index(t, "<think>")
		if i < 0 {
			return t
		}
		j := strings.Index(t[i:], "</think>")
		if j < 0 {
			return t[:i]
		}
		t = t[:i] + t[i+j+len("</think>"):]
	}
}

// renderTranscript flattens messages for the summarizer, keeping the most
// recent maxChars (older content matters least).
func renderTranscript(msgs []litellm.Message, maxChars int) string {
	var parts []string
	for _, m := range msgs {
		var b strings.Builder
		for _, blk := range m.Blocks {
			switch v := blk.(type) {
			case litellm.TextBlock:
				fmt.Fprintf(&b, "%s: %s\n", strings.ToUpper(string(m.Role)), v.Text)
			case litellm.ToolUseBlock:
				fmt.Fprintf(&b, "ASSISTANT called %s %s\n", v.Name, clip(string(v.Arguments), 400))
			case litellm.ToolResultBlock:
				var out strings.Builder
				for _, c := range v.Content {
					if t, ok := c.(litellm.TextBlock); ok {
						out.WriteString(t.Text)
					}
				}
				label := "RESULT"
				if v.IsError {
					label = "ERROR"
				}
				fmt.Fprintf(&b, "%s: %s\n", label, clip(out.String(), compactToolResultCap))
			}
		}
		if b.Len() > 0 {
			parts = append(parts, b.String())
		}
	}
	text := strings.Join(parts, "")
	if len(text) > maxChars {
		text = "…(earliest part omitted)\n" + text[len(text)-maxChars:]
	}
	return text
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("…(%d more chars)", len(s)-n)
}

// outlineSummary is the no-model fallback: user requests, tools used, errors.
func outlineSummary(msgs []litellm.Message) string {
	var b strings.Builder
	b.WriteString("Outline of the earlier conversation (automatic; the model summary failed):\n")
	for _, m := range msgs {
		for _, blk := range m.Blocks {
			switch v := blk.(type) {
			case litellm.TextBlock:
				if m.Role == litellm.RoleUser {
					fmt.Fprintf(&b, "- user: %s\n", clip(strings.TrimSpace(v.Text), 300))
				}
			case litellm.ToolUseBlock:
				var args map[string]any
				_ = json.Unmarshal(v.Arguments, &args)
				target := ""
				for _, k := range []string{"path", "command", "query", "url", "op"} {
					if s, ok := args[k].(string); ok && s != "" {
						target = clip(s, 120)
						break
					}
				}
				fmt.Fprintf(&b, "- %s %s\n", v.Name, target)
			case litellm.ToolResultBlock:
				if v.IsError {
					for _, c := range v.Content {
						if t, ok := c.(litellm.TextBlock); ok {
							fmt.Fprintf(&b, "  error: %s\n", clip(strings.TrimSpace(t.Text), 200))
						}
					}
				}
			}
		}
	}
	return b.String()
}

// isContextLengthError recognizes providers' "prompt too long" rejections.
func isContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, k := range []string{"context length", "context_length_exceeded", "maximum context", "prompt is too long", "too many tokens", "context window", "reduce the length of the messages"} {
		if strings.Contains(msg, k) {
			return true
		}
	}
	return false
}

// noticeObserver is optional: observers that implement it show short
// informational notices (compaction, undo, …).
type noticeObserver interface {
	OnNotice(text string)
}

func notifyNotice(o AgentObserver, text string) {
	if o == nil {
		return
	}
	if no, ok := o.(noticeObserver); ok {
		no.OnNotice(text)
		return
	}
	if mo, ok := o.(*multiObserver); ok {
		mo.each(func(sub AgentObserver) { notifyNotice(sub, text) })
	}
}
