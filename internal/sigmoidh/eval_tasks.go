package sigmoidh

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/litellm"
)

// Task evals measure the agent end to end on real work, not just prompt
// knowledge: each task copies a fixture project, runs the agent on a prompt,
// then scores the result with a shell check. Turns, tool calls, tool errors
// and tokens are recorded so efficiency regressions show up as numbers.
//
//	./sigmoidh -eval-tasks demo/evals/tasks.json [-eval-runs 3] [-model X]
//
// tasks.json:
//
//	[{
//	  "name": "iterative-fib",
//	  "fixture": "../fixture",            // directory, relative to this file
//	  "prompt": "make fib iterative",
//	  "check": "go run . | tail -1 | grep -qx 34 && ! grep -q 'fib(n-1)' main.go",
//	  "expect": ["iterative"],            // optional: substrings of the answer
//	  "max_turns": 30,                    // optional; default 40
//	  "timeout_sec": 300                  // optional; default 300
//	}]
//
// Each run's event log is written as JSON lines next to a results.json.

type evalTask struct {
	Name       string   `json:"name"`
	Fixture    string   `json:"fixture"`
	Prompt     string   `json:"prompt"`
	Check      string   `json:"check"`
	Expect     []string `json:"expect"`
	MaxTurns   int      `json:"max_turns"`
	TimeoutSec int      `json:"timeout_sec"`
}

type evalResult struct {
	Task       string `json:"task"`
	Run        int    `json:"run"`
	Pass       bool   `json:"pass"`
	Reason     string `json:"reason,omitempty"`
	Turns      int    `json:"turns"`
	ToolCalls  int    `json:"tool_calls"`
	ToolErrors int    `json:"tool_errors"`
	Tokens     int    `json:"tokens"`
	Ms         int64  `json:"ms"`
	Log        string `json:"log"`
}

// countingObserver records tool errors while forwarding to the JSON log.
type countingObserver struct {
	*jsonObserver
	toolErrors int
}

func (c *countingObserver) OnToolResult(name, out string, isErr bool, took time.Duration) {
	if isErr {
		c.toolErrors++
	}
	c.jsonObserver.OnToolResult(name, out, isErr, took)
}

func runTaskEval(path, provider, model string, runs int) {
	data, err := os.ReadFile(path)
	fatalIf(err)
	var tasks []evalTask
	fatalIf(json.Unmarshal(data, &tasks))
	if runs < 1 {
		runs = 1
	}
	if os.Getenv(evalSandboxEnv) == "" {
		reexecEvalSandboxed(path, tasks) // does not return
	}
	client, defModel, fallbacks, err := newClients(provider)
	fatalIf(err)
	if model == "" {
		model = defModel
	}
	outDir := os.Getenv(evalOutEnv)
	if outDir == "" {
		outDir, err = os.MkdirTemp("", "sigmoidh-eval-")
		fatalIf(err)
	}
	base := filepath.Dir(path)

	fmt.Printf("== task eval · %s · %d tasks × %d runs · logs in %s ==\n\n", model, len(tasks), runs, outDir)
	fmt.Printf("%-24s %-4s %5s %5s %5s %8s %7s  %s\n", "task", "run", "pass", "turns", "tools", "tokens", "secs", "note")

	var results []evalResult
	for _, t := range tasks {
		for r := 1; r <= runs; r++ {
			res := runOneTask(t, r, base, outDir, client, model, provider, fallbacks)
			results = append(results, res)
			mark := "FAIL"
			if res.Pass {
				mark = "ok"
			}
			fmt.Printf("%-24s %-4d %5s %5d %5d %8s %7.1f  %s\n", clip(t.Name, 24), r, mark, res.Turns,
				res.ToolCalls, humanNumber(res.Tokens), float64(res.Ms)/1000, res.Reason)
		}
	}

	pass, turns, tools, errs, toks := 0, 0, 0, 0, 0
	for _, r := range results {
		if r.Pass {
			pass++
		}
		turns += r.Turns
		tools += r.ToolCalls
		errs += r.ToolErrors
		toks += r.Tokens
	}
	n := max(len(results), 1)
	fmt.Printf("\npassed %d/%d · avg turns %.1f · avg tool calls %.1f · tool errors %d · avg tokens %s\n",
		pass, len(results), float64(turns)/float64(n), float64(tools)/float64(n), errs, humanNumber(toks/n))

	summary, _ := json.MarshalIndent(results, "", "  ")
	_ = os.WriteFile(filepath.Join(outDir, "results.json"), summary, 0o644)
	fmt.Printf("results: %s\n", filepath.Join(outDir, "results.json"))
	if pass < len(results) {
		os.Exit(1)
	}
}

