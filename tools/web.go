package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var htmlTagPattern = regexp.MustCompile(`<[^>]+>`)
var htmlSpacePattern = regexp.MustCompile(`\s+`)

func runWeb(ctx context.Context, rawURL, query string) (string, bool) {
	if rawURL == "" {
		if query == "" {
			return "url or query is required", true
		}
		rawURL = "https://duckduckgo.com/html/?q=" + url.QueryEscape(query)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err.Error(), true
	}
	req.Header.Set("User-Agent", "sigmoidh/0.1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error(), true
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err.Error(), true
	}
	text := extractWebText(string(data))
	prefix := fmt.Sprintf("%s %s\n", resp.Status, resp.Request.URL.String())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return truncate(prefix + text), true
	}
	return truncate(prefix + text), false
}

func extractWebText(body string) string {
	body = strings.ReplaceAll(body, "<br>", "\n")
	body = strings.ReplaceAll(body, "<br/>", "\n")
	body = strings.ReplaceAll(body, "<br />", "\n")
	body = htmlTagPattern.ReplaceAllString(body, " ")
	replacer := strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", "\"",
		"&#39;", "'",
	)
	body = replacer.Replace(body)
	return strings.TrimSpace(htmlSpacePattern.ReplaceAllString(body, " "))
}
