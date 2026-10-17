package sigmoidh

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Working memory: a compact, agent-managed scratchpad that survives across
// turns. The agent distills what matters (decisions, paths, findings) here so
// older tool results and thinking can be pruned or forgotten without losing
// the essentials.

const (
	maxMemoryKeys     = 64
	maxMemoryValueLen = 2000
	maxMemoryKeyLen   = 64
)

type memoryArgs struct {
	Op    string `json:"op"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

type memoryEntry struct {
	key   string
	value string
}

type memoryStore struct {
	mu      sync.Mutex
	entries map[string]string
	order   []string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{entries: map[string]string{}}
}

func (m *memoryStore) list() []memoryEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]memoryEntry, 0, len(m.order))
	for _, k := range m.order {
		out = append(out, memoryEntry{k, m.entries[k]})
	}
	return out
}

func (m *memoryStore) write(key, value string) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	if len(key) > maxMemoryKeyLen {
		return fmt.Errorf("key too long (max %d chars)", maxMemoryKeyLen)
	}
	if len(value) > maxMemoryValueLen {
		return fmt.Errorf("value too long (max %d chars); keep it terse", maxMemoryValueLen)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[key]; !exists && len(m.entries) >= maxMemoryKeys {
		return fmt.Errorf("memory full (%d keys); delete or clear before adding more", maxMemoryKeys)
	}
	if _, exists := m.entries[key]; !exists {
		m.order = append(m.order, key)
	}
	m.entries[key] = value
	return nil
}

func (m *memoryStore) del(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[key]; !ok {
		return false
	}
	delete(m.entries, key)
	for i, k := range m.order {
		if k == key {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	return true
}

// handleMemory is the host-side implementation of the "memory" tool.
func (s *Sigmoidh) handleMemory(_ context.Context, raw json.RawMessage, _ string) (string, bool) {
	var a memoryArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err.Error(), true
	}
	a.Op = strings.ToLower(strings.TrimSpace(a.Op))
	a.Key = strings.TrimSpace(a.Key)

	switch a.Op {
	case "write":
		if err := s.memory.write(a.Key, a.Value); err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("remembered %q (%d bytes)", a.Key, len(a.Value)), false

	case "read":
		if a.Key == "" {
			return "key is required for op=read", true
		}
		s.memory.mu.Lock()
		v, ok := s.memory.entries[a.Key]
		s.memory.mu.Unlock()
		if !ok {
			return fmt.Sprintf("no memory for %q", a.Key), true
		}
		return v, false

	case "delete":
		if !s.memory.del(a.Key) {
			return fmt.Sprintf("no memory for %q", a.Key), true
		}
		return fmt.Sprintf("forgot %q", a.Key), false

	case "clear":
		s.memory.mu.Lock()
		n := len(s.memory.entries)
		s.memory.entries = map[string]string{}
		s.memory.order = nil
		s.memory.mu.Unlock()
		return fmt.Sprintf("cleared %d memories", n), false

	case "list", "":
		entries := s.memory.list()
		if len(entries) == 0 {
			return "(working memory empty)", false
		}
		sort.SliceStable(entries, func(i, j int) bool { return false }) // preserve insertion order
		var b strings.Builder
		fmt.Fprintf(&b, "%d memories:\n", len(entries))
		for _, e := range entries {
			fmt.Fprintf(&b, "- %s: %s\n", e.key, e.value)
		}
		return strings.TrimRight(b.String(), "\n"), false

	default:
		return `op must be one of: write, read, delete, clear, list`, true
	}
}
