package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	webUserAgent = "Mozilla/5.0 (compatible; sigmoidh/0.5; +https://github.com/adityabavadekar/sigmoidh)"
	searchURL    = "https://html.duckduckgo.com/html/?q="
	maxResults   = 8

	webErrSearchBlocked = `web search is blocked right now by DuckDuckGo's bot check (HTTP 202). ` +
		`Retrying the search will not help soon. Fetch a specific page with url= instead, ` +
		`or answer from what you already have.`
)

var (
	htmlTagPattern   = regexp.MustCompile(`<[^>]+>`)
	htmlSpacePattern = regexp.MustCompile(`[ \t\f\v]+`)
	htmlBlankLines   = regexp.MustCompile(`\n\s*\n+`)
	// content that is never readable text: scripts, styles, inline svg…
	htmlJunkPattern = regexp.MustCompile(`(?is)<(script|style|noscript|svg|template|iframe)\b.*?</(script|style|noscript|svg|template|iframe)\s*>|<!--.*?-->`)
	htmlTitle       = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	htmlHead        = regexp.MustCompile(`(?is)<head\b.*?</head\s*>`)
	// tags that end a visual line, so extracted text keeps its structure
	htmlBlockTag = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/h[1-6]|/tr|/section|/article|/header|/footer|/pre|/blockquote|/ul|/ol|/table)\b[^>]*>`)

	ddgResultLink    = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgResultSnippet = regexp.MustCompile(`(?is)class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</(?:a|div|td)>`)
)

func runWeb(ctx context.Context, rawURL, query string) (string, bool) {
	query = strings.TrimSpace(query)
	// a URL passed as the query is a fetch, not a search for the URL text
	if rawURL == "" && looksLikeURL(query) {
		rawURL, query = query, ""
	}
	search := rawURL == ""
	if search {
		if query == "" {
			return "url or query is required", true
		}
		rawURL = searchURL + url.QueryEscape(query)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err.Error(), true
	}
	req.Header.Set("User-Agent", webUserAgent)
	req.Header.Set("Accept", "text/html,text/plain;q=0.9,*/*;q=0.5")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error(), true
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err.Error(), true
	}
	body := string(data)
	prefix := fmt.Sprintf("%s %s\n", resp.Status, resp.Request.URL.String())

	if search {
		if resp.StatusCode == http.StatusAccepted || strings.Contains(body, "bots use DuckDuckGo too") {
			return webErrSearchBlocked, true
		}
		if results := formatSearchResults(body); results != "" {
			return truncate(fmt.Sprintf("Search results for %q (fetch one with url=):\n\n%s", query, results)), false
		}
		return truncate(prefix + "no results found"), resp.StatusCode >= 300
	}

	text := body
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "html") || strings.HasPrefix(strings.TrimSpace(body), "<") {
		text = extractWebText(body)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return truncate(prefix + text), true
	}
	return truncate(prefix + text), false
}

func looksLikeURL(s string) bool {
	if strings.ContainsAny(s, " \t\n") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// formatSearchResults turns DuckDuckGo's HTML results into a numbered list of
// title, real URL and snippet. Flattening the whole page dropped every link
// (so the model had nothing to fetch) and kept page chrome such as the
// 60-entry region picker.
func formatSearchResults(page string) string {
	links := ddgResultLink.FindAllStringSubmatch(page, -1)
	snippets := ddgResultSnippet.FindAllStringSubmatch(page, -1)
	var b strings.Builder
	n := 0
	for i, m := range links {
		target := resultTarget(html.UnescapeString(m[1]))
		if target == "" {
			continue // ads and internal links
		}
		n++
		title := inlineText(m[2])
		fmt.Fprintf(&b, "%d. %s\n   %s\n", n, title, target)
		if i < len(snippets) {
			if s := inlineText(snippets[i][1]); s != "" {
				fmt.Fprintf(&b, "   %s\n", s)
			}
		}
		if n == maxResults {
			break
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// resultTarget unwraps DuckDuckGo's redirect (/l/?uddg=<url>) and drops ads
// (y.js) and other links back into DuckDuckGo.
func resultTarget(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(u.Host, "duckduckgo.com") {
		if real := u.Query().Get("uddg"); real != "" && !strings.Contains(real, "duckduckgo.com/y.js") {
			return real
		}
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return href
}

func inlineText(fragment string) string {
	s := htmlTagPattern.ReplaceAllString(fragment, "")
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}

// extractWebText reduces an HTML page to readable text: the title, then the
// body with scripts/styles removed and block elements kept as line breaks.
// Before, script and style contents leaked through as "text", so a fetched
// article was mostly minified JavaScript.
func extractWebText(body string) string {
	title := ""
	if m := htmlTitle.FindStringSubmatch(body); m != nil {
		title = inlineText(m[1])
	}
	body = htmlJunkPattern.ReplaceAllString(body, " ")
	body = htmlHead.ReplaceAllString(body, " ")
	body = htmlBlockTag.ReplaceAllString(body, "\n")
	body = htmlTagPattern.ReplaceAllString(body, " ")
	body = html.UnescapeString(body)

	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimSpace(htmlSpacePattern.ReplaceAllString(ln, " "))
	}
	text := strings.TrimSpace(htmlBlankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
	if title != "" && !strings.HasPrefix(text, title) {
		text = title + "\n\n" + text
	}
	return text
}
