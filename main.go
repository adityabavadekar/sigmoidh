// Copyright 2026 Aditya Bavadekar
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	agenttools "github.com/adityabavadekar/sigmoidh/tools"
	"github.com/chzyer/readline"
	"golang.design/x/clipboard"
	"golang.org/x/term"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
)

const VERSION = "v0.5.0"
const EXIT_MESSAGE = "Exiting Sigmoidh. Goodbye!"

var errExitRequest = errors.New(EXIT_MESSAGE)

func main() {
	loadDotEnvGlobal()

	provider := flag.String("provider", "openrouter", "llm provider: anthropic, openai, or openrouter")
	model := flag.String("model", "", "model name, overrides the provider default")
	system := flag.String("system", "", "path to a system prompt file")
	oneShotPrompt := flag.String("prompt", "", "run a single prompt and exit")
	listTools := flag.Bool("list-tools", false, "list available tools and exit")
	listSessionsFlag := flag.Bool("list-sessions", false, "list sessions run in the current directory and exit")
	importFlag := flag.String("import", "", "ingest a foreign session: opencode[:id-prefix] | claude:<file> | opencode-list")
	webAddr := flag.String("web", "", "serve a live read-only web UI at this address (e.g. 127.0.0.1:8427)")
	tuiMode := flag.Bool("tui", false, "run the full-screen tview interface")
	evalPrompt := flag.String("eval-prompt", "", "run prompt-knowledge probes from a JSON file and exit")
	evalVariants := flag.String("eval-variants", "", "comma-separated prompt variants to A/B (default: all)")
	sessionName := flag.String("session", "", "resume a session by table number or id (e.g. 1 or 2026-08-22_23-05-33)")
	readOnlyMode := flag.Bool("readonly", false, "enable read-only mode (only read tool allowed)")
	noSandbox := flag.Bool("no-sandbox", false, "run bash/exec without the bubblewrap sandbox (full host access; use with care)")
	continueLast := flag.Bool("continue", false, "resume the most recent session in this directory")
	resumeFlag := flag.Bool("resume", false, "alias for -continue")
	ping := flag.Bool("ping", false, "ping me once the assigned task is completed")
	version := flag.Bool("version", false, "print version and exit")
	debug := flag.Bool("debug", false, "enable debug logging")

	flag.Usage = func() {
		out := flag.CommandLine.Output()
		bin := filepath.Base(os.Args[0])
		fmt.Fprintf(out, "Sigmoidh %s — terminal coding agent\n\n", VERSION)
		fmt.Fprintf(out, "Usage:\n  %s [flags] [prompt]\n\n", bin)
		fmt.Fprintln(out, "Examples:")
		fmt.Fprintf(out, "  %s                            start an interactive session\n", bin)
		fmt.Fprintf(out, "  %s \"fix the failing test\"      run one prompt and exit\n", bin)
		fmt.Fprintf(out, "  %s -list-sessions             list sessions run in this directory\n", bin)
		fmt.Fprintf(out, "  %s -continue                  resume the latest one (-resume works too)\n", bin)
		fmt.Fprintf(out, "  %s -session 2                 resume by table number or id\n\n", bin)
		fmt.Fprintln(out, "Flags:")
		flag.PrintDefaults()
	}

	flag.Parse()

	if *version {
		fmt.Printf("Sigmoidh %s\n", VERSION)
		os.Exit(0)
	}

	if *listTools {
		agenttools.SetReadOnlyMode(*readOnlyMode)
		agenttools.SetSandboxEnabled(!*noSandbox)
		agenttools.PrintAvailableTools()
		os.Exit(0)
	}

	workDir, err := os.Getwd()
	if err != nil {
		fatal(err)
	}

	ensureMemoryFileDir()
	pruneSpillFiles()
	agenttools.SetAllowedPaths(memoryFilePath())
	os.MkdirAll(spillDir(), 0o755)
	agenttools.SetSandboxBinds(spillDir()) // spilled outputs stay reachable via bash
	agenttools.SetSandboxEnabled(!*noSandbox)
	if *noSandbox {
		bannerNoSandbox = true
		fmt.Fprintf(os.Stderr, "%s  ⚠ sandbox disabled — bash/exec run with full host access%s\n", ansiYellow, ansiReset)
	}

	if *evalPrompt != "" {
		runPromptEval(*evalPrompt, *provider, *model, *evalVariants)
		os.Exit(0)
	}

	if *importFlag != "" {
		runImport(*importFlag)
		os.Exit(0)
	}

	if *listSessionsFlag {
		infos, err := listSessionInfos(filepath.Join(homeDir(), ".sigmoidh", "history"), workDir)
		if err != nil {
			fatal(err)
		}
		if len(infos) == 0 {
			fmt.Printf("No sessions recorded in %s yet.\n", workDir)
			os.Exit(0)
		}
		printSessionTable(infos)
		os.Exit(0)
	}

	client, defaultModel, fallbacks, err := newClients(*provider)
	if err != nil {
		fatal(err)
	}
	if *model != "" {
		defaultModel = *model
	}

	systemPrompt := defaultSystemPrompt
	if *system != "" {
		b, err := os.ReadFile(*system)
		if err != nil {
			fatal(err)
		}
		systemPrompt = string(b)
	}
	systemPrompt, err = generateSystemPrompt(systemPrompt, *readOnlyMode, workDir)
	if err != nil {
		fatal(err)
	}

	if *debug {
		fmt.Println(systemPrompt)
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	if *tuiMode && !interactive {
		fatal(fmt.Errorf("-tui needs an interactive terminal"))
	}

	var observer AgentObserver
	t := (*TUI)(nil)
	if *tuiMode {
		t = NewTUI(*provider, defaultModel, *readOnlyMode)
		observer = t.obs
	} else {
		observer = NewUi(os.Stderr, *debug)
	}
	if *webAddr != "" {
		wo, err := StartWeb(*webAddr)
		if err != nil {
			fatal(err)
		}
		observer = &multiObserver{observers: []AgentObserver{observer, wo}}
		fmt.Fprintf(os.Stderr, "%s  ● live view: http://%s%s\n", ansiCyan, *webAddr, ansiReset)
	}
	smh := NewSigmoidh(client, defaultModel, systemPrompt, workDir, observer, readOnlyMode, debug)
	if t != nil {
		t.BindAgent(smh)
	}
	smh.SetProviders(providerAlt{Name: *provider, Client: client, Model: defaultModel}, fallbacks)
	defer smh.Close()

	if *sessionName != "" || *continueLast || *resumeFlag {
		histDir := filepath.Join(smh.AppDir(), "history")
		var path string
		var err error
		switch {
		case *sessionName != "":
			path, err = resolveSessionArg(histDir, smh.workDir, *sessionName)
		default:
			infos, ierr := listSessionInfos(histDir, smh.workDir)
			if ierr != nil || len(infos) == 0 {
				err = fmt.Errorf("no sessions recorded in %s", smh.workDir)
			} else {
				path = infos[0].Path
			}
		}
		if err != nil {
			fatal(err)
		}
		n, rerr := smh.ResumeFrom(path)
		if rerr != nil {
			fatal(rerr)
		}
		fmt.Fprintf(os.Stderr, "%s● Resumed %d messages from %s%s\n",
			ansiDim, n, filepath.Base(path), ansiReset)
	}

	// Ctrl+C cancels the in-flight run gracefully; readline handles ^C at the prompt itself.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if args := flag.Args(); len(args) > 0 {
		*oneShotPrompt = strings.Join(args, " ")
	}

	if !interactive {
		runNonInteractive(ctx, smh, *oneShotPrompt)
		return
	}

	if t != nil {
		defer func() { _ = recover() }() // tcell panics on teardown are cosmetic
		if err := t.Run(); err != nil {
			fatal(err)
		}
		return
	}

	repl(ctx, smh, *provider, defaultModel, *oneShotPrompt, readOnlyMode, ping)
}

