package main

import (
	"fmt"
	"os"
	"time"

	"github.com/voocel/litellm"
	"github.com/voocel/litellm/provider/anthropic"
	"github.com/voocel/litellm/provider/openai"
	"github.com/voocel/litellm/provider/openrouter"
	litellmretry "github.com/voocel/litellm/retry"
)

const (
	defaultAnthropicModel  = "claude-sonnet-4-5-20250929"
	defaultOpenAIModel     = "gpt-5.4-mini"
	defaultOpenRouterModel = "anthropic/claude-sonnet-4.5"
)

// retryPolicy is applied at the HTTP transport layer: transient failures
// (429/5xx/connection resets before the stream opens) are retried with
// exponential backoff, honoring Retry-After headers.
var retryPolicy = &litellmretry.Policy{
	MaxAttempts:       4,
	InitialDelay:      500 * time.Millisecond,
	MaxDelay:          8 * time.Second,
	Multiplier:        2,
	Jitter:            true,
	RespectRetryAfter: true,
}

// providerAlt is a backup provider built from whatever API keys exist.
type providerAlt struct {
	Name   string
	Client *litellm.Client
	Model  string
}

func envKeyFor(provider string) string {
	switch provider {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	default:
		return ""
	}
}

func defaultModelFor(provider string) string {
	switch provider {
	case "anthropic":
		return defaultAnthropicModel
	case "openai":
		return defaultOpenAIModel
	case "openrouter":
		return defaultOpenRouterModel
	default:
		return ""
	}
}

// buildClient constructs one provider client from its env-var API key.
func buildClient(provider string) (*litellm.Client, string, error) {
	key := os.Getenv(envKeyFor(provider))
	if key == "" {
		return nil, "", fmt.Errorf("%s is not set", envKeyFor(provider))
	}
	switch provider {
	case "anthropic":
		client, err := anthropic.NewClient(anthropic.Config{APIKey: key, Retry: retryPolicy})
		return client, defaultAnthropicModel, err
	case "openai":
		client, err := openai.NewClient(openai.Config{APIKey: key, Retry: retryPolicy})
		return client, defaultOpenAIModel, err
	case "openrouter":
		client, err := openrouter.NewClient(openrouter.Config{APIKey: key, Retry: retryPolicy})
		return client, defaultOpenRouterModel, err
	default:
		return nil, "", fmt.Errorf("unknown provider: %s (use anthropic, openai, or openrouter)", provider)
	}
}

// newClients builds the preferred provider client plus fallback clients for
// every other provider whose API key is present in the environment.
func newClients(preferred string) (*litellm.Client, string, []providerAlt, error) {
	client, model, err := buildClient(preferred)
	if err != nil {
		return nil, "", nil, err
	}
	var alts []providerAlt
	for _, name := range []string{"anthropic", "openai", "openrouter"} {
		if name == preferred || os.Getenv(envKeyFor(name)) == "" {
			continue
		}
		if c, m, e := buildClient(name); e == nil {
			alts = append(alts, providerAlt{Name: name, Client: c, Model: m})
		}
	}
	return client, model, alts, nil
}

// newClient keeps the original single-provider entry point for callers that
// do not need fallbacks.
func newClient(provider string) (*litellm.Client, string, error) {
	client, model, _, err := newClients(provider)
	return client, model, err
}
