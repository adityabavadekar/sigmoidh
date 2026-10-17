package tools

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultYieldMS = 250
	maxYieldMS     = 5000
)

type execSession struct {
	id                  int
	command             string
	started             time.Time
	cmd                 *exec.Cmd
	stdin               io.WriteCloser
	cancel              func()
	captureWG           sync.WaitGroup
	initialSnapshotDone chan struct{}
	background          bool

	mu     sync.Mutex
	output bytes.Buffer
	done   bool
	err    error
}

var execSessions = struct {
	sync.Mutex
	next int
	byID map[int]*execSession
}{byID: map[int]*execSession{}}

var execSessionDoneHandler struct {
	sync.RWMutex
	fn func(ExecSessionResult)
}

// SetExecSessionDoneHandler registers the host callback for every completed
// background command. Passing nil disables automatic completion delivery.
func SetExecSessionDoneHandler(fn func(ExecSessionResult)) {
	execSessionDoneHandler.Lock()
	execSessionDoneHandler.fn = fn
	execSessionDoneHandler.Unlock()
}

func getExecSessionDoneHandler() func(ExecSessionResult) {
	execSessionDoneHandler.RLock()
	defer execSessionDoneHandler.RUnlock()
	return execSessionDoneHandler.fn
}

// ExecSessionsStatus returns a concise snapshot of all uncollected exec sessions.
func ExecSessionsStatus() string {
	execSessions.Lock()
	sessions := make([]*execSession, 0, len(execSessions.byID))
	for _, s := range execSessions.byID {
		sessions = append(sessions, s)
	}
	execSessions.Unlock()
	if len(sessions) == 0 {
		return "none"
	}
	lines := make([]string, 0, len(sessions))
	for _, s := range sessions {
		s.mu.Lock()
		state := "running"
		if s.done {
			state = "completed"
			if s.err != nil {
				state += " (error)"
			}
		}
		command := s.command
		if len(command) > 100 {
			command = command[:100] + "…"
		}
		lines = append(lines, fmt.Sprintf("#%d %s (%s): %s", s.id, state, time.Since(s.started).Round(time.Second), command))
		s.mu.Unlock()
	}
	out := strings.Join(lines, " | ")
	for _, s := range sessions {
		s.mu.Lock()
		running := !s.done
		s.mu.Unlock()
		if running {
			out += "\n[running exec sessions: notified on exit. write_stdin=input, status=progress, schedule op=once=periodic wake. Don't check without purpose.]"
			break
		}
	}
	return out
}

func startExecSession(command string, yieldTimeMS int, workDir string) (string, bool) {
	if command == "" {
		return "command cannot be empty", true
	}
	if isSimpleSleep(command) || isBackgroundedSimpleSleep(command) {
		return "Refused: plain sleep (also with '&') is a wait, not background work. Run it with bash WITHOUT '&' (blocks, returns after the wait), or schedule op=once for timer/reminder.", true
	}
	if segment, blocked := destructiveCommandOnProtectedPath(command, workDir); blocked {
		return BlockedCommandRefusal(segment), true
	}

	var cmd *exec.Cmd
	if sandboxEnabled {
		cmd = exec.Command("bwrap", bashSandboxArgs(command, workDir)...)
	} else {
		cmd = exec.Command("sh", "-c", command)
		cmd.Dir = workDir
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err.Error(), true
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err.Error(), true
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err.Error(), true
	}

	s := &execSession{cmd: cmd, stdin: stdin, command: command, started: time.Now(), initialSnapshotDone: make(chan struct{})}
	execSessions.Lock()
	execSessions.next++
	s.id = execSessions.next
	execSessions.byID[s.id] = s
	execSessions.Unlock()

	if err := cmd.Start(); err != nil {
		removeExecSession(s.id)
		return err.Error(), true
	}
	s.captureWG.Add(2)
	go func() { defer s.captureWG.Done(); s.capture(stdout) }()
	go func() { defer s.captureWG.Done(); s.capture(stderr) }()
	go func() {
		// Drain stdout/stderr before Wait: Wait closes the pipes, and calling it
		// first can discard buffered tail output (os/exec documents that reads
		// must complete before Wait).
		s.captureWG.Wait()
		err := cmd.Wait()
		s.mu.Lock()
		s.done = true
		s.err = err
		result := ExecSessionResult{ID: s.id, Output: s.output.String(), Err: err}
		s.mu.Unlock()
		<-s.initialSnapshotDone
		s.mu.Lock()
		background := s.background
		s.mu.Unlock()
		if background {
			if handler := getExecSessionDoneHandler(); handler != nil {
				handler(result)
			}
		}
	}()

	time.Sleep(yieldDuration(yieldTimeMS))
	initial := s.snapshot(true)
	s.mu.Lock()
	s.background = !s.done
	background := s.background
	close(s.initialSnapshotDone)
	s.mu.Unlock()
	if background {
		initial += fmt.Sprintf("\n[session #%d in background; notified on exit. Meanwhile: write_stdin=input, status=progress, schedule op=once=periodic wake. Don't check without purpose.]", s.id)
	}
	return initial, false
}

