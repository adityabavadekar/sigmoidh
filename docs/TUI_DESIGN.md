# Sigmoidh TUI — Design & Theme

Mission-control aesthetic for the terminal agent. The screen is an
instrument panel: a live event stream on the left, an instrument cluster
on the right, a command deck at the bottom. Everything flat, geometric,
high-signal — **no glow, no gradients, no rounded fluff**. Chunky heavy
box-drawing chrome (┏━┓) gives it weight; neon ink on deep space gives it
attitude.

Implementation: `tui.go` (app, layout, input), `tui_stream.go` (event
cards + rendering core), `tui_theme.go` (palette + chrome). The same
rendering core drives tests via `NewDirectTuiObserver` (headless, no app
loop) and `NewHeadlessTUI`.

---

## 1. Palette

True-color hex tokens (`pal` in `tui_theme.go`); every dynamic-color tag
in the feed is generated from these tokens, never hardcoded:

| token | hex | role |
|---|---|---|
| void | `#080B14` | app background |
| panel | `#0D121E` | widget backgrounds |
| chrome | `#3E5480` | borders, rules, meter trough |
| focus | `#5FD7FF` | focused border, titles, cyan accents, code spans |
| brand | `#FF5FD7` | ✻ logo, thinking markers, spinner |
| mint | `#5FFF87` | success, results, live dot, diff-add ink |
| amber | `#FFD75F` | warnings, retries, read-only badge, tool glyphs |
| alert | `#FF6B6B` | errors, destructive, diff-del ink |
| violet | `#9D8CFF` | streamed reasoning body |
| text | `#C7D0DC` | primary foreground |
| dim | `#5C6A7D` | secondary / meta / rules / gutter |

Diff bands paint full-row background fills behind the inks (git-style,
capped at 12 lines): add bg `#12301C`, del bg `#33141B`.

## 2. Chrome

Global `tview.Borders` overridden to the **heavy** set (┏ ━ ┓ ┃ ┗ ┛ ┣ ┫
┳ ┻ ╋). The focused variant keeps tview's double-line set (═ ║ ╔ …) so
the focused pane announces itself without color alone. Panel titles are
embedded in the top border and follow the pane accent while borders stay
chrome: `" ✻ STREAM "` (brand), `" ◈ SESSION "` (focus), `" ☑ PLAN "`
(mint), `" ⌗ CONTEXT "` (violet), `" ❯ PROMPT "` (focus).

> **tview gotcha (handled in code):** Box setters return the embedded
> `*Box`, which silently strips the subclass Draw (text layer) when the
> chained result is handed to a Flex. Widgets are mutated in place and
> the original primitive is what gets added.

## 3. Layout

opencode-school minimalism: the borderless transcript owns the screen.
No top bar, no permanent side panels — chrome appears only when useful.

```
┏ ✻ stream ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃ ▎ refactor the auth middleware              ┃   ← quote-block prompt
┃                                             ┃
┃ ✻ thinking…                                 ┃
┃   weighing options                          ┃
┃ ✻ thought for 1.2s                          ┃
┃ ⚙ bash                                      ┃   ← flat inline tool row
┃   │ git remote -v                           ┃   ← hard-wrapped command
┃ └ ok 57 B · 0.0s                            ┃   ← quiet result branch
┃   │ origin  git@example.com:x/y.git         ┃
┃ The middleware uses …                       ┃   ← streamed markdown
┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛
 ✓ · 1. scaffold                                   ← plan strip: only when a plan exists
 ▶ · 2. theme
╔═ ❯ prompt ═══════════════════════════════════╗
║ describe a task…                             ║
╚══════════════════════════════════════════════╝
model · ~/cwd            ⠋ working · 3t 7⚙ 12.4K tok · ▰▱▱▱ 21%
```

1. **Transcript** (flex): append-only feed, dynamic colors, word wrap,
   bottom-follow while pinned, capped at 5000 lines.
