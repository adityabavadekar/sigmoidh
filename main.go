package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	agenttools "github.com/adityabavadekar/sigmoidh/tools"
	"os"
	"strconv"
	"strings"
)

import "golang.design/x/clipboard"

const VERSION = "v0.4.0"
const EXIT_MESSAGE = "Exiting Sigmoidh. Goodbye!"

func main() {
	provider := flag.String("provider", "openrouter", "llm provider: anthropic, openai, or openrouter")
	model := flag.String("model", "", "model name, overrides the provider default")
	system := flag.String("system", "", "path to a system prompt file")
	oneShotPrompt := flag.String("prompt", "", "run a single prompt and exit")
	listTools := flag.Bool("list-tools", false, "list available tools and exit")
	readOnlyMode := flag.Bool("readonly", false, "enable read-only mode (only read tool allowed)")
	ping := flag.Bool("ping", false, "ping me once the assigned task is completed")
	version := flag.Bool("version", false, "print version and exit")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	if *version {
		fmt.Printf("Sigmoidh %s\n", VERSION)
		os.Exit(0)
	}

	if *listTools {
		agenttools.SetReadOnlyMode(*readOnlyMode)
		agenttools.PrintAvailableTools()
		os.Exit(0)
	}

	workDir, err := os.Getwd()
	if err != nil {
		fatal(err)
	}

	client, defaultModel, err := newClient(*provider)
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
	// TODO: tty check
	observer := NewUi(os.Stderr, *debug)
	smh := NewSigmoidh(client, defaultModel, systemPrompt, workDir, observer, readOnlyMode, debug)
	defer smh.Close()
	ctx := context.Background()

	if args := flag.Args(); len(args) > 0 {
		*oneShotPrompt = strings.Join(args, " ")
	}

	repl(ctx, smh, *provider, defaultModel, *oneShotPrompt, readOnlyMode, ping, debug)
}

func repl(ctx context.Context, smh *Sigmoidh, provider, model string, oneShotPrompt string, readOnlyMode, ping, debug *bool) {
	if oneShotPrompt != "" {
		if err := smh.Run(ctx, oneShotPrompt); err != nil {
			fatal(err)
		}
		return
	}

	smh.observer.OnBootstrap(VERSION, provider, model, *readOnlyMode)

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "/") {
			if err := handleSlashCommand(ctx, smh, line); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				if err.Error() == EXIT_MESSAGE {
					break
				}
			}
			continue
		}

		if line == "exit" || line == "quit" {
			break
		}
		if err := smh.Run(ctx, line); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}

		if *ping {
			PingUser()
		}
	}
	smh.OnRunComplete()
}

func copyToClipboard(text string) error {
	if err := clipboard.Init(); err != nil {
		return err
	}
	clipboard.Write(clipboard.FmtText, []byte(text))
	return nil
}

func handleSlashCommand(ctx context.Context, smh *Sigmoidh, line string) error {
	line = line[1:]
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return fmt.Errorf("empty command")
	}
	cmd := parts[0]
	args := parts[1:]

	switch cmd {
	case "help":
		fmt.Println("Available commands:")
		fmt.Println("/help  -                    Show this help message")
		fmt.Println("/usage -                    Show usage statistics")
		fmt.Println("/copy [all|last|num <n>] -  Copy previous responses to clipboard")
		fmt.Println("/exit  -                    Exit the program")
		return nil

	case "exit":
		return fmt.Errorf(EXIT_MESSAGE)
	case "usage":
		fmt.Print("[ USAGE ]")
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
