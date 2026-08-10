//go:build windows

package hotkey

import "golang.design/x/hotkey"

// modAlt is the Alt global-hotkey modifier on Windows. The upstream
// golang.design/x/hotkey package names it ModAlt on Windows.
const modAlt = hotkey.ModAlt