// runNonInteractive drives the agent as a filter: prompt comes from -prompt
// or all of stdin; only the final response text is printed on stdout.
func runNonInteractive(ctx context.Context, smh *Sigmoidh, oneShot string) {
	if strings.TrimSpace(oneShot) == "" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fatal(err)
		}
		oneShot = strings.TrimSpace(string(b))
	}
	if oneShot == "" {
		fatal(fmt.Errorf("no prompt given (pipe input or use -prompt)"))
	}
	if err := smh.Run(ctx, oneShot); err != nil {
		fatal(err)
	}
}

func repl(ctx context.Context, smh *Sigmoidh, provider, model string, oneShotPrompt string, readOnlyMode, ping *bool) {
	if oneShotPrompt != "" {
		notifyUserInput(smh.observer, oneShotPrompt)
		if err := smh.Run(ctx, oneShotPrompt); err != nil {
			smh.fireRunCompleteTriggers()
			fatal(err)
		}
		smh.fireRunCompleteTriggers()
		printResumeHint(smh)
		return
	}

	smh.observer.OnBootstrap(VERSION, provider, model, *readOnlyMode)

	// Bracketed paste: giant blocks land as one input via [[paste:id]] tokens.
	stdin := io.ReadCloser(os.Stdin)
	if enableBracketedPaste(os.Stdin) {
		stdin = newPasteReader(os.Stdin, replPastes)
		defer disableBracketedPaste(os.Stdin)
	}

	rl, err := readline.NewEx(&readline.Config{
		Prompt:              fmt.Sprintf("%s❯%s ", ansiCyan, ansiReset),
		HistoryFile:         smh.ReplHistoryFilePath(),
		HistoryLimit:        1000,
		InterruptPrompt:     "^C",
		EOFPrompt:           "exit",
		AutoComplete:        slashCompleter{},
		FuncFilterInputRune: translateKey,
		Stdin:               stdin,
	})
	if err != nil {
		fatal(fmt.Errorf("failed to initialize readline: %w", err))
	}
	defer rl.Close()

	for {
		line, err := rl.Readline()
		if err != nil {
			if errors.Is(err, readline.ErrInterrupt) {
				if len(line) > 0 {
					// Ctrl+C on non-empty line -> just clear line
					continue
				}
				// Ctrl+C on empty line -> exit
				break
			}
			// Ctrl+D (EOF) -> exit
			if errors.Is(err, io.EOF) {
				break
			}
			fmt.Fprintf(os.Stderr, "%s✖ Read error:%s %v\n", ansiRed, ansiReset, err)
			break
		}
		raw := line
		line = replPastes.expand(line)
		pasted := replPastes.wasExpanded(raw, line)
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Pasted blocks are always prompts, never commands.
		if !pasted && strings.HasPrefix(line, "/") {
			err := handleSlashCommand(ctx, smh, line)
			switch {
			case errors.Is(err, errExitRequest):
				smh.OnRunComplete()
				printResumeHint(smh)
				return
			case err != nil:
				fmt.Fprintln(os.Stderr, "error:", err)
			}
			continue
		}

		// Pasted blocks are always prompts, never commands or exit words.
		if !pasted && bareExitWords[line] {
			break
		}
		notifyUserInput(smh.observer, line)
		runErr := smh.Run(ctx, line)
		smh.fireRunCompleteTriggers()
		if runErr != nil {
			fmt.Fprintln(os.Stderr, "error:", runErr)
		}

		if *ping {
			PingUser()
		}
	}
	smh.OnRunComplete()
	printResumeHint(smh)
}

