package tools

import (
	"context"
	"fmt"
	"os/exec"
)

func runBash(ctx context.Context, command string, workDir string) (string, bool) {
	if _, blocked := destructiveCommandOnProtectedPath(command, workDir); blocked {
		return refusalBashDestructive, true
	}

	ctx, cancel := context.WithTimeout(ctx, bashTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bwrap", bashSandboxArgs(command, workDir)...)
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
	}
	return args
}
