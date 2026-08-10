//go:build !windows && !darwin

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// aiembed_other.go is the fallback for platforms without native window embedding
// (currently Linux). Windows uses Win32 owner windows (aiembed_windows.go) and
// macOS uses NSWindow child windows (aiembed_darwin.go); on the rest we don't
// reparent and let ActivateAIService fall back to opening each service in its own
// independent window (see aiwin.go's aiEmbedSupported branch). These stubs keep
// the shared code compiling on every platform.

const aiEmbedSupported = false

func aiEmbedOwn(hub, child *application.WebviewWindow) {}

func aiEmbedPosition(hub, child *application.WebviewWindow, x, y, w, h int) {}

func aiEmbedReveal(hub, child *application.WebviewWindow, x, y, w, h int) {}

func aiEmbedStash(child *application.WebviewWindow, w, h int) {}
