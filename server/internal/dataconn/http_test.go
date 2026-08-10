package dataconn

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPClassify(t *testing.T) {
	c := NewHTTPConnector()
	cases := []struct {
		method   string
		path     string
		wantMode AccessMode
		wantErr  bool
		wantObj  string
	}{
		{"GET", "/users", ModeRead, false, "/users"},
		{"get", "users", ModeRead, false, "/users"}, // normalized verb + leading slash
		{"HEAD", "/x", ModeRead, false, "/x"},
		{"POST", "/users", ModeRead, false, "/users"}, // POST is a read (query APIs)
		{"PUT", "/users/1", ModeWrite, false, "/users/1"},
		{"DELETE", "/users/1", ModeWrite, false, "/users/1"},
		{"TRACE", "/x", "", true, ""},                      // bad method
		{"GET", "http://evil/x", "", true, ""},             // absolute URL
		{"GET", "/a/../b", "", true, ""},                   // traversal
		{"GET", "//evil/x", "", true, ""},                  // protocol-relative
		{"GET", "/x?y=1", "", true, ""},                    // embedded query
		{"GET", "", "", true, ""},                          // empty
	}
	for _, tc := range cases {
		mode, ref, err := c.Classify(Operation{HTTPMethod: tc.method, HTTPPath: tc.path})
		if tc.wantErr {
			if err == nil {
				t.Errorf("Classify(%q,%q): expected error, got none", tc.method, tc.path)
			}
			continue
		}
		if err != nil {
			t.Errorf("Classify(%q,%q): unexpected error %v", tc.method, tc.path, err)
			continue
		}
		if mode != tc.wantMode {
			t.Errorf("Classify(%q,%q): mode=%q want %q", tc.method, tc.path, mode, tc.wantMode)
		}
		if len(ref.Objects) != 1 || ref.Objects[0] != tc.wantObj {
			t.Errorf("Classify(%q,%q): objects=%v want [%q]", tc.method, tc.path, ref.Objects, tc.wantObj)
		}
		if !ref.ReferencesTables {
			t.Errorf("Classify(%q,%q): ReferencesTables must be true", tc.method, tc.path)
		}
	}
}

func TestHTTPExecuteArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("active"); got != "true" {
			t.Errorf("query active=%q want true", got)
		}
		if got := r.Header.Get("X-Api-Key"); got != "secret" {
			t.Errorf("header X-Api-Key=%q want secret", got)
		}
		u, p, ok := r.BasicAuth()
		if !ok || u != "alice" || p != "pw" {
			t.Errorf("basic auth = %q/%q ok=%v", u, p, ok)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[{"id":1,"name":"a"},{"id":2,"name":"b"}]`)
	}))
	defer srv.Close()

	conn := connFromTestURL(t, srv.URL)
	conn.User, conn.Password = "alice", "pw"
	conn.Options["headers"] = map[string]any{"X-Api-Key": "secret"}

	c := NewHTTPConnector()
	rs, err := c.Execute(context.Background(), conn, Operation{
		HTTPMethod: "GET",
		HTTPPath:   "/users",
		HTTPQuery:  map[string]any{"active": true},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(rs.Rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rs.Rows))
	}
	if cols := columnNames(rs); strings.Join(cols, ",") != "id,name" {
		t.Errorf("columns=%v want [id name]", cols)
	}
	if rs.Engine != "http" {
		t.Errorf("engine=%q want http", rs.Engine)
	}
}

func TestHTTPExecuteObjectAndLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":7,"name":"solo"}`)
	}))
	defer srv.Close()
	c := NewHTTPConnector()
	rs, err := c.Execute(context.Background(), connFromTestURL(t, srv.URL), Operation{HTTPMethod: "GET", HTTPPath: "/x"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(rs.Rows) != 1 {
		t.Fatalf("object rows=%d want 1", len(rs.Rows))
	}

	arr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"id":1},{"id":2},{"id":3}]`)
	}))
	defer arr.Close()
	rs, err = c.Execute(context.Background(), connFromTestURL(t, arr.URL), Operation{HTTPMethod: "GET", HTTPPath: "/x", RowLimit: 2})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(rs.Rows) != 2 || !rs.Truncated {
		t.Errorf("rows=%d truncated=%v want 2/true", len(rs.Rows), rs.Truncated)
	}
}

func TestHTTPExecuteErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}))
	defer srv.Close()
	c := NewHTTPConnector()
	_, err := c.Execute(context.Background(), connFromTestURL(t, srv.URL), Operation{HTTPMethod: "GET", HTTPPath: "/x"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("expected HTTP 500 error, got %v", err)
	}
}

func TestHTTPExecutePostBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method=%q want POST", r.Method)
		}
		b, _ := io.ReadAll(r.Body)
		var got map[string]any
		json.Unmarshal(b, &got)
		if got["name"] != "neo" {
			t.Errorf("body name=%v want neo", got["name"])
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	c := NewHTTPConnector()
	_, err := c.Execute(context.Background(), connFromTestURL(t, srv.URL), Operation{
		HTTPMethod: "POST", HTTPPath: "/users", HTTPBody: map[string]any{"name": "neo"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestHTTPNormalizeUnpack(t *testing.T) {
	rowsOf := func(rs *ResultSet) int { return len(rs.Rows) }

	// No list_path -> the response is returned AS-IS: a top-level array is rows.
	rs := httpNormalize([]byte(`[{"id":1},{"id":2}]`), 1000, "")
	if rowsOf(rs) != 2 {
		t.Fatalf("top-level array rows=%d want 2", rowsOf(rs))
	}

	// No list_path on a wrapped object -> as-is = a SINGLE row (no auto-unpack).
	rs = httpNormalize([]byte(`{"total":2,"items":[{"id":1},{"id":2}]}`), 1000, "")
	if rowsOf(rs) != 1 {
		t.Fatalf("wrapped object without list_path rows=%d want 1 (as-is)", rowsOf(rs))
	}

	// With list_path it unpacks the array into rows.
	rs = httpNormalize([]byte(`{"total":2,"items":[{"id":1},{"id":2}]}`), 1000, "items")
	if rowsOf(rs) != 2 || strings.Join(columnNames(rs), ",") != "id" {
		t.Fatalf("items list_path rows=%d cols=%v", rowsOf(rs), columnNames(rs))
	}

	// Nested list_path {code,data:{list:[...]}} -> rows.
	rs = httpNormalize([]byte(`{"code":0,"data":{"total":3,"list":[{"id":1},{"id":2},{"id":3}]}}`), 1000, "data.list")
	if rowsOf(rs) != 3 {
		t.Fatalf("nested data.list rows=%d want 3", rowsOf(rs))
	}

	// A bad list_path -> empty result (clear "no match"), not a junk single row.
	rs = httpNormalize([]byte(`{"payload":{"hits":[{"id":1}]}}`), 1000, "payload.nope")
	if rowsOf(rs) != 0 {
		t.Fatalf("bad list_path rows=%d want 0", rowsOf(rs))
	}

	// A plain object stays a single row when no rule is set.
	rs = httpNormalize([]byte(`{"id":7,"name":"solo"}`), 1000, "")
	if rowsOf(rs) != 1 {
		t.Fatalf("plain object rows=%d want 1", rowsOf(rs))
	}

	// list_path that resolves to a single object -> one row.
	rs = httpNormalize([]byte(`{"data":{"user":{"id":1,"name":"a"}}}`), 1000, "data.user")
	if rowsOf(rs) != 1 || strings.Join(columnNames(rs), ",") != "id,name" {
		t.Fatalf("object list_path rows=%d cols=%v", rowsOf(rs), columnNames(rs))
	}
}

// connFromTestURL splits an httptest server URL (http://127.0.0.1:port) into a
// ConnConfig with scheme http and the right host/port.
func connFromTestURL(t *testing.T, raw string) ConnConfig {
	t.Helper()
	rest := strings.TrimPrefix(raw, "http://")
	host, portStr, found := strings.Cut(rest, ":")
	if !found {
		t.Fatalf("bad test url %q", raw)
	}
	port := 0
	for _, r := range portStr {
		port = port*10 + int(r-'0')
	}
	return ConnConfig{
		Kind:    KindHTTP,
		Host:    host,
		Port:    port,
		Options: map[string]any{"scheme": "http"},
	}
}

func columnNames(rs *ResultSet) []string {
	out := make([]string, len(rs.Columns))
	for i, c := range rs.Columns {
		out[i] = c.Name
	}
	return out
}