// classifyUserInput distinguishes real prompts from control lines (slash
// commands, exit/quit) so views can color them differently.
func classifyUserInput(line string) string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "/") {
		return "command"
	}
	switch strings.ToLower(line) {
	case "exit", "quit", "q":
		return "command"
	}
	return "message"
}

// handleBoolToggle implements /<toggle> [on|off] for any boolean UI flag.
func handleBoolToggle(get func() bool, set func(bool), label, usageName string, args []string) error {
	visible := get()
	switch len(args) {
	case 0:
	case 1:
		switch strings.ToLower(args[0]) {
		case "on":
			visible = true
		case "off":
			visible = false
		default:
			return fmt.Errorf("usage: %s [on|off]", usageName)
		}
	default:
		return fmt.Errorf("usage: %s [on|off]", usageName)
	}
	set(visible)
	state := "off"
	if visible {
		state = "on"
	}
	fmt.Printf("%s✓%s %s: %s\n", ansiGreen, ansiReset, label, state)
	return nil
}

// notifyUserInput pushes the user's prompt to observers that care (web view).
func notifyUserInput(o AgentObserver, text string) {
	if uo, ok := o.(interface{ OnUserInput(string, string) }); ok {
		uo.OnUserInput(classifyUserInput(text), text)
	}
}

// printResumeHint reminds the user how to pick this conversation back up.
func printResumeHint(smh *Sigmoidh) {
	if name := smh.CurrentSessionName(); name != "" {
		fmt.Fprintf(os.Stderr, "%s  ↳ resume this chat: %ssigmoidh -session %s%s\n",
			ansiDim, ansiBold, name, ansiReset)
	}
}

