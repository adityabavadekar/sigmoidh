package sigmoidh

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
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
	defaultGeminiModel     = "gemini-2.5-flash"
	defaultGroqModel       = "openai/gpt-oss-120b"

	geminiOpenAIBase = "https://generativelanguage.googleapis.com/v1beta/openai"
	groqOpenAIBase   = "https://api.groq.com/openai/v1"
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
	// sameModel marks another account of the primary provider: it must use
	// the model the user chose, not the provider default.
	sameModel bool
}

// providerSpec describes one provider: where its keys live and how to build
// a client for a key.
type providerSpec struct {
	name     string
	keyEnvs  []string // accepted env vars for the primary key, in order
	model    string
	newKeyed func(key string) (*litellm.Client, error)
}

func openAICompatible(base string) func(string) (*litellm.Client, error) {
	return func(key string) (*litellm.Client, error) {
		return openai.NewClient(openai.Config{APIKey: key, BaseURL: base, API: openai.APIChat, Retry: retryPolicy},
			litellm.WithMessageRepair(litellm.RepairAll))
	}
}

// providerSpecs lists supported providers in fallback-preference order.
// Gemini and Groq use their OpenAI-compatible endpoints (tool calling and
// streaming included).
var providerSpecs = []providerSpec{
	{name: "anthropic", keyEnvs: []string{"ANTHROPIC_API_KEY"}, model: defaultAnthropicModel, newKeyed: func(k string) (*litellm.Client, error) {
		return anthropic.NewClient(anthropic.Config{APIKey: k, Retry: retryPolicy}, litellm.WithMessageRepair(litellm.RepairAll))
	}},
	{name: "openai", keyEnvs: []string{"OPENAI_API_KEY"}, model: defaultOpenAIModel, newKeyed: func(k string) (*litellm.Client, error) {
		return openai.NewClient(openai.Config{APIKey: k, Retry: retryPolicy}, litellm.WithMessageRepair(litellm.RepairAll))
	}},
	{name: "openrouter", keyEnvs: []string{"OPENROUTER_API_KEY"}, model: defaultOpenRouterModel, newKeyed: func(k string) (*litellm.Client, error) {
		return openrouter.NewClient(openrouter.Config{APIKey: k, Retry: retryPolicy}, litellm.WithMessageRepair(litellm.RepairAll))
	}},
	{name: "gemini", keyEnvs: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, model: defaultGeminiModel, newKeyed: openAICompatible(geminiOpenAIBase)},
	{name: "groq", keyEnvs: []string{"GROQ_API_KEY"}, model: defaultGroqModel, newKeyed: openAICompatible(groqOpenAIBase)},
}

func specFor(provider string) (providerSpec, bool) {
	for _, sp := range providerSpecs {
		if sp.name == provider {
			return sp, true
		}
	}
	return providerSpec{}, false
}

func providerNames() string {
	var names []string
	for _, sp := range providerSpecs {
		names = append(names, sp.name)
	}
	return strings.Join(names, ", ")
}

// providerKeys returns every configured key for a provider: the primary
// variable(s), then numbered accounts BASE_ACC_1, BASE_ACC_2, … (any count,
// numeric order), deduplicated. Several accounts let a rate-limited key fall
// over to the next one.
func providerKeys(sp providerSpec) []string {
	seen := map[string]bool{}
	var keys []string
	add := func(k string) {
		if k = strings.TrimSpace(k); k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for _, env := range sp.keyEnvs {
		add(os.Getenv(env))
	}
	type numbered struct {
		n   int
		key string
	}
	var extra []numbered
	for _, kv := range os.Environ() {
		name, val, _ := strings.Cut(kv, "=")
		for _, env := range sp.keyEnvs {
			if rest, ok := strings.CutPrefix(name, env+"_ACC_"); ok {
				if n, err := strconv.Atoi(rest); err == nil {
					extra = append(extra, numbered{n, val})
				}
			}
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].n < extra[j].n })
	for _, e := range extra {
		add(e.key)
	}
	return keys
}

func envKeyFor(provider string) string {
	if sp, ok := specFor(provider); ok {
		return sp.keyEnvs[0]
	}
	return ""
}

func defaultModelFor(provider string) string {
	if sp, ok := specFor(provider); ok {
		return sp.model
	}
	return ""
}

// buildClients constructs one client per configured key of a provider.
func buildClients(provider string) ([]*litellm.Client, string, error) {
	sp, ok := specFor(provider)
	if !ok {
		return nil, "", fmt.Errorf("unknown provider: %s (use %s)", provider, providerNames())
	}
	keys := providerKeys(sp)
	if len(keys) == 0 {
		return nil, "", fmt.Errorf("%s is not set (numbered accounts like %s_ACC_1 also work)", sp.keyEnvs[0], sp.keyEnvs[0])
	}
	var clients []*litellm.Client
	for _, k := range keys {
		c, err := sp.newKeyed(k)
		if err != nil {
			return nil, "", err
		}
		clients = append(clients, c)
	}
	return clients, sp.model, nil
}

// newClients builds the preferred provider's client plus fallbacks: its
// other accounts first (same model, so a rate limit just moves to the next
// key), then every other provider whose key is present.
func newClients(preferred string) (*litellm.Client, string, []providerAlt, error) {
	clients, model, err := buildClients(preferred)
	if err != nil {
		return nil, "", nil, err
	}
	var alts []providerAlt
	for i, c := range clients[1:] {
		alts = append(alts, providerAlt{Name: fmt.Sprintf("%s (account %d)", preferred, i+2), Client: c, Model: model, sameModel: true})
	}
	for _, sp := range providerSpecs {
		if sp.name == preferred {
			continue
		}
		if cs, m, e := buildClients(sp.name); e == nil {
			alts = append(alts, providerAlt{Name: sp.name, Client: cs[0], Model: m})
		}
	}
	return clients[0], model, alts, nil
}

// newClient keeps the original single-provider entry point for callers that
// do not need fallbacks.
func newClient(provider string) (*litellm.Client, string, error) {
	client, model, _, err := newClients(provider)
	return client, model, err
}
