package sigmoidh

import (
	"sync"
)

// iterationBudget is a thread-safe consume/refund counter for agent turns.
// Parents and sub-agents each hold their own; refunds cover retries that
// didn't actually make progress (empty completions, transient failures).
// max <= 0 means unlimited.
type iterationBudget struct {
	mu   sync.Mutex
	left int
	max  int
}

func newIterationBudget(max int) *iterationBudget {
	return &iterationBudget{left: max, max: max}
}

// Consume takes one turn; false means exhausted.
func (b *iterationBudget) Consume() bool {
	if b.max <= 0 { // unlimited: nothing to track
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.left <= 0 {
		return false
	}
	b.left--
	return true
}

// Refund returns an unconsumed turn (retry didn't make progress).
func (b *iterationBudget) Refund() {
	if b.max <= 0 {
		return
	}
	b.mu.Lock()
	b.left++
	if b.left > b.max {
		b.left = b.max
	}
	b.mu.Unlock()
}

func (b *iterationBudget) Left() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.left
}
