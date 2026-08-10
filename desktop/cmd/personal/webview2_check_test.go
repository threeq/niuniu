package main

import (
	"runtime"
	"testing"
)

// TestFindWebView2Version_DoesNotPanic exercises whichever build-tagged
// implementation is compiled in. We can't assert presence/absence because
// CI may or may not have WebView2 installed, but the function must always
// return cleanly so the pre-flight gate in main() can rely on it.
func TestFindWebView2Version_DoesNotPanic(t *testing.T) {
	ver, src := findWebView2Version()
	t.Logf("WebView2 detected: version=%q source=%q (goos=%s)", ver, src, runtime.GOOS)

	if runtime.GOOS != "windows" {
		// Non-Windows stub returns the documented sentinel pair so callers
		// know the check is intentionally a no-op rather than a missing
		// runtime.
		if ver != "n/a" || src != "non-windows" {
			t.Errorf("non-windows stub: got (%q,%q); want (\"n/a\",\"non-windows\")", ver, src)
		}
	}
}