func copyToClipboard(text string) error {
	if err := clipboard.Init(); err != nil {
		return err
	}
	clipboard.Write(clipboard.FmtText, []byte(text))
	return nil
}

func handleResume(smh *Sigmoidh, args []string) error {
	infos, err := smh.sessionsHere()
	if err != nil {
		return err
	}
	pick := 0
	switch {
	case len(args) == 0: // newest
	case args[0] == "list":
		printSessionTable(infos)
		return nil
	default:
		n, aerr := strconv.Atoi(args[0])
		if aerr != nil || n < 1 {
			return fmt.Errorf("usage: /resume [n|list]")
		}
		pick = n - 1
	}
	if pick >= len(infos) {
		return fmt.Errorf("session %d out of range (have %d)", pick+1, len(infos))
	}
	n, rerr := smh.ResumeFrom(infos[pick].Path)
	if rerr != nil {
		return rerr
	}
	fmt.Printf("Resumed %d messages from %s (%s)\n", n, infos[pick].Name, relativeTime(infos[pick].At))
	return nil
}

func handleSlashCommand(_ context.Context, smh *Sigmoidh, line string) error {
	line = line[1:]
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return fmt.Errorf("empty command")
	}
	cmd := parts[0]
	args := parts[1:]

	switch cmd {
	case "help":
		printSlashHelp(os.Stderr)
		return nil

	case "thinking":
		return handleBoolToggle(smh.observer.ThinkingVisible,
			smh.observer.SetThinkingVisible, "thinking display", "/thinking", args)
	case "statsline":
		return handleBoolToggle(smh.observer.UsageVisible,
			smh.observer.SetUsageVisible, "stats line", "/statsline", args)
	case "sandbox":
		return handleBoolToggle(agenttools.SandboxEnabled,
			agenttools.SetSandboxEnabled, "bash sandbox", "/sandbox", args)
	case "resume":
		return handleResume(smh, args)

	case "exit":
		return errExitRequest
	case "usage":
		smh.PrintStats()
		return nil
	case "copy":
		if len(smh.lastTexts) == 0 {
			return fmt.Errorf("no previous responses to copy")
		}

		if len(args) == 0 {
			return copyToClipboard(smh.lastTexts[len(smh.lastTexts)-1])
		}

		switch args[0] {
		case "all":
			return copyToClipboard(strings.Join(smh.lastTexts, "\n"))
		case "last":
			return copyToClipboard(smh.lastTexts[len(smh.lastTexts)-1])
		case "num":
			count := 1
			if len(args) > 1 {
				if n, err := strconv.Atoi(args[1]); err == nil && n > 0 {
					count = n
				}
			}
			if count > len(smh.lastTexts) {
				count = len(smh.lastTexts)
			}
			start := len(smh.lastTexts) - count
			return copyToClipboard(strings.Join(smh.lastTexts[start:], "\n"))
		default:
			return fmt.Errorf("usage: /copy [all|last|num <n>]")
		}
	default:
		return fmt.Errorf("unknown slash command: /%s", cmd)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// runImport converts a foreign transcript into a sigmoidh history file.
func runImport(arg string) {
	switch {
	case arg == "opencode-list":
		if err := listOpenCodeSessions(); err != nil {
			fatal(err)
		}
	case arg == "opencode", strings.HasPrefix(arg, "opencode:"):
		prefix := ""
		if i := strings.Index(arg, ":"); i >= 0 {
			prefix = arg[i+1:]
		}
		path, name, n, err := importOpenCode(prefix)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("%s✓%s imported %d entries → %s\n  resume: %ssigmoidh -session %s%s\n",
			ansiGreen, ansiReset, n, path, ansiBold, name, ansiReset)
	case strings.HasPrefix(arg, "claude:"):
		path, name, n, err := importClaudeCode(strings.TrimPrefix(arg, "claude:"))
		if err != nil {
			fatal(err)
		}
		fmt.Printf("%s✓%s imported %d entries → %s\n  resume: %ssigmoidh -session %s%s\n",
			ansiGreen, ansiReset, n, path, ansiBold, name, ansiReset)
	default:
		fatal(fmt.Errorf("unknown -import target %q (use opencode[:id] | claude:<file> | opencode-list)", arg))
	}
}