2. **Plan strip** (h≤7, contextual): materializes above the prompt only
   when the agent writes a plan; collapses back to zero rows otherwise.
3. **Prompt** (h=3): boxed InputField, placeholder `describe a task…`.
4. **Statusline** (h=1): left = model chip (`[ro]` badge when read-only) ·
   cwd; right = spinner/state · turns/tools/tokens · context meter vs
   `maxTokens` (mint <60 %, amber <85 %, red ≥85 %). Identical frames are
   skipped, so an idle session costs zero redraws.

## 4. Event cards (stream)

| event | glyph | accent | notes |
|---|---|---|---|
| boot | ◆ | focus | version · provider · model · read-only badge |
| user submit | ❯ | focus | echo of the submitted line |
| thinking start/end | ✻ | brand | end card shows `thought for Xs` |
| reasoning delta | raw | violet | streamed inline, choppy newlines collapsed |
| assistant answer | raw | styled | incremental markdown: headings, bold, inline code, fences, bullets, quotes, rules |
| tool call | ▲ | amber | bold name + dim target (+counts for edit/write) |
| command / task | │ gutter | dim | hard-wrapped full command or sub-agent task |
| diff preview | −/+ bands | mint/alert on tinted bg | edit/write/apply_patch, ≤12 rows |
| result ok | ▼ result | mint | bytes · duration once ≥100 ms |
| result error | ✖ error | alert | duration always shown |
| short output | │ gutter | text | inline when ≤400 B & ≤5 lines |
| plan update | ▼ plan | mint | board also re-renders into the PLAN pane |
| retry | ⧗ | amber | provider · attempt n/N · backoff delay |
| fallback | ⚠ | amber | provider pivot + model swap in header |
| notification | 🔔 | mint | fired trigger message |
| done | ● | mint | turns · tools · tokens · took |
| notice | ⚠ | amber | e.g. second submit while a run is active |
| cancelled | ⏹ | amber | run stopped via ctrl+c |
| usage grid | ● usage | focus | rendered on `/usage` |

No turn separators — events flow as one continuous transcript.

## 5. Live elements

- **Spinner**: braille frames (~90 ms) + state word in footer-right while
  a run is active; idle shows `● idle`.
- **Live dot**: header-left `● live` pulses with the activity clock,
  `○ idle` when quiet.
- **Elapsed timer**: header-right, ticks from run start, freezes on end.
- **Context meter**: lives in its own ⌗ CONTEXT pane (not the footer):
  estimate = last real input-token count + streamed output since, falling
  back to chars÷4 before the first usage report.

## 6. Interaction

| key | action |
|---|---|
| `enter` | submit prompt |
| `tab` | slash-command completion (styled menu on ambiguous) |
| `ctrl+c` | stop current run; quit when idle |
| `pgup/pgdn/up/down/home/end` | scroll the stream (reaches bottom → re-pins follow) |
| mouse wheel | scroll the stream |
| typing | always lands in the prompt field (focus bounces back) |

Slash commands route through the same handler as the REPL
(`handleSlashCommand`); their stdout/stderr chatter is captured and
rendered as a quiet gutter block so it never trashes the screen. One run
at a time — a second submit gets an inline notice.

## 7. Implementation notes

- Theme applied once via `applyTheme()`: `tview.Styles` overrides +
  global `tview.Borders` replacement (no per-widget style drift).
- **Concurrency**: observer events arrive on agent goroutines. Feed text
  goes through the thread-safe `TextView.Write`; everything else —
  SetText, ScrollToEnd, rect reads — happens only inside the refresh
  loop's QueueUpdateDraw pass. A cap-1 wake channel coalesces writes;
  one ticker drives spinner/pulse/timer at ~10 fps.
- Delta styling is incremental (`mdStream`): markdown state survives
  across network chunks, O(chunk) per delta, no re-renders of history.
- Sub-agents nest under a four-space gutter with their own observer;
  their prose stays hidden (it becomes the tool result).
- Context estimate: real usage tokens once known, else Σ chars ÷ 4.
