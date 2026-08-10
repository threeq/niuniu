// Package releasecheck reads the latest desktop release straight from the
// official website's changelog page (https://www.niu6ai.com/changelog) instead
// of the GitHub Releases API.
//
// Why: api.github.com is unreliable from mainland China — unauthenticated
// requests routinely come back `HTTP 403` (shared-IP rate limit / regional
// blocking), so the in-app "check for updates" feature broke for the exact
// users it's meant to serve. The marketing site is Aliyun-hosted and always
// reachable, and its /changelog page is built from the same GitHub releases,
// so it's the canonical China-friendly source of "what's the latest version".
//
// The page lists releases newest-first; each release carries its tag, date and
// per-platform download links (the asset filenames keep the GitHub naming
// convention, so the existing platform-asset matchers still work unchanged).
package releasecheck

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
// website-backed source is a drop-in for the old GitHub-JSON shape.
type Release struct {
	TagName     string  `json:"tag_name"`
	HTMLURL     string  `json:"html_url"`
	PublishedAt string  `json:"published_at"`
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
// than the slow-from-China GitHub release page.
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

// FetchLatest GETs <baseURL>/changelog and parses the latest release out of it.
// Pass DefaultBaseURL for the production site. A nil client uses a 15s-timeout
// default.
func FetchLatest(ctx context.Context, client *http.Client, baseURL string) (*Release, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
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
