package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	agenttools "github.com/adityabavadekar/sigmoidh/tools"
	"github.com/voocel/litellm"
)

// Prompt-knowledge evaluation: proves the system prompt teaches the model its
// environment well enough that it answers WITHOUT wasting tool calls.
//
//	./sigmoidh -eval-prompt probes.json [-provider openrouter -model X]
//
// probes.json: [{"q": "...", "expect": ["substring", ...]}, ...]
// A probe passes only if the reply contains every expected substring AND the
// model issued zero tool calls. -eval-variants A/Bs built-in prompt variants;
// "baseline" runs with an empty system prompt.

// promptVariants is the A/B matrix for -eval-prompt. "original" is the
// historical default prompt; the rest are experiments over the same info.

const variantOriginal = `You are Sigmoidh, a terminal coding and task agent.
Help the user with programming, debugging, and technical tasks.
Use the provided terminal tools when filesystem or command-line access is needed.
Prefer the fewest tool calls necessary. Verify important changes, but do not repeat checks or investigate already-confirmed information.
Be direct and concise in your replies.
Always ask BEFORE performing destructive actions like deleting files or making irreversible changes with git.

Keep reasoning terse. Do not restate the user's request or explain obvious tool choices. Do not explain obvious actions or tool choices.`

var promptVariants = map[string]string{
	"original": variantOriginal,

	"original_trust": variantOriginal + `
The environment facts below are authoritative: trust them instead of re-checking with tools, and treat unlisted binaries as unavailable.`,

	"lean": defaultSystemPrompt,

	"lean_policy": `You are Sigmoidh, a terminal coding agent. Use tools only when the answer is not in your context; fewest calls possible. The environment facts below are authoritative: never run pwd, git status, or binary checks to confirm them; unlisted binaries are unavailable. Verify edits once; never repeat confirmed checks. Reply direct and concise: no filler, no restating requests, no narrating obvious steps. Keep reasoning terse. Ask before destructive or irreversible actions (deletions, resets, force-push).`,

	"bulleted": `You are Sigmoidh, a terminal coding agent.
Rules:
- Use tools for anything needing filesystem or shell access; fewest calls possible.
- Environment facts below are current and complete: never verify them with tools. Unlisted binaries are unavailable.
- Verify changes once; never repeat confirmed checks.
- Reply direct and concise: no filler, no restating requests, no narrating obvious steps.
- Keep reasoning terse.
- Ask before destructive or irreversible actions (deletions, resets, force-push).`,

	"minimal": `You are Sigmoidh, a terminal coding agent. Use tools sparingly; facts below are current. Be concise and terse. Ask before destructive actions.`,

	"conversational": `You're Sigmoidh, a hands-on coding buddy working in the user's terminal. You can see all the environment details listed below, so there's no need to double-check them with commands; just use them. When you do need tools, keep it to the minimum. Answer straight, skip the fluff and the play-by-play, think briefly, and check with the user before doing anything destructive like deleting files or rewriting history.`,
}

var variantOrder = []string{"original", "original_trust", "lean", "lean_policy", "bulleted", "minimal", "conversational"}

type probe struct {
	Q      string   `json:"q"`
	Expect []string `json:"expect"`
}

func runPromptEval(evalPath, provider, model, variantsCSV string) {
	data, err := os.ReadFile(evalPath)
	fatalIf(err)
	var probes []probe
	fatalIf(json.Unmarshal(data, &probes))

	client, defModel, _, err := newClients(provider)
	fatalIf(err)
	if model == "" {
		model = defModel
	}
	workDir, err := os.Getwd()
	fatalIf(err)

	variants := variantOrder
	if variantsCSV != "" {
		variants = nil
		for _, v := range strings.Split(variantsCSV, ",") {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if _, ok := promptVariants[v]; !ok && v != "baseline" {
				fatal(fmt.Errorf("unknown variant %q", v))
			}
			variants = append(variants, v)
		}
	}

	fmt.Printf("== prompt eval · %s · %d probes · cwd=%s ==\n\n", model, len(probes), workDir)

	tools := agenttools.GetAvailableTools()
	type score struct{ pass, total int }
	scores := map[string]score{}
	var order []string

	for _, name := range variants {
		sysp := ""
		if name != "baseline" {
			sysp = promptVariants[name]
			if name != "lean" || os.Getenv("SIGMOIDH_EVAL_RAW") != "1" {
				var err error
				sysp, err = generateSystemPrompt(sysp, false, workDir)
				fatalIf(err)
			}
		}
		if name == "baseline" {
			name = "(no system prompt)"
		}
		order = append(order, name)
		sc := score{total: len(probes)}

		for i, pr := range probes {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			msgs := []litellm.Message{litellm.UserText(pr.Q)}
			if sysp != "" {
				msgs = []litellm.Message{litellm.System(sysp), litellm.UserText(pr.Q)}
			}
			resp, err := client.Chat(ctx, litellm.Request{
				Model:     model,
				Messages:  msgs,
				Tools:     tools,
				MaxTokens: litellm.IntPtr(400),
			})
			cancel()

			ok := false
			if err == nil && len(resp.ToolCalls()) == 0 {
				text := strings.ToLower(strings.TrimSpace(resp.Text()))
				ok = text != ""
				for _, want := range pr.Expect {
					if !strings.Contains(text, strings.ToLower(want)) {
						ok = false
					}
				}
			}
			if os.Getenv("SIGMOIDH_EVAL_VERBOSE") == "1" {
				r := ""
				if err == nil {
					r = strings.ReplaceAll(strings.SplitN(strings.TrimSpace(resp.Text()), "\n", 2)[0], "\r", "")
					if len(r) > 80 {
						r = r[:80] + "…"
					}
				} else {
					r = "ERR " + err.Error()
				}
				fmt.Printf("      ↳ %s\n", r)
			}
			mark := "F"
			if ok {
				mark = "P"
				sc.pass++
			}
			fmt.Printf("  [%s] %s q%02d %s\n", mark, name, i+1, pr.Q)
		}
		scores[name] = sc
	}

	fmt.Println("\n== RESULTS ==")
	for _, name := range order {
		sc := scores[name]
		fmt.Printf("  %-24s %d/%d\n", name, sc.pass, sc.total)
	}
	if passedAll := func() bool {
		for _, n := range order {
			if scores[n].pass < scores[n].total {
				return false
			}
		}
		return len(order) > 0
	}(); !passedAll {
		os.Exit(1)
	}
}

func fatalIf(err error) {
	if err != nil {
		fatal(err)
	}
}
