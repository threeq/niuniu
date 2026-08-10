package releasecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
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

	if _, err := FetchLatest(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("expected error on HTTP 403")
	}
}
