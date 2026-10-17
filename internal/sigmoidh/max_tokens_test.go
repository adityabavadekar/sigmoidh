package sigmoidh

import (
	"errors"
	"testing"
)

func TestEffectiveMaxTokens(t *testing.T) {
	// 1. OpenRouter default should be nil (not 64000) so context length is not exhausted
	sOR := &Sigmoidh{
		providers: []providerAlt{{Name: "openrouter"}},
	}
	if tok := sOR.effectiveMaxTokens(); tok != nil {
		t.Fatalf("expected nil max_tokens for openrouter default, got %d", *tok)
	}

	// 2. OpenAI default should be nil
	sOA := &Sigmoidh{
		providers: []providerAlt{{Name: "openai"}},
	}
	if tok := sOA.effectiveMaxTokens(); tok != nil {
		t.Fatalf("expected nil max_tokens for openai default, got %d", *tok)
	}

	// 3. Anthropic default should be 64000 (required by Anthropic API)
	sAnth := &Sigmoidh{
		providers: []providerAlt{{Name: "anthropic"}},
	}
	tok := sAnth.effectiveMaxTokens()
	if tok == nil || *tok != 64000 {
		t.Fatalf("expected 64000 for anthropic default, got %v", tok)
	}

	// 4. Explicit maxTokens should override for any provider
	sOR.SetMaxTokens(4096)
	if tok := sOR.effectiveMaxTokens(); tok == nil || *tok != 4096 {
		t.Fatalf("expected 4096 when explicitly configured, got %v", tok)
	}
}

func TestIsOutputTokenLimitError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{
			err:  errors.New("This endpoint's maximum context length is 65536 tokens. However, you requested about 66511 tokens (297 of text input, 2214 of tool input, 64000 in the output). Please reduce the length"),
			want: true,
		},
		{
			err:  errors.New("context_length_exceeded: max_tokens is too large"),
			want: true,
		},
		{
			err:  errors.New("rate limit exceeded"),
			want: false,
		},
		{
			err:  nil,
			want: false,
		},
	}

	for _, tc := range cases {
		got := isOutputTokenLimitError(tc.err)
		if got != tc.want {
			t.Errorf("isOutputTokenLimitError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
