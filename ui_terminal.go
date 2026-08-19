package main

import (
	"encoding/json"
	"fmt"
	"github.com/voocel/litellm"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/glamour"
	"github.com/common-nighthawk/go-figure"
	"golang.org/x/term"
)

// ANSI terminal color
const (
	ansiReset   = "\033[0m"
	ansiBold    = "\033[1m"
	ansiDim     = "\033[2m"
	ansiCyan    = "\033[36m"
	ansiGreen   = "\033[32m"
	ansiYellow  = "\033[33m"
	ansiRed     = "\033[31m"
	ansiMagenta = "\033[35m"
)

type TerminalLogger struct {
	w     io.Writer
	debug bool
}

func NewTerminalLogger(w io.Writer, debug bool) *TerminalLogger {
	return &TerminalLogger{w: w, debug: debug}
}

func (l *TerminalLogger) OnBootstrap(version, provider, model string, readOnly bool) {
	printDynamicBanner(provider, model, readOnly)
}

func (l *TerminalLogger) OnReasoning(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	// "  │ " = 4 columns + 2 columns safety margin
	usableWidth := getTerminalWidth() - 6
	fmt.Fprintf(os.Stderr, "  %sthinking%s\n", ansiMagenta, ansiReset)

	for _, rawLine := range strings.Split(text, "\n") {
		rawLine = strings.TrimSpace(rawLine)
		if rawLine == "" {
			fmt.Fprintf(os.Stderr, "  %s│%s\n", ansiDim, ansiReset)
			continue
		}

		wrappedLines := wrapWords(rawLine, usableWidth)
		for _, line := range wrappedLines {
			fmt.Fprintf(os.Stderr, "  %s│%s %s%s%s\n", ansiDim, ansiReset, ansiDim, line, ansiReset)
		}
	}
	fmt.Fprintln(os.Stderr)
}

func (l *TerminalLogger) OnToolCall(name string, args json.RawMessage) {
	fmt.Fprintf(l.w, "  %s▲ call%s   %s%s%s %s%s%s\n",
		ansiYellow, ansiReset,
		ansiBold, name, ansiReset,
		ansiDim, string(args), ansiReset,
	)
}

func (l *TerminalLogger) OnToolResult(name, out string, isErr bool) {
	statusTag := fmt.Sprintf("%s▼ result%s", ansiGreen, ansiReset)
	if isErr {
		statusTag = fmt.Sprintf("%s▼ error%s", ansiRed, ansiReset)
	}

	trimmed := strings.TrimRight(out, "\r\n")
	byteCount := len(out)

	if !l.debug {
		fmt.Fprintf(l.w, "  %s %s(%d bytes)%s\n\n", statusTag, ansiDim, byteCount, ansiReset)
		return
	}

	fmt.Fprintf(l.w, "  %s %s(%d bytes)%s\n", statusTag, ansiDim, byteCount, ansiReset)
	for _, line := range strings.Split(trimmed, "\n") {
		fmt.Fprintf(l.w, "  %s│%s %s\n", ansiDim, ansiReset, line)
	}
	fmt.Fprintln(l.w)
}

func (l *TerminalLogger) OnAssistantText(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(getTerminalWidth()),
	)
	if err != nil {
		fmt.Fprintln(os.Stdout, text)
		return
	}

	rendered, err := renderer.Render(text)
	if err != nil {
		fmt.Fprintln(os.Stdout, text)
		return
	}

	fmt.Print(rendered)
}

func (l *TerminalLogger) OnRunComplete(stats *RunStats) {
	l.PrintSummaryLine(stats)
	fmt.Fprintf(os.Stderr, "%sGoodbye!%s\n", ansiDim, ansiReset)
}

func (l *TerminalLogger) OnPrintUsageStats(stats *RunStats) {
	l.PrintDetailedUsage(stats)
}

func humanNumber(n int) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func (l *TerminalLogger) PrintSummaryLine(stats *RunStats) {
	if stats == nil {
		return
	}

	cacheInfo := ""
	if stats.TotalCacheReadTokens > 0 || stats.TotalCacheWriteTokens > 0 {
		cacheInfo = fmt.Sprintf(" · cache %s/%s",
			humanNumber(stats.TotalCacheReadTokens),
			humanNumber(stats.TotalCacheWriteTokens),
		)
	}

	fmt.Fprintf(os.Stderr, "\n%s%s● Done%s  %s%s turns · %s tools · %s total tokens (%s in · %s out · %s r%s)%s\n\n",
		ansiBold, ansiGreen, ansiReset,
		ansiDim,
		humanNumber(stats.TotalTurns),
		humanNumber(stats.TotalToolCalls),
		humanNumber(stats.TotalTokens),
		humanNumber(stats.TotalInputTokens),
		humanNumber(stats.TotalOutputTokens),
		humanNumber(stats.TotalReasoningTokens),
		cacheInfo,
		ansiReset,
	)
}

