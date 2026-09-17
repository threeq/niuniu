package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsForbiddenIP(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":    true,
		"127.9.9.9":    true,
		"10.1.2.3":     true,
		"172.16.0.1":   true,
		"172.31.255.1": true,
		"192.168.1.1":  true,
		"169.254.1.1":  true,
		"0.0.0.0":      true,
		"::1":          true,
		"fe80::1":      true,
		"fc00::1":      true,
		"8.8.8.8":      false,
		"172.32.0.1":   false, // just outside RFC1918
		"2606:4700::1": false,
	}
	for ip, want := range cases {
		if got := IsForbiddenIP(ip); got != want {
			t.Errorf("IsForbiddenIP(%q) = %v, want %v", ip, got, want)
		}
	}
}

func TestWebFetchSSRFBlocked(t *testing.T) {
	// httptest 自身就是 127.0.0.1 —— 生产模式（禁私网）必须拒绝。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>secret</body></html>"))
	}))
	defer ts.Close()

	wf := NewWebFetch(false)
	_, err := wf.Execute(context.Background(), json.RawMessage(`{"url":`+jsonStr(ts.URL)+`}`))
	if err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("err = %v, want private-address rejection", err)
	}
	// 非 http(s) scheme 拒绝。
	if _, err := wf.Execute(context.Background(), json.RawMessage(`{"url":"file:///etc/passwd"}`)); err == nil {
		t.Error("file:// must be rejected")
	}
}

func TestWebFetchFetchesAndConvertsHTML(t *testing.T) {
	page := `<html><head><style>.x{color:red}</style><script>evil()</script></head>
<body><h1>Title</h1><p>Hello &amp; welcome to <a href="/x">niuniu</a>.</p><script>more()</script></body></html>`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer ts.Close()

	wf := NewWebFetch(true) // 测试模式放行私网（httptest 在 127.0.0.1）
	out, err := wf.Execute(context.Background(), json.RawMessage(`{"url":`+jsonStr(ts.URL)+`}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"Title", "Hello", "welcome", "niuniu."} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, banned := range []string{"evil()", "color:red", "<"} {
		if strings.Contains(out, banned) {
			t.Errorf("output leaked %q:\n%s", banned, out)
		}
	}
}

func TestWebFetchURLValidation(t *testing.T) {
	wf := NewWebFetch(true)
	if _, err := wf.Execute(context.Background(), json.RawMessage(`{"url":"notaurl"}`)); err == nil {
		t.Error("want error for missing scheme")
	}
	if _, err := wf.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("want error for missing url")
	}
}

func TestWebSearchUnconfigured(t *testing.T) {
	ws := NewWebSearch("")
	_, err := ws.Execute(context.Background(), json.RawMessage(`{"query":"go release"}`))
	if err == nil || !strings.Contains(err.Error(), "NIUNIU_AGENT_SEARCH") {
		t.Fatalf("err = %v, want configuration guidance", err)
	}
}

func TestWebSearchParseDDG(t *testing.T) {
	html := `<html><body>
	<a rel="nofollow" class="result__a" href="https://go.dev/blog/go1.23">Go 1.23 is released</a>
	<a class="result__snippet">The latest Go release, version 1.23, arrives six months after 1.22.</a>
	<a rel="nofollow" class="result__a" href="https://go.dev/doc/">Documentation - The Go Programming Language</a>
	<a class="result__snippet">Official Go docs.</a>
	</body></html>`
	results := parseDDGResults(html)
	if len(results) != 2 || results[0].Title != "Go 1.23 is released" ||
		results[0].URL != "https://go.dev/blog/go1.23" ||
		!strings.Contains(results[0].Snippet, "six months after") {
		t.Fatalf("results = %+v", results)
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