func isSimpleSleep(command string) bool {
	fields := strings.Fields(command)
	if len(fields) != 2 || fields[0] != "sleep" {
		return false
	}
	if _, err := time.ParseDuration(fields[1]); err == nil {
		return true
	}
	_, err := strconv.ParseFloat(fields[1], 64)
	return err == nil
}

func isBackgroundedSimpleSleep(command string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(command), " \t")
	if !strings.HasSuffix(trimmed, "&") {
		return false
	}
	return isSimpleSleep(strings.TrimSuffix(trimmed, "&"))
}

func writeExecStdin(sessionID int, chars string, closeInput bool, yieldTimeMS int) (string, bool) {
	s := getExecSession(sessionID)
	if s == nil {
		return fmt.Sprintf("unknown session_id: %d", sessionID), true
	}
	if chars != "" {
		if _, err := io.WriteString(s.stdin, chars); err != nil {
			return err.Error(), true
		}
	}
	if closeInput {
		s.stdin.Close()
	}
	time.Sleep(yieldDuration(yieldTimeMS))
	return s.snapshot(false), false
}

func (s *execSession) capture(r io.Reader) {
	var buf [4096]byte
	for {
		n, err := r.Read(buf[:])
		if n > 0 {
			s.mu.Lock()
			s.output.Write(buf[:n])
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *execSession) snapshot(started bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var b bytes.Buffer
	if started {
		fmt.Fprintf(&b, "session_id: %d\n", s.id)
	}
	if s.output.Len() > 0 {
		b.WriteString(truncate(s.output.String()))
	}
	if s.done {
		if s.err != nil {
			fmt.Fprintf(&b, "\n[exited: %v]", s.err)
		} else {
			b.WriteString("\n[exited: 0]")
		}
		removeExecSession(s.id)
	}
	return b.String()
}

func getExecSession(id int) *execSession {
	execSessions.Lock()
	defer execSessions.Unlock()
	return execSessions.byID[id]
}

func removeExecSession(id int) {
	execSessions.Lock()
	delete(execSessions.byID, id)
	execSessions.Unlock()
}

func yieldDuration(ms int) time.Duration {
	if ms <= 0 {
		ms = defaultYieldMS
	}
	if ms > maxYieldMS {
		ms = maxYieldMS
	}
	return time.Duration(ms) * time.Millisecond
}

// NotifyOnExecSessionDone runs fn once when the session's process exits (or
// the session disappears). Returns false if no such session exists.
func NotifyOnExecSessionDone(id int, fn func(err error)) bool {
	return OnExecSessionDone(id, func(result ExecSessionResult) { fn(result.Err) })
}

// ExecSessionResult contains the captured output and exit error from a
// completed background command.
type ExecSessionResult struct {
	ID     int
	Output string
	Err    error
}

// OnExecSessionDone invokes fn once with the output when the process exits.
func OnExecSessionDone(id int, fn func(ExecSessionResult)) bool {
	execSessions.Lock()
	s, ok := execSessions.byID[id]
	execSessions.Unlock()
	if !ok {
		return false
	}
	go func() {
		for {
			time.Sleep(300 * time.Millisecond)
			s.mu.Lock()
			done, e := s.done, s.err
			output := s.output.String()
			s.mu.Unlock()
			if done {
				fn(ExecSessionResult{ID: id, Output: output, Err: e})
				return
			}
			execSessions.Lock()
			_, exists := execSessions.byID[id]
			execSessions.Unlock()
			if !exists {
				s.mu.Lock()
				output := s.output.String()
				err := s.err
				s.mu.Unlock()
				fn(ExecSessionResult{ID: id, Output: output, Err: err})
				return
			}
		}
	}()
	return true
}
