package sigmoidh

import (
	"testing"

	"github.com/chzyer/readline"
)

func TestTranslateKeyCtrlBackspace(t *testing.T) {
	got, process := translateKey(8)
	if !process || got != readline.CharCtrlW {
		t.Fatalf("ctrl+backspace (0x08) must map to CharCtrlW, got %d/%v", got, process)
	}
	for _, r := range []rune{'a', '/', ' ', 127, 27} {
		got, process := translateKey(r)
		if !process || got != r {
			t.Fatalf("passthrough broken for %q", r)
		}
	}
}
