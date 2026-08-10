//go:build linux

package hotkey

import "golang.design/x/hotkey"

// modAlt is the Alt global-hotkey modifier on Linux/X11. The upstream
// golang.design/x/hotkey package does not define ModAlt on Linux; the X11
// Alt modifier maps to Mod1.
const modAlt = hotkey.Mod1
