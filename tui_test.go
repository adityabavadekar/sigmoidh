package main

import (
	"strings"
	"testing"
	"time"

	"github.com/rivo/tview"
)

func TestTuiObserverRendersColoredEvents(t *testing.T) {
	feed := tview.NewTextView().SetDynamicColors(true)
	obs := NewDirectTuiObserver(feed)

	obs.OnToolCall("bash", []byte(`{"command":"git remote -v"}`))
	obs.OnThinkingStart()
	obs.OnReasoningDelta("weigh it")
	obs.OnThinkingEnd(1200 * time.Millisecond)
	obs.OnProviderFallback("stealth", "other/model")

	out := feed.GetText(true)
	for _, want := range []string{"• bash", "git remote -v", "falling back", "thought for 1.2s", "falling back"} {
		if !strings.Contains(out, want) {
			t.Fatalf("feed missing %q:\n%s", want, out)
		}
	}
}
