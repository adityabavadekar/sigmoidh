package tools

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Checkpoints let the user undo what the agent changed. Every file a
// mutating tool (write, edit, apply_patch) touches is snapshotted the first
// time it is touched within a checkpoint; the host opens one checkpoint per
// user prompt, so /undo reverts a whole run. Changes made through bash or
// exec are not tracked.

const (
	maxCheckpoints      = 20
	maxSnapshotFileSize = 4 << 20
)

type fileSnapshot struct {
	path    string
	data    []byte
	existed bool
	tooBig  bool
}

type checkpoint struct {
	label string
	at    time.Time
	files []fileSnapshot
	seen  map[string]bool
}

var checkpoints struct {
	mu    sync.Mutex
	stack []*checkpoint
}

// BeginCheckpoint opens a new checkpoint; later file changes belong to it.
// An empty previous checkpoint is replaced rather than kept.
func BeginCheckpoint(label string) {
	checkpoints.mu.Lock()
	defer checkpoints.mu.Unlock()
	if n := len(checkpoints.stack); n > 0 && len(checkpoints.stack[n-1].files) == 0 {
		checkpoints.stack = checkpoints.stack[:n-1]
	}
	checkpoints.stack = append(checkpoints.stack, &checkpoint{label: label, at: time.Now(), seen: map[string]bool{}})
	if len(checkpoints.stack) > maxCheckpoints {
		checkpoints.stack = checkpoints.stack[len(checkpoints.stack)-maxCheckpoints:]
	}
}

// snapshotBefore records path's current content (or absence) in the open
// checkpoint, once per checkpoint. Call it before mutating the file.
func snapshotBefore(path string) {
	checkpoints.mu.Lock()
	defer checkpoints.mu.Unlock()
	if len(checkpoints.stack) == 0 {
		checkpoints.stack = append(checkpoints.stack, &checkpoint{label: "(unlabelled)", at: time.Now(), seen: map[string]bool{}})
	}
	cp := checkpoints.stack[len(checkpoints.stack)-1]
	if cp.seen[path] {
		return
	}
	cp.seen[path] = true
	snap := fileSnapshot{path: path}
	if info, err := os.Stat(path); err == nil {
		snap.existed = true
		if info.Size() > maxSnapshotFileSize {
			snap.tooBig = true
		} else if data, err := os.ReadFile(path); err == nil {
			snap.data = data
		} else {
			snap.tooBig = true // unreadable: cannot restore
		}
	}
	cp.files = append(cp.files, snap)
}

// UndoResult describes what UndoLast changed.
type UndoResult struct {
	Label    string
	Restored []string // files put back to their earlier content
	Removed  []string // files the run had created, now deleted
	Skipped  []string // files too large/unreadable to have been snapshotted
}

var ErrNothingToUndo = errors.New("nothing to undo: no file changes recorded by write/edit/apply_patch")

// UndoLast reverts the most recent checkpoint that changed files and drops it.
func UndoLast() (UndoResult, error) {
	checkpoints.mu.Lock()
	defer checkpoints.mu.Unlock()
	for len(checkpoints.stack) > 0 {
		cp := checkpoints.stack[len(checkpoints.stack)-1]
		checkpoints.stack = checkpoints.stack[:len(checkpoints.stack)-1]
		if len(cp.files) == 0 {
			continue
		}
		res := UndoResult{Label: cp.label}
		var errs []error
		for i := len(cp.files) - 1; i >= 0; i-- {
			f := cp.files[i]
			switch {
			case f.tooBig:
				res.Skipped = append(res.Skipped, f.path)
			case !f.existed:
				if err := os.Remove(f.path); err != nil {
					if !os.IsNotExist(err) {
						errs = append(errs, err)
					}
					continue // never created (e.g. a patch that failed)
				}
				res.Removed = append(res.Removed, f.path)
			default:
				if err := os.WriteFile(f.path, f.data, 0o644); err != nil {
					errs = append(errs, err)
					continue
				}
				res.Restored = append(res.Restored, f.path)
			}
		}
		if len(errs) > 0 {
			return res, fmt.Errorf("undo incomplete: %w", errors.Join(errs...))
		}
		return res, nil
	}
	return UndoResult{}, ErrNothingToUndo
}

// UndoableCheckpoints counts checkpoints that hold file changes.
func UndoableCheckpoints() int {
	checkpoints.mu.Lock()
	defer checkpoints.mu.Unlock()
	n := 0
	for _, cp := range checkpoints.stack {
		if len(cp.files) > 0 {
			n++
		}
	}
	return n
}
