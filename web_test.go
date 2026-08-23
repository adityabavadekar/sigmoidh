package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	agenttools "github.com/adityabavadekar/sigmoidh/tools"
	"time"
)

func TestWebHubStreamsEvents(t *testing.T) {
	hub := newWebHub()
	srv := httptest.NewServer(hub.handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	go func() {
		time.Sleep(100 * time.Millisecond)
		wo := &WebObserver{hub: hub}
		wo.OnToolCall("bash", json.RawMessage(`{"command":"echo hi"}`))
	}()

	buf := make([]byte, 4096)
	var acc string
	for {
		n, err := resp.Body.Read(buf)
		acc += string(buf[:n])
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}
		if strings.Contains(acc, `"event":"tool_call"`) {
			break
		}
	}
	if !strings.Contains(acc, `"name":"bash"`) || !strings.Contains(acc, "echo hi") {
		t.Fatalf("SSE event incomplete:\n%s", acc)
	}
}

func TestMultiObserverFansOut(t *testing.T) {
	hub := newWebHub()
	wo := &WebObserver{hub: hub}
	termBuf := &bytes.Buffer{}
	term := &TerminalLogger{w: termBuf, outTTY: false, showThinking: true}
	mo := &multiObserver{observers: []AgentObserver{term, wo}}

	sub := hub.subscribe()
	mo.OnProviderFallback("stealth", "other/model")
	select {
	case data := <-sub:
		var ev map[string]any
		if json.Unmarshal(data, &ev) != nil || ev["event"] != "fallback" {
			t.Fatalf("web observer missed event: %s", data)
		}
	case <-time.After(time.Second):
		t.Fatal("no event received")
	}
	if !strings.Contains(termBuf.String(), "falling back") {
		t.Fatal("terminal observer missed event")
	}
}

func TestWebPageServed(t *testing.T) {
	hub := newWebHub()
	srv := httptest.NewServer(hub.handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "sigmoidh") || !strings.Contains(string(body), "/events") {
		t.Fatalf("page incomplete")
	}
}

func TestUserInputEventFlowsToWeb(t *testing.T) {
	hub := newWebHub()
	wo := &WebObserver{hub: hub}
	termBuf := &bytes.Buffer{}
	mo := &multiObserver{observers: []AgentObserver{
		&TerminalLogger{w: termBuf, outTTY: false, showThinking: true}, wo,
	}}

	sub := hub.subscribe()
	notifyUserInput(mo, "make the website")
	select {
	case data := <-sub:
		var ev map[string]any
		if json.Unmarshal(data, &ev) != nil || ev["event"] != "user_input" {
			t.Fatalf("bad event: %s", data)
		}
		if ev["text"] != "make the website" {
			t.Fatalf("text mismatch: %s", data)
		}
	case <-time.After(time.Second):
		t.Fatal("no user_input event received")
	}
	// terminal logger has no OnUserInput: must not panic via forwarding
	if strings.Contains(termBuf.String(), "make the website") {
		t.Fatal("terminal should stay quiet for user input events")
	}
}

func TestNotifyClassifiesUserInput(t *testing.T) {
	hub := newWebHub()
	wo := &WebObserver{hub: hub}
	termBuf := &bytes.Buffer{}
	mo := &multiObserver{observers: []AgentObserver{
		&TerminalLogger{w: termBuf, outTTY: false, showThinking: true}, wo,
	}}

	sub := hub.subscribe()
	lines := []struct{ in, kind, text string }{
		{"/help", "command", "/help"},
		{"exit", "command", "exit"},
		{"quit", "command", "quit"},
		{"make the site", "message", "make the site"},
	}
	for _, l := range lines {
		notifyUserInput(mo, l.in)
	}

	got := map[string]string{}
	deadline := time.After(time.Second)
	for len(got) < len(lines) {
		select {
		case data := <-sub:
			var ev map[string]any
			if json.Unmarshal(data, &ev) != nil || ev["event"] != "user_input" {
				continue
			}
			got[ev["text"].(string)] = ev["kind"].(string)
		case <-deadline:
			t.Fatalf("missing events; got %v", got)
		}
	}
	for _, l := range lines {
		if got[l.text] != l.kind {
			t.Fatalf("%q classified %q, want %q", l.text, got[l.text], l.kind)
		}
	}
}

func TestSpillHandlerPersistsFullOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	big := strings.Repeat("A", agenttools.SpillThresholdChars+500)

	spilled := spillOutput("bash", big)
	if len(spilled) >= len(big) {
		t.Fatal("spilled preview must be smaller than input")
	}
	if !strings.Contains(spilled, "[output truncated") || !strings.Contains(spilled, "Full text saved to:") {
		t.Fatalf("missing file reference:\\n%.200s", spilled)
	}
	const marker = "Full text saved to: "
	i := strings.Index(spilled, marker)
	if i < 0 {
		t.Fatalf("no file marker:\n%s", spilled)
	}
	path := spilled[i+len(marker):]
	if j := strings.Index(path, " (use"); j >= 0 {
		path = path[:j]
	}
	path = strings.TrimSpace(path)
	data, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != len(big) {
		t.Fatalf("full output not persisted: %d vs %d", len(data), len(big))
	}
}

func TestIterationBudgetRefund(t *testing.T) {
	b := newIterationBudget(2)
	if !b.Consume() || !b.Consume() {
		t.Fatal("consume failed")
	}
	if b.Consume() {
		t.Fatal("budget should be exhausted")
	}
	b.Refund()
	if !b.Consume() {
		t.Fatal("refund did not restore a turn")
	}
}
