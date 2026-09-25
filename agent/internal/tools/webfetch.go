package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// WebFetch fetches a URL and returns readable text (HTML stripped). SSRF
// hardening: only http(s); by default EVERY resolved address (including
// after redirects) must be public — loopback, RFC1918, link-local,
// unspecified and IPv6 equivalents are refused. Tests use the allowPrivate
// switch to reach httptest's 127.0.0.1.
type WebFetch struct {
	allowPrivate bool
}

// NewWebFetch builds the tool; allowPrivate MUST stay false in production
// wiring.
func NewWebFetch(allowPrivate bool) *WebFetch { return &WebFetch{allowPrivate: allowPrivate} }

// webMaxBytes caps fetched text.
const webMaxBytes = 20 << 10

func (w *WebFetch) Def() model.ToolDef {
	return model.ToolDef{
		Name: "WebFetch",
		Description: "Fetch a public http(s) URL and return its readable text (HTML converted, capped at 20KB). " +
			"Private/loopback addresses are refused. Use for documentation pages, blog posts, and release notes.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"url":{"type":"string","description":"Absolute http(s) URL"}},` +
			`"required":["url"]}`),
	}
}

// IsForbiddenIP reports whether the address must never be fetched.
func IsForbiddenIP(ipStr string) bool {
	ip := net.ParseIP(strings.Trim(ipStr, "[]"))
	if ip == nil {
		return true // unparseable = forbidden (fail closed)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	// RFC1918 covers most of IPv4 private; keep IPv6 UEFI (fc00::/7) explicit.
	if v4 := ip.To4(); v4 == nil {
		if _, uEFI, _ := net.ParseCIDR("fc00::/7"); uEFI.Contains(ip) {
			return true
		}
	}
	return false
}

// checkURL validates the scheme and (unless allowPrivate) every address the
// host resolves to.
func (w *WebFetch) checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("only absolute http(s) URLs are supported, got %q", raw)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL has no host: %q", raw)
	}
	if w.allowPrivate {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if IsForbiddenIP(host) {
			return fmt.Errorf("refusing private/loopback address %q (SSRF protection)", host)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if IsForbiddenIP(ip.String()) {
			return fmt.Errorf("refusing %q: host resolves to private/loopback address %s (SSRF protection)", host, ip)
		}
	}
	return nil
}

func (w *WebFetch) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(in.URL) == "" {
		return "", fmt.Errorf("url is required")
	}
	if err := w.checkURL(in.URL); err != nil {
		return "", err
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return w.checkURL(req.URL.String())
		},
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("User-Agent", "niuniu-agent/0.1 (webfetch)")
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, in.URL)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	text := htmlToText(string(body))
	if len(text) > webMaxBytes {
		text = text[:webMaxBytes] + "\n…[truncated at 20KB]"
	}
	if strings.TrimSpace(text) == "" {
		return "(no readable text at this URL)", nil
	}
	return text, nil
}

var (
	scriptRe = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	// Block-level tags break lines; inline tags (a/span/b/…) are removed so
	// inline text stays on one line.
	blockRe   = regexp.MustCompile(`(?i)</?(p|div|h[1-6]|li|tr|br|table|ul|ol|section|article|pre|blockquote)[^>]*>`)
	tagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe   = regexp.MustCompile(`\n{3,}|[ \t]{2,}`)
	htmlUnesc = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ")
)

// htmlToText strips scripts/styles/tags, unescapes the common entities and
// squeezes whitespace — a deliberately small, dependency-free converter.
func htmlToText(html string) string {
	s := scriptRe.ReplaceAllString(html, " ")
	s = blockRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, "")
	s = htmlUnesc.Replace(s)
	s = spaceRe.ReplaceAllString(s, "\n")
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
