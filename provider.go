package main

import (
	"fmt"
	"os"

	"github.com/voocel/litellm"
	"github.com/voocel/litellm/provider/anthropic"
	"github.com/voocel/litellm/provider/openai"
	"github.com/voocel/litellm/provider/openrouter"
)

const (
	defaultAnthropicModel  = "claude-sonnet-4-5-20250929"
	defaultOpenAIModel     = "gpt-5.4-mini"
	defaultOpenRouterModel = "anthropic/claude-sonnet-4.5"
)

// newClient builds a provider client and its default model from an env var API key.
func newClient(provider string) (*litellm.Client, string, error) {
	switch provider {
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, "", fmt.Errorf("ANTHROPIC_API_KEY is not set")
		}
		client, err := anthropic.NewClient(anthropic.Config{APIKey: key})
		return client, defaultAnthropicModel, err
	case "openai":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, "", fmt.Errorf("OPENAI_API_KEY is not set")
		}
		client, err := openai.NewClient(openai.Config{APIKey: key})
		return client, defaultOpenAIModel, err
	case "openrouter":
		key := os.Getenv("OPENROUTER_API_KEY")
		if key == "" {
			return nil, "", fmt.Errorf("OPENROUTER_API_KEY is not set")
		}
		client, err := openrouter.NewClient(openrouter.Config{APIKey: key})
		return client, defaultOpenRouterModel, err
	default:
		return nil, "", fmt.Errorf("unknown provider: %s (use anthropic, openai, or openrouter)", provider)
	}
}
