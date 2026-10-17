package tools

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// run_tests: run the project's test command and return a short structured
// summary - which tests failed and where - instead of hundreds of lines of
// runner output, with a timeout long enough for real suites.
//
// The harness does not guess the command: real projects run tests through
// Docker, scripts, monorepo tooling or CI-only setups that detection gets
// wrong. The agent passes the command it found (AGENTS.md, README, CI
// config, Makefile), or the user pins one as "test" in .sigmoidh/config.json.

const testTimeout = 15 * time.Minute

// ConfiguredTestCommand returns the pinned test command, if any.
func ConfiguredTestCommand(workDir string) string {
	return strings.TrimSpace(loadProjectConfig(workDir).Test)
}

func runTests(ctx context.Context, command, workDir string) (string, bool) {
	source := "given"
	if strings.TrimSpace(command) == "" {
		command, source = ConfiguredTestCommand(workDir), "from .sigmoidh/config.json"
		if command == "" {
			return "command is required: find how this project runs its tests (AGENTS.md, README, CI workflow, Makefile, package.json scripts) and pass that exact command.", true
		}
	}

	start := time.Now()
	out, exitErr, timedOut := runShell(ctx, command, workDir, testTimeout)
	took := time.Since(start).Round(100 * time.Millisecond)

	status := "PASSED"
	if exitErr != nil {
		status = "FAILED"
	}
	if timedOut {
		status = "TIMED OUT"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s (%s, %s)\n", status, command, source, took)
	summary := summarizeTestOutput(out)
	if summary != "" {
		b.WriteString(summary + "\n")
	}
	if exitErr != nil {
		// the tail is where runners print the verdict; less of it when the
		// summary already names the failures
		n := 15
		if summary == "" {
			n = 40
		}
		b.WriteString("output tail:\n" + tailLines(out, n))
	}
	return truncate(strings.TrimRight(b.String(), "\n")), exitErr != nil || timedOut
}

var (
	reGoFail      = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	reGoPkg       = regexp.MustCompile(`^(ok|FAIL|---)\s+(\S+)\s`)
	reGoLoc       = regexp.MustCompile(`^\s+(\S+\.go:\d+): (.*)`)
	rePytestFail  = regexp.MustCompile(`^(FAILED|ERROR) (\S+)(?: - (.*))?`)
	rePytestSum   = regexp.MustCompile(`^=+ (.*(?:passed|failed|error).*) =+$`)
	reJestFail    = regexp.MustCompile(`^\s*● (.+)$`)
	reJestSum     = regexp.MustCompile(`^Tests:\s+(.*)$`)
	reCargoFail   = regexp.MustCompile(`^test (\S+) \.\.\. FAILED`)
	reCargoSum    = regexp.MustCompile(`^test result: (.*)$`)
	reRspecFail   = regexp.MustCompile(`^rspec (\S+) # (.*)$`)
	reGenericSum  = regexp.MustCompile(`(?i)^\s*(\d+ (?:tests?|examples?|specs?)[^\n]*(?:fail|pass|error)[^\n]*)$`)
	reCompileErrs = regexp.MustCompile(`^(\S+\.\w+:\d+(?::\d+)?): (?:error: )?(.*)`)
)

// summarizeTestOutput pulls failing test names, their first error location
// and the runner's own summary line out of common runners' output.
func summarizeTestOutput(out string) string {
	var fails, sums, locs []string
	seen := map[string]bool{}
	addFail := func(s string) {
		if !seen[s] && len(fails) < 30 {
			seen[s] = true
			fails = append(fails, s)
		}
	}
	lastGoTest := ""
	for _, ln := range strings.Split(out, "\n") {
		switch {
		case reGoFail.MatchString(ln):
			lastGoTest = reGoFail.FindStringSubmatch(ln)[1]
			addFail(lastGoTest)
		case reGoLoc.MatchString(ln) && lastGoTest != "":
			m := reGoLoc.FindStringSubmatch(ln)
			if len(locs) < 30 {
				locs = append(locs, fmt.Sprintf("%s: %s: %s", lastGoTest, m[1], strings.TrimSpace(m[2])))
			}
		case reGoPkg.MatchString(ln) && strings.HasPrefix(ln, "FAIL"):
			sums = append(sums, strings.TrimSpace(ln))
		case rePytestFail.MatchString(ln):
			m := rePytestFail.FindStringSubmatch(ln)
			addFail(strings.TrimSpace(m[2] + " " + m[3]))
		case rePytestSum.MatchString(ln):
			sums = append(sums, rePytestSum.FindStringSubmatch(ln)[1])
		case reJestFail.MatchString(ln):
			addFail(reJestFail.FindStringSubmatch(ln)[1])
		case reJestSum.MatchString(ln):
			sums = append(sums, "Tests: "+reJestSum.FindStringSubmatch(ln)[1])
		case reCargoFail.MatchString(ln):
			addFail(reCargoFail.FindStringSubmatch(ln)[1])
		case reCargoSum.MatchString(ln):
			sums = append(sums, reCargoSum.FindStringSubmatch(ln)[1])
		case reRspecFail.MatchString(ln):
			m := reRspecFail.FindStringSubmatch(ln)
			addFail(m[1] + " " + m[2])
		case reGenericSum.MatchString(ln):
			sums = append(sums, strings.TrimSpace(ln))
		}
	}
	var b strings.Builder
	if len(fails) > 0 {
		fmt.Fprintf(&b, "failing (%d):\n", len(fails))
		for _, f := range fails {
			b.WriteString("- " + f + "\n")
		}
	}
	if len(locs) > 0 {
		b.WriteString("errors:\n")
		for _, l := range locs {
			b.WriteString("- " + l + "\n")
		}
	}
	if len(fails) == 0 {
		// build failures: compiler-style "file:line: message"
		n := 0
		for _, ln := range strings.Split(out, "\n") {
			if m := reCompileErrs.FindStringSubmatch(strings.TrimSpace(ln)); m != nil && n < 15 {
				if n == 0 {
					b.WriteString("errors:\n")
				}
				b.WriteString("- " + m[1] + ": " + m[2] + "\n")
				n++
			}
		}
	}
	if len(sums) > 0 {
		b.WriteString("summary: " + strings.Join(dedupe(sums), "; ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append([]string{fmt.Sprintf("… (%d earlier lines)", len(lines)-n)}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}

// runShell runs a command the way bash does (sandboxed when enabled, same
// destructive-command guard) with a custom timeout.
func runShell(ctx context.Context, command, workDir string, timeout time.Duration) (out string, exitErr error, timedOut bool) {
	if segment, blocked := destructiveCommandOnProtectedPath(command, workDir); blocked {
		return BlockedCommandRefusal(segment), fmt.Errorf("refused"), false
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if sandboxEnabled {
		cmd = exec.CommandContext(ctx, "bwrap", bashSandboxArgs(command, workDir)...)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Dir = workDir
	}
	b, err := cmd.CombinedOutput()
	return string(b), err, ctx.Err() == context.DeadlineExceeded
}
