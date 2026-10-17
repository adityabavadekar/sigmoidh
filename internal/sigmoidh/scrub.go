package sigmoidh

import "strings"

const (
	openTag  = "<think>"
	closeTag = "</think>"
)

// thinkScrubber removes inline <think>…</think> blocks from streamed content,
// statefully: some open-weight models emit them inside ordinary text instead
// of using native reasoning channels. Partial tags at chunk boundaries are
// held back until disambiguated, so nothing half-open ever reaches the UI.
type thinkScrubber struct {
	inThink bool
	pend    string
}

func (t *thinkScrubber) Scrub(chunk string) string {
	data := t.pend + chunk
	t.pend = ""
	var out strings.Builder

	for {
		tag := openTag
		if t.inThink {
			tag = closeTag
		}
		idx := strings.Index(data, tag)
		if idx < 0 {
			keep := maxPartial(data, tag)
			if !t.inThink {
				out.WriteString(data[:len(data)-keep])
			}
			t.pend = data[len(data)-keep:]
			return out.String()
		}
		if !t.inThink {
			out.WriteString(data[:idx])
		}
		t.inThink = !t.inThink
		data = data[idx+len(tag):]
	}
}

// Flush emits held-back bytes at end-of-stream (they were not a tag prefix).
func (t *thinkScrubber) Flush() string {
	out := t.pend
	t.pend = ""
	return out
}

// maxPartial returns the length of the longest suffix of s that is a prefix
// of tag (so it might complete on the next chunk).
func maxPartial(s, tag string) int {
	max := len(tag) - 1
	if max > len(s) {
		max = len(s)
	}
	for k := max; k > 0; k-- {
		if strings.HasPrefix(tag, s[len(s)-k:]) {
			return k
		}
	}
	return 0
}
