package releasecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A trimmed-but-faithful slice of www.niu6ai.com/changelog markup: two releases,
// newest-first, each with a tag anchor, a date and four platform download links.
const sampleChangelog = `
<ol>
  <li>
    <h2><a href="https://github.com/threeq/niuniu-public/releases/tag/v0.7.0">v0.7.0</a></h2>
    <div class="mt-1 text-sm">2026-06-28</div>
    <div class="mt-5 flex">
      <a href="https://github.com/threeq/niuniu-public/releases/download/v0.7.0/niuniu-desktop-v0.7.0-darwin-amd64.dmg"><span>macOS (Intel)</span></a>
      <a href="https://github.com/threeq/niuniu-public/releases/download/v0.7.0/niuniu-desktop-v0.7.0-darwin-arm64.dmg"><span>macOS</span></a>
      <a href="https://github.com/threeq/niuniu-public/releases/download/v0.7.0/niuniu-desktop-v0.7.0-linux-amd64"><span>Linux</span></a>
      <a href="https://github.com/threeq/niuniu-public/releases/download/v0.7.0/niuniu-desktop-v0.7.0-windows-amd64.exe"><span>Windows</span></a>
    </div>
  </li>
  <li>
    <h2><a href="https://github.com/threeq/niuniu-public/releases/tag/v0.6.1">v0.6.1</a></h2>
    <div class="mt-1 text-sm">2026-06-24</div>
    <div class="mt-5 flex">
      <a href="https://github.com/threeq/niuniu-public/releases/download/v0.6.1/niuniu-desktop-v0.6.1-windows-amd64.exe"><span>Windows</span></a>
    </div>
  </li>
</ol>`

func TestParseLatest(t *testing.T) {
	rel, err := ParseLatest(sampleChangelog, "https://www.niu6ai.com/changelog")
	if err != nil {
		t.Fatalf("ParseLatest: %v", err)
	}
	if rel.TagName != "v0.7.0" {
		t.Errorf("tag = %q, want v0.7.0", rel.TagName)
	}
	if rel.PublishedAt != "2026-06-28" {
		t.Errorf("published = %q, want 2026-06-28", rel.PublishedAt)
	}
	if rel.HTMLURL != "https://www.niu6ai.com/changelog" {
		t.Errorf("html_url = %q", rel.HTMLURL)
	}
	// Exactly the four assets of the LATEST release — v0.6.1's asset must not leak in.
	if len(rel.Assets) != 4 {
		t.Fatalf("len(assets) = %d, want 4: %+v", len(rel.Assets), rel.Assets)
	}
	var win string
	for _, a := range rel.Assets {
		if a.Name == "niuniu-desktop-v0.7.0-windows-amd64.exe" {
			win = a.BrowserDownloadURL
		}
		if a.Name == "niuniu-desktop-v0.6.1-windows-amd64.exe" {
			t.Errorf("older release asset leaked in: %q", a.Name)
		}
	}
	if win != "https://github.com/threeq/niuniu-public/releases/download/v0.7.0/niuniu-desktop-v0.7.0-windows-amd64.exe" {
		t.Errorf("windows asset url = %q", win)
	}
}

func TestParseLatest_NoRelease(t *testing.T) {
	if _, err := ParseLatest("<html><body>no releases yet</body></html>", "x"); err == nil {
		t.Fatal("expected error when no release tag present")
	}
}

func TestFetchLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/changelog" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(sampleChangelog))
	}))
	defer srv.Close()

	// This test exercises the CHANGELOG fallback path, so pin the GitHub API
	// to a dead address — otherwise FetchLatest hits the real api.github.com
	// and the local server is never consulted.
	t.Setenv("NIUNIU_RELEASE_API_URL", "http://127.0.0.1:0/repos/threeq/niuniu/releases/latest")

	rel, err := FetchLatest(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("FetchLatest: %v", err)
	}
	if rel.TagName != "v0.7.0" {
		t.Errorf("tag = %q, want v0.7.0", rel.TagName)
	}
	if rel.HTMLURL != srv.URL+"/changelog" {
		t.Errorf("html_url = %q, want %q", rel.HTMLURL, srv.URL+"/changelog")
	}
}

func TestFetchLatest_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	// Pin the GitHub API dead as well: both sources must fail for an error.
	t.Setenv("NIUNIU_RELEASE_API_URL", "http://127.0.0.1:0/repos/threeq/niuniu/releases/latest")

	if _, err := FetchLatest(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("expected error on HTTP 403")
	}
}

// --- GitHub-first source (threeq/niuniu) + release notes ----------------------

