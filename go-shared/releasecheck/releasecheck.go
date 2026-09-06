// Package releasecheck resolves the latest desktop release for the in-app
// "check for updates" feature, from TWO sources in priority order:
//
//  1. PRIMARY — the GitHub Releases API for github.com/threeq/niuniu. The
//     release body IS the "What's Changed" notes list, served as Release.Notes
//     so the UI can show what changed in the new version.
//  2. FALLBACK — the official website's changelog page
//     (https://www.niu6ai.com/changelog), parsed from HTML.
//
// Why the fallback exists: api.github.com is unreliable from mainland China —
// unauthenticated requests routinely come back `HTTP 403` (shared-IP rate
// limit / regional blocking). The marketing site is Aliyun-hosted and always
// reachable; its /changelog page is built from the same GitHub releases, so it
// remains a usable degraded source (notes become best-effort there).
//
// The changelog page lists releases newest-first; each release carries its
// tag, date and per-platform download links (the asset filenames keep the
// GitHub naming convention, so the platform-asset matchers work for both
// sources unchanged).
package releasecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is the official website origin. The changelog lives at
// <DefaultBaseURL>/changelog.
const DefaultBaseURL = "https://www.niu6ai.com"

// Asset mirrors the subset of a GitHub release asset the updaters consume.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Release mirrors the subset of a GitHub release the updaters consume, so the
// website-backed source is a drop-in for the old GitHub-JSON shape. Notes is
// the release's "what changed" text (markdown from the GitHub release body;
// best-effort plain text on the changelog fallback) — empty when unavailable.
type Release struct {
	TagName     string  `json:"tag_name"`
	HTMLURL     string  `json:"html_url"`
	PublishedAt string  `json:"published_at"`
	Notes       string  `json:"notes,omitempty"`
	Assets      []Asset `json:"assets"`
}

var (
	// Release tag anchor, e.g. .../releases/tag/v0.7.0 . The page is ordered
	// newest-first, so the first match is the latest release.
	tagLinkRe = regexp.MustCompile(`releases/tag/(v[0-9][0-9A-Za-z.+\-]*)`)
	// yyyy-mm-dd published date.
	dateRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	// Download anchor, e.g.
	// href="https://github.com/.../releases/download/v0.7.0/niuniu-desktop-v0.7.0-windows-amd64.exe"
	dlLinkRe = regexp.MustCompile(`href="(https?://[^"]*?/releases/download/([^/"]+)/([^"/]+))"`)
)

// ParseLatest extracts the most-recent release from the /changelog HTML.
// changelogURL is recorded as the release's HTMLURL so the UI can send users to
// the China-friendly page (which also offers a Baidu Netdisk mirror) rather
// than the slow-from-China GitHub release page. Notes are extracted
// best-effort from the newest release's section prose (HTML stripped); a
// links-only section yields empty notes, never an error.
func ParseLatest(html, changelogURL string) (*Release, error) {
	m := tagLinkRe.FindStringSubmatch(html)
	if m == nil {
		return nil, fmt.Errorf("releasecheck: no release tag found in changelog HTML")
	}
	tag := m[1]
	rel := &Release{TagName: tag, HTMLURL: changelogURL}

	// Published date: the first yyyy-mm-dd appearing after the latest tag
	// anchor (the page renders the date right below the version heading).
	if idx := strings.Index(html, m[0]); idx >= 0 {
		rel.PublishedAt = dateRe.FindString(html[idx:])
	}

	// Notes: the newest release's prose, best-effort.
	rel.Notes = extractSectionNotes(html, m[0])

	// Assets: every download link whose path segment matches the latest tag.
	// Scoping by tag is more robust than slicing the HTML at <li> boundaries.
	for _, dm := range dlLinkRe.FindAllStringSubmatch(html, -1) {
		url, dlTag, name := dm[1], dm[2], dm[3]
		if dlTag != tag {
			continue
		}
		rel.Assets = append(rel.Assets, Asset{Name: name, BrowserDownloadURL: url})
	}
	return rel, nil
}

// FetchLatest resolves the latest release, trying sources in order: the GitHub
// Releases API first (structured JSON + full release notes), then the
// changelog page when GitHub is unreachable (the mainland-China 403 case).
// Pass DefaultBaseURL for the production site. A nil client uses a 15s-timeout
// default.
func FetchLatest(ctx context.Context, client *http.Client, baseURL string) (*Release, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	// Primary: GitHub Releases API (github.com/threeq/niuniu).
	if rel, err := fetchFromGitHub(ctx, client); err == nil {
		return rel, nil
	}

	// Fallback: the Aliyun-hosted changelog page.
	changelogURL := strings.TrimRight(baseURL, "/") + "/changelog"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, changelogURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "niuniu-update-check")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("releasecheck: fetch changelog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("releasecheck: changelog HTTP %d", resp.StatusCode)
	}
	// Cap the read: the changelog is a few hundred KB at most; 4 MiB is a safe
	// ceiling against a misconfigured upstream.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("releasecheck: read changelog: %w", err)
	}
	return ParseLatest(string(body), changelogURL)
}

