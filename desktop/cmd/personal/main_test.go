package main

import (
	"net/url"
	"strings"
	"testing"
)

// TestLoadingSplashURL_ParsesCleanly guards the Windows launch crash: Wails feeds
// every window URL through url.Parse (assetserver.GetStartURL → baseURL.Parse).
// A data: URL whose body contains a raw '%' (CSS 50%) or '#' (hex color) makes
// url.Parse return "invalid URL escape", which Wails treats as a FATAL and exits
// the process before any window appears. The splash must therefore be a
// cleanly-parseable URL.
func TestLoadingSplashURL_ParsesCleanly(t *testing.T) {
	got := loadingSplashURL("zh")

	if _, err := url.Parse(got); err != nil {
		t.Fatalf("loading splash URL must parse cleanly or Wails FATALs at launch: %v", err)
	}
	// (*url.URL).Parse is the exact call Wails uses; exercise it too.
	base := &url.URL{Scheme: "http", Host: "wails.localhost"}
	if _, err := base.Parse(got); err != nil {
		t.Fatalf("baseURL.Parse(splash) must succeed (matches Wails GetStartURL): %v", err)
	}

	if !strings.HasPrefix(got, "data:text/html;charset=utf-8,") {
		t.Errorf("splash must stay a utf-8 text/html data URL, got prefix %.40q", got)
	}
	// The encoded body must round-trip back to real HTML so the splash renders.
	body := strings.TrimPrefix(got, "data:text/html;charset=utf-8,")
	decoded, err := url.PathUnescape(body)
	if err != nil {
		t.Fatalf("splash body must be valid percent-encoding: %v", err)
	}
	if !strings.Contains(decoded, "启动 牛牛桌面版") || !strings.Contains(decoded, "border-radius:50%") {
		t.Error("decoded splash lost its content; encoding must be reversible")
	}

	// The splash is localized: the en variant must carry the English strings, not
	// the Chinese defaults, so a non-Chinese OS locale sees an English boot page.
	en := loadingSplashURL("en")
	enDecoded, err := url.PathUnescape(strings.TrimPrefix(en, "data:text/html;charset=utf-8,"))
	if err != nil {
		t.Fatalf("en splash body must be valid percent-encoding: %v", err)
	}
	if !strings.Contains(enDecoded, "Starting Niuniu Desktop") ||
		!strings.Contains(enDecoded, "Initializing local service") {
		t.Error("en splash must use the English i18n strings")
	}
}

// TestConnectingSplashURL_ParsesCleanly applies the same Wails url.Parse guard to
// the remote connecting slideshow (which is heavy on '%', '#' and emoji), and
// checks the node target, name, a localized slide, and the health-poll target
// survive the round-trip.
func TestConnectingSplashURL_ParsesCleanly(t *testing.T) {
	got := connectingSplashURL("zh", "公司机", "https://niuniu.example.com:8443")

	if _, err := url.Parse(got); err != nil {
		t.Fatalf("connecting splash must parse cleanly or Wails FATALs: %v", err)
	}
	base := &url.URL{Scheme: "http", Host: "wails.localhost"}
	if _, err := base.Parse(got); err != nil {
		t.Fatalf("baseURL.Parse(connecting splash) must succeed: %v", err)
	}

	body := strings.TrimPrefix(got, "data:text/html;charset=utf-8,")
	decoded, err := url.PathUnescape(body)
	if err != nil {
		t.Fatalf("connecting splash body must be valid percent-encoding: %v", err)
	}
	for _, want := range []string{
		"https://niuniu.example.com:8443", // navigation target (JS)
		"/api/health",                     // health poll
		"公司机",                             // connection name
		"多会话并行",                           // a zh slide
		"正在连接",                            // status label
		"data:image/png;base64,",          // the real product logo, inlined
	} {
		if !strings.Contains(decoded, want) {
			t.Errorf("connecting splash missing %q", want)
		}
	}

	// The en locale must use the English slides.
	en := connectingSplashURL("en", "Office", "http://h.example.com")
	enDecoded, _ := url.PathUnescape(strings.TrimPrefix(en, "data:text/html;charset=utf-8,"))
	if !strings.Contains(enDecoded, "Parallel agents") {
		t.Error("en connecting splash must use English slides")
	}
}