// prints a grid for when the user types /usage.
func (l *TerminalLogger) PrintDetailedUsage(stats *RunStats) {
	if stats == nil {
		fmt.Fprintf(os.Stderr, "%sNo active session statistics available.%s\n", ansiDim, ansiReset)
		return
	}

	fmt.Fprintf(os.Stderr, "\n%s%s● Session Usage Metrics%s\n\n", ansiBold, ansiCyan, ansiReset)

	fmt.Fprintf(os.Stderr, "  %sTurns%s         %-8s    %sInput Tokens%s     %s\n",
		ansiDim, ansiReset, humanNumber(stats.TotalTurns),
		ansiDim, ansiReset, humanNumber(stats.TotalInputTokens),
	)
	fmt.Fprintf(os.Stderr, "  %sTool Calls%s    %-8s    %sOutput Tokens%s    %s\n",
		ansiDim, ansiReset, humanNumber(stats.TotalToolCalls),
		ansiDim, ansiReset, humanNumber(stats.TotalOutputTokens),
	)
	fmt.Fprintf(os.Stderr, "  %sCache Read%s    %-8s    %sReasoning%s        %s\n",
		ansiDim, ansiReset, humanNumber(stats.TotalCacheReadTokens),
		ansiDim, ansiReset, humanNumber(stats.TotalReasoningTokens),
	)
	fmt.Fprintf(os.Stderr, "  %sCache Write%s   %-8s    %sTotal Tokens%s     %s%s%s\n\n",
		ansiDim, ansiReset, humanNumber(stats.TotalCacheWriteTokens),
		ansiDim, ansiReset, ansiBold, humanNumber(stats.TotalTokens), ansiReset,
	)
}

func printDynamicBanner(provider, model string, readOnly bool) {
	myFigure := figure.NewColorFigure("SigmoidH", "slant", "cyan", true)
	myFigure.Print()

	modeBadge := ""
	if readOnly {
		modeBadge = fmt.Sprintf("  %s%s[read-only]%s", ansiYellow, ansiBold, ansiReset)
	}

	fmt.Fprintf(os.Stderr, "\n  %s%sSigmoidh %s%s  %s%s · %s%s%s\n",
		ansiBold, ansiReset, VERSION, ansiReset,
		ansiDim, provider, model, ansiReset,
		modeBadge,
	)
	fmt.Fprintf(os.Stderr, "%s  type /help for commands · Ctrl+D to exit%s\n\n", ansiDim, ansiReset)
}

func (l *TerminalLogger) OnTurnStart(turn int, model string, usage litellm.Usage) {
	fmt.Fprintf(l.w, "\n%s%s● Turn %02d%s  %s%s%s  %s[%s tok · %s in · %s out · %s r]%s\n\n",
		ansiBold, ansiCyan, turn, ansiReset,
		ansiDim, model, ansiReset,
		ansiDim, humanNumber(usage.TotalTokens), humanNumber(usage.InputTokens),
		humanNumber(usage.OutputTokens), humanNumber(usage.ReasoningTokens), ansiReset,
	)
}

// getTerminalWidth returns active terminal columns (fallback to 80 if piped/unknown).
func getTerminalWidth() int {
	width, _, err := term.GetSize(int(os.Stderr.Fd()))
	if err != nil || width <= 0 {
		return 80
	}
	return width
}

// wrapWords splits a paragraph by whitespace so each line fits inside maxWidth.
func wrapWords(text string, maxWidth int) []string {
	if maxWidth < 20 {
		maxWidth = 20
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	var lines []string
	var current strings.Builder
	currentLen := 0

	for _, w := range words {
		wLen := utf8.RuneCountInString(w)
		if currentLen == 0 {
			current.WriteString(w)
			currentLen = wLen
		} else if currentLen+1+wLen <= maxWidth {
			current.WriteString(" ")
			current.WriteString(w)
			currentLen += 1 + wLen
		} else {
			lines = append(lines, current.String())
			current.Reset()
			current.WriteString(w)
			currentLen = wLen
		}
	}

	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}