// A trimmed-but-faithful GitHub Releases API response: body carries the
// "What's Changed" notes, assets keep the platform naming the front-end
// matches on, published_at is a full RFC3339 timestamp.
const sampleGitHubRelease = `{
  "tag_name": "v0.8.6",
  "name": "v0.8.6",
  "html_url": "https://github.com/threeq/niuniu/releases/tag/v0.8.6",
  "published_at": "2026-08-30T12:00:00Z",
  "body": "## What's Changed\n* feat(license): 多租户组织功能分级 by @threeq in https://github.com/threeq/niuniu/pull/1\n* fix(web): 修复高亮跨 hunk 串色 in https://github.com/threeq/niuniu/pull/2\n**Full Changelog**: https://github.com/threeq/niuniu/compare/v0.8.5...v0.8.6",
  "assets": [
    {"name": "niuniu-desktop-v0.8.6-windows-amd64.exe", "browser_download_url": "https://github.com/threeq/niuniu/releases/download/v0.8.6/niuniu-desktop-v0.8.6-windows-amd64.exe"},
    {"name": "niuniu-desktop-v0.8.6-darwin-arm64.dmg", "browser_download_url": "https://github.com/threeq/niuniu/releases/download/v0.8.6/niuniu-desktop-v0.8.6-darwin-arm64.dmg"}
  ]
}`

// TestFetchLatest_GitHubFirst pins the new primary source: FetchLatest asks the
// GitHub Releases API FIRST, and a successful response flows through with the
// release body carried as Notes (the 优化点 the UI now renders) and the
// published date normalized to yyyy-mm-dd (the shape the old changelog parser
// produced and the UI displays).
func TestFetchLatest_GitHubFirst(t *testing.T) {
	var sawAPI bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/threeq/niuniu/releases/latest":
			sawAPI = true
			if r.Header.Get("User-Agent") == "" {
				t.Error("GitHub API requires a User-Agent; request had none")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(sampleGitHubRelease))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("NIUNIU_RELEASE_API_URL", srv.URL+"/repos/threeq/niuniu/releases/latest")

	rel, err := FetchLatest(context.Background(), srv.Client(), "https://www.niu6ai.com")
	if err != nil {
		t.Fatalf("FetchLatest: %v", err)
	}
	if !sawAPI {
		t.Fatal("GitHub API was not consulted first")
	}
	if rel.TagName != "v0.8.6" {
		t.Errorf("tag = %q, want v0.8.6", rel.TagName)
	}
	if rel.PublishedAt != "2026-08-30" {
		t.Errorf("published_at = %q, want normalized 2026-08-30", rel.PublishedAt)
	}
	if rel.HTMLURL != "https://github.com/threeq/niuniu/releases/tag/v0.8.6" {
		t.Errorf("html_url = %q", rel.HTMLURL)
	}
	if !strings.Contains(rel.Notes, "What's Changed") || !strings.Contains(rel.Notes, "多租户组织功能分级") {
		t.Errorf("notes must carry the release body, got %q", rel.Notes)
	}
	if len(rel.Assets) != 2 {
		t.Fatalf("len(assets) = %d, want 2", len(rel.Assets))
	}
}

// TestFetchLatest_FallsBackToChangelogWhenGitHubDown: a GitHub failure (403
// rate limit, the exact mainland-China failure mode) must degrade to the
// niu6ai changelog path and still return a usable release.
func TestFetchLatest_FallsBackToChangelogWhenGitHubDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/threeq/niuniu/releases/latest":
			http.Error(w, "rate limited", http.StatusForbidden)
		case "/changelog":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(sampleChangelog))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("NIUNIU_RELEASE_API_URL", srv.URL+"/repos/threeq/niuniu/releases/latest")

	// baseURL points at the SAME test server so both the (failing) API and the
	// (working) changelog are local.
	rel, err := FetchLatest(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("FetchLatest must fall back to changelog after GitHub 403: %v", err)
	}
	if rel.TagName != "v0.7.0" {
		t.Errorf("fallback tag = %q, want v0.7.0", rel.TagName)
	}
}

// TestParseLatest_NotesExtraction pins the best-effort notes extraction on the
// changelog fallback: the newest release's section text becomes the notes, HTML
// stripped; when the section has no readable text the notes stay empty (never
// an error).
func TestParseLatest_NotesExtraction(t *testing.T) {
	rel, err := ParseLatest(sampleChangelog, "https://www.niu6ai.com/changelog")
	if err != nil {
		t.Fatalf("ParseLatest: %v", err)
	}
	// The fixture's latest section has no prose — only links and a date — so
	// notes stay empty; the important part is ParseLatest never fails on it.
	if rel.Notes != "" {
		t.Errorf("notes = %q, want empty for a links-only section", rel.Notes)
	}

	const withProse = `
<li><h2><a href="https://github.com/threeq/niuniu/releases/tag/v0.9.0">v0.9.0</a></h2>
<div class="mt-1 text-sm">2026-09-01</div>
<p>知识库管理重构为一级资源。</p>
<div><a href="https://github.com/threeq/niuniu/releases/download/v0.9.0/niuniu-desktop-v0.9.0-windows-amd64.exe">Windows</a></div>
</li>`
	rel2, err := ParseLatest(withProse, "https://www.niu6ai.com/changelog")
	if err != nil {
		t.Fatalf("ParseLatest(prose): %v", err)
	}
	if rel2.TagName != "v0.9.0" {
		t.Fatalf("tag = %q, want v0.9.0", rel2.TagName)
	}
	if !strings.Contains(rel2.Notes, "知识库管理重构为一级资源") {
		t.Errorf("notes = %q, want the section prose", rel2.Notes)
	}
	if strings.Contains(rel2.Notes, "<") {
		t.Errorf("notes must be HTML-stripped plain text, got %q", rel2.Notes)
	}
}
