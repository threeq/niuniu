//go:build !windows

package main

// On non-Windows platforms WebView2 is irrelevant: macOS uses WKWebView,
// Linux uses WebKitGTK. Return "installed" so the pre-flight check never
// blocks startup off-Windows.

func findWebView2Version() (string, string) { return "n/a", "non-windows" }

func showWebView2MissingDialog() bool { return false }