func runOneTask(t evalTask, run int, base, outDir string, client *litellm.Client, model, provider string, fallbacks []providerAlt) evalResult {
	res := evalResult{Task: t.Name, Run: run}
	work, err := os.MkdirTemp("", "sigmoidh-task-")
	if err != nil {
		res.Reason = err.Error()
		return res
	}
	defer os.RemoveAll(work)
	if t.Fixture != "" {
		src := t.Fixture
		if !filepath.IsAbs(src) {
			src = filepath.Join(base, src)
		}
		if err := copyTree(src, work); err != nil {
			res.Reason = "fixture: " + err.Error()
			return res
		}
	}

	logPath := filepath.Join(outDir, fmt.Sprintf("%s-%d.jsonl", safeName(t.Name), run))
	logFile, err := os.Create(logPath)
	if err != nil {
		res.Reason = err.Error()
		return res
	}
	defer logFile.Close()
	res.Log = logPath
	obs := &countingObserver{jsonObserver: newJSONObserver(logFile)}

	sys, err := generateSystemPrompt(defaultSystemPrompt, false, work)
	if err != nil {
		res.Reason = err.Error()
		return res
	}
	debug, ro := false, false
	smh := NewSigmoidh(client, model, sys, work, obs, &ro, &debug)
	smh.noHistory = true
	smh.SetProviders(providerAlt{Name: provider, Client: client, Model: model}, fallbacks)
	maxTurns := t.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 40
	}
	smh.SetMaxTurns(maxTurns)
	timeout := time.Duration(max(t.TimeoutSec, 0)) * time.Second
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	runErr := smh.Run(ctx, t.Prompt)
	st := smh.stats
	res.Turns, res.ToolCalls, res.Tokens, res.Ms = st.TotalTurns, st.TotalToolCalls, st.TotalTokens, st.TotalDuration.Milliseconds()
	res.ToolErrors = obs.toolErrors
	answer := ""
	if n := len(smh.lastTexts); n > 0 {
		answer = smh.lastTexts[n-1]
	}
	obs.done(answer, st)

	if runErr != nil {
		res.Reason = "agent: " + clip(runErr.Error(), 80)
		return res
	}
	for _, want := range t.Expect {
		if !strings.Contains(strings.ToLower(answer), strings.ToLower(want)) {
			res.Reason = fmt.Sprintf("answer lacks %q", want)
			return res
		}
	}
	if strings.TrimSpace(t.Check) != "" {
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer ccancel()
		cmd := exec.CommandContext(cctx, "sh", "-c", t.Check)
		cmd.Dir = work
		if out, err := cmd.CombinedOutput(); err != nil {
			res.Reason = "check failed: " + clip(strings.TrimSpace(string(out)), 80)
			return res
		}
	}
	res.Pass = true
	return res
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == ' ' || r == '\\' {
			return '-'
		}
		return r
	}, s)
}

// copyTree copies a fixture directory (regular files and dirs only).
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, _ := d.Info()
		mode := fs.FileMode(0o644)
		if info != nil {
			mode = info.Mode().Perm()
		}
		return os.WriteFile(target, data, mode)
	})
}

const (
	evalSandboxEnv = "SIGMOIDH_EVAL_SANDBOXED"
	evalOutEnv     = "SIGMOIDH_EVAL_OUT"
)

// reexecEvalSandboxed re-runs this eval inside bubblewrap so an agent that
// misbehaves cannot touch the real system: / is read-only, the home
// directory is an empty tmpfs (no access to keys or other projects), /tmp is
// private, and only the fixtures, the binary and the API-key file are bound
// in read-only. The results directory is the single writable host path.
// The network stays up (the model API needs it). Set SIGMOIDH_EVAL_SANDBOXED=1
// to run unsandboxed deliberately.
func reexecEvalSandboxed(tasksPath string, tasks []evalTask) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		fatal(fmt.Errorf("task evals run inside bubblewrap so the agent cannot modify your system; install bwrap (or set %s=1 to run unsandboxed at your own risk)", evalSandboxEnv))
	}
	exe, err := os.Executable()
	fatalIf(err)
	exe, _ = filepath.EvalSymlinks(exe)
	absTasks, err := filepath.Abs(tasksPath)
	fatalIf(err)
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()

	outDir, err := os.MkdirTemp("", "sigmoidh-eval-")
	fatalIf(err)

	args := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--tmpfs", home,
		"--bind", outDir, outDir,
		"--ro-bind", exe, exe,
		"--ro-bind", filepath.Dir(absTasks), filepath.Dir(absTasks),
	}
	for _, t := range tasks {
		if t.Fixture == "" {
			continue
		}
		f := t.Fixture
		if !filepath.IsAbs(f) {
			f = filepath.Join(filepath.Dir(absTasks), f)
		}
		if f, err = filepath.Abs(f); err == nil {
			args = append(args, "--ro-bind", f, f)
		}
	}
	// API keys: SIGMOIDH_ENV_FILE, else the global env file, else ./.env
	for _, envFile := range []string{os.Getenv("SIGMOIDH_ENV_FILE"), filepath.Join(home, ".sigmoidh", ".env"), filepath.Join(cwd, ".env")} {
		if envFile == "" {
			continue
		}
		if _, err := os.Stat(envFile); err == nil {
			args = append(args, "--ro-bind", envFile, filepath.Join(home, ".sigmoidh", ".env"))
			break
		}
	}
	args = append(args,
		"--chdir", filepath.Dir(absTasks),
		"--setenv", evalSandboxEnv, "1",
		"--setenv", evalOutEnv, outDir,
		"--setenv", "HOME", home,
		"--unsetenv", "SIGMOIDH_ENV_FILE", // bound as the global env file instead
		"--die-with-parent",
		"--", exe,
	)
	// same flags, with the tasks path made absolute
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		if (a == "-eval-tasks" || a == "--eval-tasks") && i+1 < len(os.Args) {
			args = append(args, a, absTasks)
			i++
			continue
		}
		if strings.HasPrefix(a, "-eval-tasks=") || strings.HasPrefix(a, "--eval-tasks=") {
			a = "-eval-tasks=" + absTasks
		}
		args = append(args, a)
	}
	cmd := exec.Command(bwrap, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fatal(err)
	}
	os.Exit(0)
}
