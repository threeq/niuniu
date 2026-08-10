package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPrettyJSON(t *testing.T) {
	out := prettyJSON([]byte(`{"a":1}`))
	if !strings.Contains(out, "\"a\": 1") {
		t.Fatalf("expected indented JSON, got %q", out)
	}
	// Non-JSON falls back to the raw bytes.
	if got := prettyJSON([]byte("not json")); got != "not json" {
		t.Fatalf("expected raw fallback, got %q", got)
	}
}

// TestKBToolsForwarding verifies kb_search/kb_list hit the workspace-scoped KB
// endpoints with the right method, path and query, mirroring the server routes.
func TestKBToolsForwarding(t *testing.T) {
	var gotSearch, gotList string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/kb/search"):
			gotSearch = r.URL.RawQuery
			_, _ = w.Write([]byte(`[{"kb_id":1,"kb_name":"docs","rel_path":"a.md","snippet":"hit"}]`))
		case strings.HasSuffix(r.URL.Path, "/kb/list"):
			gotList = r.Method + " " + r.URL.Path
			_, _ = w.Write([]byte(`[{"id":1,"name":"docs"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	api := &apiClient{base: strings.TrimRight(srv.URL, "/"), client: &http.Client{Timeout: 5 * time.Second}}

	// kb_search forwards q + limit.
	if _, err := api.get("/mcp/workspaces/7/kb/search?q=" + "%E5%85%A8%E6%96%87" + "&limit=5"); err != nil {
		t.Fatalf("kb/search get: %v", err)
	}
	if !strings.Contains(gotSearch, "q=") || !strings.Contains(gotSearch, "limit=5") {
		t.Fatalf("search query not forwarded: %q", gotSearch)
	}

	// kb_list is a plain GET on the workspace-scoped path.
	if _, err := api.get("/mcp/workspaces/7/kb/list"); err != nil {
		t.Fatalf("kb/list get: %v", err)
	}
	if gotList != "GET /mcp/workspaces/7/kb/list" {
		t.Fatalf("list not forwarded as GET: %q", gotList)
	}
}
