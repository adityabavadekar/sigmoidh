package sigmoidh

import (
	"fmt"
	"strings"
)

// Degenerate-generation detection: some models (notably older Gemini builds,
// but any model can) fall into emitting the same word or short phrase over and
// over. The existing tool-call loop guard cannot see this because it only
// looks at tool calls, so this catches repetition in the assistant's prose.

const (
	repeatWordRun  = 6  // same word this many times in a row → degenerate
	repeatGramReps = 5  // same n-word phrase this many times in a row → degenerate
	repeatGramMaxN = 5  // longest phrase considered
	repeatMinWords = 12 // ignore short answers
	repeatScanCap  = 1500
)

// degenerateRepetition reports a human-readable reason when text has collapsed
// into repeated words or phrases, or "" when the text looks normal.
func degenerateRepetition(text string) string {
	words := strings.Fields(text)
	if len(words) > repeatScanCap {
		words = words[:repeatScanCap]
	}

	// Consecutive single-word runs ("the the the the the the"). Checked before
	// the length gate: a short answer can still be a runaway word loop.
	run := 1
	for i := 1; i < len(words); i++ {
		if sameWord(words[i], words[i-1]) {
			run++
			if run >= repeatWordRun {
				return fmt.Sprintf("%q repeated %d times", words[i], run)
			}
		} else {
			run = 1
		}
	}

	if len(words) < repeatMinWords {
		return ""
	}

	// Consecutive n-gram repeats ("I will do it I will do it ...").
	for n := 2; n <= repeatGramMaxN; n++ {
		if len(words) < n*repeatGramReps {
			continue
		}
		for i := 0; i+n*repeatGramReps <= len(words); i++ {
			reps := 1
			for j := i + n; j+n <= len(words); j += n {
				if !sameGram(words[j:j+n], words[i:i+n]) {
					break
				}
				reps++
			}
			if reps >= repeatGramReps {
				return fmt.Sprintf("phrase %q repeated %d times", strings.Join(words[i:i+n], " "), reps)
			}
		}
	}
	return ""
}

func sameGram(a, b []string) bool {
	for i := range a {
		if !sameWord(a[i], b[i]) {
			return false
		}
	}
	return true
}

func sameWord(a, b string) bool {
	return normWord(a) == normWord(b)
}

// normWord lowercases and strips surrounding punctuation so "Done" and "done,"
// compare equal. Digits and apostrophes inside a word are preserved.
func normWord(w string) string {
	w = strings.TrimFunc(w, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '\'')
	})
	return strings.ToLower(w)
}
