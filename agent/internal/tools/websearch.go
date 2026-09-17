package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// WebSearch queries a configurable search provider. The zero-config option
// is DuckDuckGo's HTML endpoint (no API key); unset means the tool explains
// how to enable it instead of failing silently.
type WebSearch struct {
	Provider string // "" | "duckduckgo"
}

// NewWebSearch builds the tool from NIUNIU_AGENT_SEARCH.
func NewWebSearch(provider string) *WebSearch {
	return &WebSearch{Provider: strings.ToLower(strings.TrimSpace(provider))}
}

func (w *WebSearch) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "WebSearch",
		Description: "Search the web for current information. Results are title/URL/snippet triples from the configured provider. Requires NIUNIU_AGENT_SEARCH (e.g. duckduckgo).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"query":{"type":"string","description":"Search keywords"}},` +
			`"required":["query"]}`),
	}
}

func (w *WebSearch) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(in.Query) == "" {
		return "", fmt.Errorf("query is required")
	}
	switch w.Provider {
	case "", "off", "none":
		return "", fmt.Errorf("web search is not configured: set NIUNIU_AGENT_SEARCH=duckduckgo (no API key needed) to enable it")
	case "duckduckgo":
		return w.searchDDG(in.Query)
	default:
		return "", fmt.Errorf("unknown search provider %q (NIUNIU_AGENT_SEARCH supports: duckduckgo)", w.Provider)
	}
}

func (w *WebSearch) searchDDG(query string) (string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet,
		"https://html.duckduckgo.com/html/?q="+url.QueryEscape(query), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "niuniu-agent/0.1 (websearch)")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("search: HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 0, 512<<10)
	tmp := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
		if len(buf) > 1<<20 {
			break
		}
	}
	results := parseDDGResults(string(buf))
	if len(results) == 0 {
		return "(no results)", nil
	}
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, r.Snippet)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// SearchResult is one parsed hit.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

var (
	ddgLinkRe    = regexp.MustCompile(`(?s)<a[^>]*class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRe = regexp.MustCompile(`(?s)<a[^>]*class="result__snippet"[^>]*>(.*?)</a>`)
	tagStripRe   = regexp.MustCompile(`<[^>]*>`)
	uddgRe       = regexp.MustCompile(`uddg=([^&]+)`)
)

// parseDDGResults extracts title/url/snippet triples from the DuckDuckGo
// HTML endpoint's markup.
func parseDDGResults(htmlDoc string) []SearchResult {
	var titles, snippets []SearchResult
	for _, m := range ddgLinkRe.FindAllStringSubmatch(htmlDoc, -1) {
		u := m[1]
		if dec, err := url.QueryUnescape(u); err == nil {
			u = dec
		}
		if g := uddgRe.FindStringSubmatch(u); g != nil { // DDG redirect wrapper
			if dec, err := url.QueryUnescape(g[1]); err == nil {
				u = dec
			}
		}
		titles = append(titles, SearchResult{
			Title: strings.TrimSpace(tagStripRe.ReplaceAllString(html.UnescapeString(m[2]), "")),
			URL:   u,
		})
	}
	for _, m := range ddgSnippetRe.FindAllStringSubmatch(htmlDoc, -1) {
		snippets = append(snippets, SearchResult{
			Snippet: strings.TrimSpace(tagStripRe.ReplaceAllString(html.UnescapeString(m[1]), "")),
		})
	}
	out := make([]SearchResult, 0, len(titles))
	for i, t := range titles {
		if i < len(snippets) {
			t.Snippet = snippets[i].Snippet
		}
		out = append(out, t)
	}
	return out
}
