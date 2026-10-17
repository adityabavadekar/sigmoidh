package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func runBash(ctx context.Context, command string, workDir string) (string, bool) {
	if strings.TrimSpace(command) == "" {
		return "Refused: empty command. Provide the shell command to run.", true
	}
	if isBackgroundedSimpleSleep(command) {
		return "Refused: plain sleep +'&' is still just a wait. Run bash with a plain sleep (blocks, returns after the wait), or schedule op=once for timer/reminder.", true
	}
	if segment, blocked := destructiveCommandOnProtectedPath(command, workDir); blocked {
		return BlockedCommandRefusal(segment), true
	}

	ctx, cancel := context.WithTimeout(ctx, bashTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if sandboxEnabled {
		cmd = exec.CommandContext(ctx, "bwrap", bashSandboxArgs(command, workDir)...)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Dir = workDir
	}
	out, err := cmd.CombinedOutput()
	text := truncate(string(out))

	if ctx.Err() == context.DeadlineExceeded {
		return text + "\n[timed out]", true
	}
	if err != nil {
		return text + fmt.Sprintf("\n[%v]", err), true
	}
	return text, false
}

// sandboxBinds are extra read-only host paths exposed inside the sandbox
// (e.g. the spill directory, so the model can follow output pointers).
var sandboxBinds []string

// SetSandboxBinds registers additional read-only binds for bash/exec.
func SetSandboxBinds(paths ...string) {
	sandboxBinds = append(sandboxBinds[:0], paths...)
}

func bashSandboxArgs(command, workDir string) []string {
	args := []string{
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",

		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/sbin", "/sbin",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind", "/lib64", "/lib64",
		"--ro-bind", "/etc", "/etc",
		"--ro-bind", "/run", "/run",

		"--bind", workDir, workDir,
		"--chdir", workDir,
	}
	for _, p := range sandboxBinds {
		args = append(args, "--ro-bind", p, p)
	}
	args = append(args,
		"--unshare-user",
		"--unshare-pid",
		"--unshare-uts",
		"--unshare-ipc",

		"--uid", "0",
		"--gid", "0",

		"--die-with-parent",
		"--new-session",
		"--cap-drop", "ALL",
		"--",
		"sh", "-c", command,
	)
	return args
}
