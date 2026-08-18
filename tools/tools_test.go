package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBashNetwork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cwd, _ := os.Getwd()
	out, isErr := runBash(ctx, "ping -c 1 google.com", cwd)
	if isErr {
		t.Fatalf("network/Ping failed: %s", out)
	}

	if !strings.Contains(out, "1 packets transmitted, 1 received") {
		t.Fatalf("Ping returned unexpected result: %s", out)
	}

	t.Logf("ping google.com: OK")

	out, isErr = runBash(ctx, "curl -s -o /dev/null -w '%{http_code}' https://www.google.com/generate_204", cwd)
	if isErr {
		t.Fatalf("network/HTTP failed: %s", out)
	}

	if strings.TrimSpace(out) != "204" {
		t.Fatalf("HTTP returned non-204 status: %s", out)
	}

	t.Logf("network OK: %s", strings.TrimSpace(out))
}

func TestWebSearch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	out, isErr := runWeb(ctx, "", "golang web search test")
	if isErr {
		t.Fatalf("Web search failed: %s", out)
	}

	t.Logf("Web search result: %s", out)

}
