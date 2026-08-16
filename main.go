package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const defaultSystem = `You are a terminal coding and task agent.
Use the bash, read, and write tools to inspect and change the filesystem.
Prefer small, verifiable steps. Be direct and concise in your replies.`

func getProjectTree(level int) (string, error) {
	cmd := exec.Command("tree", "-L", fmt.Sprintf("%d", level))
	cmd.Dir = "."
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func generateSystemPrompt(basePrompt string) (string, error) {
	projectTree, err := getProjectTree(2)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(
		"%s\n\npwd:%s\nProject tree:\n%s",
		basePrompt,
		os.Getenv("PWD"),
		projectTree,
	), nil
}

func main() {
	provider := flag.String("provider", "openrouter", "llm provider: anthropic, openai, or openrouter")
	model := flag.String("model", "", "model name, overrides the provider default")
	system := flag.String("system", "", "path to a system prompt file")
	flag.Parse()

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
	prompt, err = generateSystemPrompt(prompt)
	fmt.Println(prompt)

	smh := NewSigmoidh(client, defaultModel, prompt)
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
		if line == "exit" || line == "quit" {
			return
		}
		if err := smh.Run(ctx, line); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
