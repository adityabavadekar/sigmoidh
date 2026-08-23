package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestEmptyBashCommandIsLoudError(t *testing.T) {
	cases := []string{``, `   `, "\n\t"}
	for _, cmd := range cases {
		raw, _ := json.Marshal(map[string]string{"command": cmd})
		out, isErr := RunTool(context.Background(), "bash", raw, t.TempDir())
		if !isErr {
			t.Fatalf("empty command %q must be an error, got output %q", cmd, out)
		}
		if !strings.Contains(out, "empty command") {
			t.Fatalf("unhelpful error for %q: %q", cmd, out)
		}
	}
}

func TestSandboxToggleRunsHostShell(t *testing.T) {
	dir := t.TempDir()
	prev := SandboxEnabled()
	defer SetSandboxEnabled(prev)

	raw, _ := json.Marshal(map[string]string{"command": "echo sandbox-$FOO"})
	os.Setenv("FOO", "off")
	defer os.Unsetenv("FOO")

	SetSandboxEnabled(false)
	out, isErr := RunTool(context.Background(), "bash", raw, dir)
	if isErr || !strings.Contains(out, "sandbox-off") {
		t.Fatalf("no-sandbox run failed: %q %v", out, isErr)
	}

	SetSandboxEnabled(true)
	// bwrap binds /usr,/bin etc but not arbitrary host env; command must still succeed
	out2, isErr2 := RunTool(context.Background(), "bash", raw, dir)
	if isErr2 || !strings.Contains(out2, "sandbox-") {
		t.Fatalf("sandboxed run failed: %q %v", out2, isErr2)
	}
}
