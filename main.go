package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

import "golang.design/x/clipboard"

func main() {
	provider := flag.String("provider", "openrouter", "llm provider: anthropic, openai, or openrouter")
	model := flag.String("model", "", "model name, overrides the provider default")
	system := flag.String("system", "", "path to a system prompt file")
	version := flag.Bool("version", false, "print version and exit")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	if *version {
		fmt.Println("Sigmoidh v0.1.0")
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

	prompt := defaultSystem
	if *system != "" {
		b, err := os.ReadFile(*system)
		if err != nil {
			fatal(err)
		}
		prompt = string(b)
	}
	prompt, err = generateSystemPrompt(prompt, workDir)
	if err != nil {
		fatal(err)
	}
	fmt.Println(prompt)

	smh := NewSigmoidh(client, defaultModel, prompt, workDir, debug)
	defer smh.Close()
	ctx := context.Background()

	if args := flag.Args(); len(args) > 0 {
		if err := smh.Run(ctx, strings.Join(args, " ")); err != nil {
			fatal(err)
		}
		return
	}

	repl(ctx, smh, *provider, defaultModel)
}

func repl(ctx context.Context, smh *Sigmoidh, provider, model string) {
	fmt.Fprintf(os.Stderr, "[ OK ] Sigmoidh ready | (%s#%s) | Ctrl-D to Exit\n", provider, model)

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
			}
			continue
		}

		if line == "exit" || line == "quit" {
			return
		}
		if err := smh.Run(ctx, line); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
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