// DefaultAPIURL is the GitHub Releases API endpoint for the project repo — the
// PRIMARY update source. The release body IS the "What's Changed" notes list,
// so serving it lets the UI show what changed in the new version; the assets
// carry the same platform naming the changelog page links to.
const DefaultAPIURL = "https://api.github.com/repos/threeq/niuniu/releases/latest"

// apiURLOverrideEnv points the GitHub source at an alternative endpoint (a
// mirror or self-hosted proxy). Read per call so tests (t.Setenv) and ops can
// redirect without a rebuild; empty means the default endpoint.
const apiURLOverrideEnv = "NIUNIU_RELEASE_API_URL"

// githubRelease mirrors the subset of the GitHub Releases API response we
// consume. body is the release notes markdown ("## What's Changed" + the PR
// list) — exactly the 优化点 the UI wants to show.
type githubRelease struct {
	TagName     string  `json:"tag_name"`
	HTMLURL     string  `json:"html_url"`
	PublishedAt string  `json:"published_at"`
	Body        string  `json:"body"`
	Assets      []Asset `json:"assets"`
}

// fetchFromGitHub GETs the releases/latest API endpoint and maps it onto the
// shared Release shape. published_at normalizes the RFC3339 timestamp down to
// its yyyy-mm-dd date so both sources present the same display form.
func fetchFromGitHub(ctx context.Context, client *http.Client) (*Release, error) {
	endpoint := os.Getenv(apiURLOverrideEnv)
	if endpoint == "" {
		endpoint = DefaultAPIURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	// GitHub's API rejects requests without a User-Agent.
	req.Header.Set("User-Agent", "niuniu-update-check")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("releasecheck: fetch github release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("releasecheck: github release HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("releasecheck: read github release: %w", err)
	}
	var gr githubRelease
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, fmt.Errorf("releasecheck: decode github release: %w", err)
	}
	if gr.TagName == "" {
		return nil, fmt.Errorf("releasecheck: github release has no tag_name")
	}
	rel := &Release{
		TagName:     gr.TagName,
		HTMLURL:     gr.HTMLURL,
		PublishedAt: dateRe.FindString(gr.PublishedAt),
		Notes:       strings.TrimSpace(gr.Body),
		Assets:      gr.Assets,
	}
	return rel, nil
}

// htmlTagRe matches any HTML tag; htmlAnchorRe matches a whole <a> element.
// Anchors are removed WITH their display text — "macOS (Intel)" and friends
// are link labels, not release notes.
var (
	htmlTagRe    = regexp.MustCompile(`<[^>]*>`)
	htmlAnchorRe = regexp.MustCompile(`(?s)<a\b[^>]*>.*?</a>`)
)

// extractSectionNotes pulls the newest release's prose out of the changelog
// HTML as best-effort notes: take the HTML after the latest tag anchor up to
// the NEXT release anchor (or end), drop the version heading (the h2 holding
// the anchor — its leftover ">" and tag text are not notes), remove anchor
// elements with their labels, strip the remaining tags, and keep non-empty
// non-date lines. Empty on any mismatch — the fallback path's notes are a
// bonus, never a failure.
func extractSectionNotes(html, latestAnchor string) string {
	start := strings.Index(html, latestAnchor)
	if start < 0 {
		return ""
	}
	rest := html[start+len(latestAnchor):]
	if next := tagLinkRe.FindStringIndex(rest); next != nil {
		rest = rest[:next[0]]
		// The next-anchor cut can land mid-href, leaving a trailing unclosed
		// tag fragment (`<a href="https://…`). Drop it so the tag strippers
		// below see well-formed markup.
		if lt := strings.LastIndex(rest, "<"); lt >= 0 && !strings.Contains(rest[lt:], ">") {
			rest = rest[:lt]
		}
	}
	// Skip past the version heading's close so the leftover `">v0.7.0` from
	// slicing inside the href attribute doesn't leak into the notes.
	if h2 := strings.Index(rest, "</h2>"); h2 >= 0 && h2 < 200 {
		rest = rest[h2+len("</h2>"):]
	}
	text := htmlAnchorRe.ReplaceAllString(rest, "")
	text = htmlTagRe.ReplaceAllString(text, "")
	text = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(text)
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || dateRe.MatchString(line) {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
