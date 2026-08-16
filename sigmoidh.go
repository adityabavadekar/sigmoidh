package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/voocel/litellm"
	"os"
)

const (
	maxTurns  = 30
	maxTokens = 4096
)

type Sigmoidh struct {
	client   *litellm.Client
	model    string
	messages []litellm.Message
}

func NewSigmoidh(client *litellm.Client, model, system string) *Sigmoidh {
	return &Sigmoidh{
		client:   client,
		model:    model,
		messages: []litellm.Message{litellm.System(system)},
	}
}

// Run sends input to the model and executes tool calls until it replies with plain text.
func (a *Sigmoidh) Run(ctx context.Context, input string) error {
	a.messages = append(a.messages, litellm.UserText(input))

	for turn := 0; turn < maxTurns; turn++ {
		resp, err := a.client.Chat(ctx, litellm.Request{
			Model:     a.model,
			Messages:  a.messages,
			Tools:     tools,
			MaxTokens: litellm.IntPtr(maxTokens),
		})
		if err != nil {
			return err
		}
		a.messages = append(a.messages, litellm.Assistant(resp.Blocks...))

		if text := resp.Text(); text != "" {
			fmt.Println(text)
		}

		calls := resp.ToolCalls()
		if len(calls) == 0 {
			return nil
		}

		results := make([]litellm.Block, len(calls))
		for i, call := range calls {
			logCall(call.Name, call.Arguments)
			out, isErr := runTool(ctx, call.Name, call.Arguments)
			logResult(out, isErr)
			results[i] = litellm.ToolResultBlock{
				ToolUseID: call.ID,
				Content:   []litellm.Block{litellm.Text(out)},
				IsError:   isErr,
			}
		}
		a.messages = append(a.messages, litellm.Message{Role: litellm.RoleTool, Blocks: results})
	}

	return fmt.Errorf("stopped after %d turns", maxTurns)
}

func logCall(name string, args json.RawMessage) {
	fmt.Fprintf(os.Stderr, "==> %s %s\n", name, string(args))
}

func logResult(out string, isErr bool) {
	status := "ok"
	if isErr {
		status = "error"
	}
	fmt.Fprintf(os.Stderr, "################## [START] tool result 	##################\n")
	fmt.Fprintf(os.Stderr, "  %s\n", out)
	fmt.Fprintf(os.Stderr, "################## [END]   tool result ##################\n")
	fmt.Fprintf(os.Stderr, "  %s (%d bytes)\n", status, len(out))
}
