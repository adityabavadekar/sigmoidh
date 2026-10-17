package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	snippetContext  = 3  // unchanged lines shown around each change
	snippetMaxLines = 60 // cap on lines a result snippet may add
)

// changedSnippet renders the regions of after that differ from before, with
// line numbers and a little context, so a mutating tool can show the model
// the result directly. Without it models re-read the file to "verify" every
// edit — one wasted turn per change.
//
//	main.go after edit:
//	   4 | import "fmt"
//	>  5 | // fib computes the nth Fibonacci number.
//	   6 | func fib(n int) int {
func changedSnippet(path, before, after string) string {
	a, b := snippetLines(before), snippetLines(after)
	changed, anchors := changedLines(a, b)
	if len(changed) == 0 && len(anchors) == 0 {
		return ""
	}
	show := map[int]bool{}
	mark := map[int]bool{}
	for _, i := range append(append([]int{}, changed...), anchors...) {
		for k := max(0, i-snippetContext); k <= min(len(b)-1, i+snippetContext); k++ {
			show[k] = true
		}
	}
	for _, i := range changed {
		mark[i] = true
	}

	width := len(fmt.Sprint(len(b)))
	var out strings.Builder
	fmt.Fprintf(&out, "%s after change:\n", filepath.Base(path))
	lines, prev := 0, -2
	for i := range b {
		if !show[i] {
			continue
		}
		if lines >= snippetMaxLines {
			rest := 0
			for j := i; j < len(b); j++ {
				if mark[j] {
					rest++
				}
			}
			fmt.Fprintf(&out, "… (%d more changed lines not shown)\n", rest)
			break
		}
		if prev >= 0 && i != prev+1 {
			out.WriteString("…\n")
		}
		prefix := "  "
		if mark[i] {
			prefix = "> "
		}
		fmt.Fprintf(&out, "%s%*d | %s\n", prefix, width, i+1, b[i])
		prev = i
		lines++
	}
	if len(changed) == 0 {
		out.WriteString("(lines were removed here; nothing added)\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

func snippetLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// changedLines returns indexes (into b) of added/modified lines, plus anchor
// indexes where lines of a were deleted without a replacement.
func changedLines(a, b []string) (changed, anchors []int) {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	midA, midB := a[p:len(a)-s], b[p:len(b)-s]
	if len(midB) == 0 {
		if len(midA) > 0 && len(b) > 0 {
			anchors = append(anchors, min(p, len(b)-1))
		}
		return nil, anchors
	}
	if len(midA)*len(midB) > 1_000_000 {
		for i := range midB {
			changed = append(changed, p+i)
		}
		return changed, nil
	}
	// LCS over the differing middle: lines of midB outside it are changes
	lcs := make([][]int32, len(midA)+1)
	for i := range lcs {
		lcs[i] = make([]int32, len(midB)+1)
	}
	for i := len(midA) - 1; i >= 0; i-- {
		for j := len(midB) - 1; j >= 0; j-- {
			if midA[i] == midB[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(midA) || j < len(midB) {
		switch {
		case i < len(midA) && j < len(midB) && midA[i] == midB[j]:
			i, j = i+1, j+1
		case j < len(midB) && (i == len(midA) || lcs[i][j+1] >= lcs[i+1][j]):
			changed = append(changed, p+j)
			j++
		default:
			anchors = append(anchors, min(p+j, len(b)-1))
			i++
		}
	}
	return changed, anchors
}
