package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	agenttools "github.com/adityabavadekar/sigmoidh/tools"
	"github.com/voocel/litellm"
)

const (
	maxTurns  = 30
	maxTokens = 4096
)

type Sigmoidh struct {
	client      *litellm.Client
	model       string
	workDir     string
	messages    []litellm.Message
	historyFile *os.File
	lastTexts   []string
	debug       bool
}

type historyEntry struct {
	Timestamp string            `json:"ts"`
	Role      string            `json:"role"`
	Content   string            `json:"content,omitempty"`
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

func NewSigmoidh(client *litellm.Client, model, system, workDir string, debug *bool) *Sigmoidh {
	s := &Sigmoidh{
		client:   client,
		model:    model,
		workDir:  workDir,
		messages: []litellm.Message{litellm.System(system)},
		debug:    *debug,
	}

	histDir := filepath.Join(homeDir(), ".sigmoidh", "history")
	os.MkdirAll(histDir, 0o755)
	fname := filepath.Join(histDir, time.Now().Format("2001-01-02_15-04-05")+".jsonl")
	if f, err := os.OpenFile(fname, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		s.historyFile = f
		s.writeHistory(historyEntry{
			Timestamp: time.Now().Format(time.RFC3339),
			Role:      "system",
			Content:   system,
		})
	}

	return s
}

func (s *Sigmoidh) Close() {
	if s.historyFile != nil {
		s.historyFile.Close()
	}
}

func (s *Sigmoidh) writeHistory(entry historyEntry) {
	if s.historyFile == nil {
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	s.historyFile.Write(append(data, '\n'))
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "."
}

func (s *Sigmoidh) Run(ctx context.Context, input string) error {
	s.messages = append(s.messages, litellm.UserText(input))
	s.writeHistory(historyEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Role:      "user",
		Content:   input,
	})

	for turn := 0; turn < maxTurns; turn++ {
		resp, err := s.client.Chat(ctx, litellm.Request{
			Model:     s.model,
			Messages:  s.messages,
			Tools:     agenttools.Definitions(),
			MaxTokens: litellm.IntPtr(maxTokens),
		})
		if err != nil {
			return err
		}
		s.messages = append(s.messages, litellm.Assistant(resp.Blocks...))

		if text := resp.Reasoning(); text != "" {
			fmt.Fprintf(os.Stderr, "[REASONING-START]\n %s\n[REASONING-END]\n\n", text)
		}

		textResp := resp.Text()
		if textResp != "" {
			fmt.Println(textResp)
		}

		calls := resp.ToolCalls()

		entry := historyEntry{
			Timestamp: time.Now().Format(time.RFC3339),
			Role:      "assistant",
			Content:   textResp,
		}
		for _, c := range calls {
			entry.ToolCalls = append(entry.ToolCalls, historyToolCall{
				ID: c.ID, Name: c.Name, Args: c.Arguments,
			})
		}
		s.writeHistory(entry)

		if len(calls) == 0 {
			if textResp != "" {
				s.lastTexts = append(s.lastTexts, textResp)
			}
			return nil
		}

		results := make([]litellm.Block, len(calls))
		resultEntries := make([]historyResult, len(calls))

		for i, call := range calls {
			logCall(call.Name, call.Arguments, s.debug)
			out, isErr := agenttools.RunTool(ctx, call.Name, call.Arguments, s.workDir)
			if out == "" && !isErr {
				out = "(no output)"
			}
			logResult(out, isErr, s.debug)
			results[i] = litellm.ToolResultBlock{
				ToolUseID: call.ID,
				Content:   []litellm.Block{litellm.Text(out)},
				IsError:   isErr,
			}
			resultEntries[i] = historyResult{
				ToolUseID: call.ID,
				Output:    out,
				IsError:   isErr,
			}
		}
		s.messages = append(s.messages, litellm.Message{Role: litellm.RoleTool, Blocks: results})
		s.writeHistory(historyEntry{
			Timestamp: time.Now().Format(time.RFC3339),
			Role:      "tool",
			Results:   resultEntries,
		})
	}

	return fmt.Errorf("stopped after %d turns", maxTurns)
}

func logCall(name string, args json.RawMessage, debug bool) {
	fmt.Fprintf(os.Stderr, "==> %s %s\n", name, string(args))
}

func logResult(out string, isErr bool, debug bool) {
	status := "ok"
	if isErr {
		status = "error"
	}
	if !debug {
		return
	}

	fmt.Fprintf(os.Stderr, "################## [START] tool result \t##################\n")
	fmt.Fprintf(os.Stderr, "  %s\n", out)
	fmt.Fprintf(os.Stderr, "################## [END]   tool result ##################\n")
	fmt.Fprintf(os.Stderr, "  %s (%d bytes)\n", status, len(out))
}
