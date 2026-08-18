package tools

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

const (
	defaultYieldMS = 250
	maxYieldMS     = 5000
)

type execSession struct {
	id     int
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel func()

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

func startExecSession(command string, yieldTimeMS int, workDir string) (string, bool) {
	if command == "" {
		return "command cannot be empty", true
	}
	if _, blocked := destructiveCommandOnProtectedPath(command, workDir); blocked {
		return refusalBashDestructive, true
	}

	cmd := exec.Command("bwrap", bashSandboxArgs(command, workDir)...)
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

	s := &execSession{cmd: cmd, stdin: stdin}
	execSessions.Lock()
	execSessions.next++
	s.id = execSessions.next
	execSessions.byID[s.id] = s
	execSessions.Unlock()

	if err := cmd.Start(); err != nil {
		removeExecSession(s.id)
		return err.Error(), true
	}
	go s.capture(stdout)
	go s.capture(stderr)
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		s.done = true
		s.err = err
		s.mu.Unlock()
	}()

	time.Sleep(yieldDuration(yieldTimeMS))
	return s.snapshot(true), false
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
