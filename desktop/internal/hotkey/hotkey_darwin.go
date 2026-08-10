//go:build darwin

package hotkey

import (
	"golang.design/x/hotkey"
)

// primaryModifiers is the OS-conventional global-hotkey modifier set on macOS
// (Cmd+Shift). The non-darwin build uses Ctrl+Shift.
func primaryModifiers() []hotkey.Modifier {
	return []hotkey.Modifier{hotkey.ModCmd, hotkey.ModShift}
}

// parseModifier maps an upper-cased accelerator modifier token to the macOS
// hotkey modifier plus its canonical display name. The non-darwin build has its
// own variant (Ctrl/Alt). Returns ok=false for non-modifier tokens.
func parseModifier(up string) (hotkey.Modifier, string, bool) {
	switch up {
	case "CMD", "COMMAND", "META", "SUPER", "⌘":
		return hotkey.ModCmd, "Cmd", true
	case "SHIFT", "⇧":
		return hotkey.ModShift, "Shift", true
	case "OPTION", "ALT", "⌥":
		return hotkey.ModOption, "Option", true
	case "CTRL", "CONTROL", "⌃":
		return hotkey.ModCtrl, "Ctrl", true
	}
	return 0, "", false
}

// Register binds Cmd+Shift+N to toggle the local main window.
func Register(toggle func()) (cleanup func(), err error) {
	return register(primaryModifiers(), hotkey.KeyN, toggle)
}

// RegisterAI binds a global hotkey to toggle the AI-aggregation window, preferring
// Cmd+Shift+A and falling back to alternatives when it's already claimed. Returns
// the label of the combo that actually bound.
func RegisterAI(toggle func()) (cleanup func(), label string, err error) {
	return registerFirst([]Combo{
		{Mods: []hotkey.Modifier{hotkey.ModCmd, hotkey.ModShift}, Key: hotkey.KeyA, Label: "⌘⇧A"},
		{Mods: []hotkey.Modifier{hotkey.ModCmd, hotkey.ModOption}, Key: hotkey.KeyA, Label: "⌘⌥A"},
		{Mods: []hotkey.Modifier{hotkey.ModCmd, hotkey.ModCtrl}, Key: hotkey.KeyA, Label: "⌘⌃A"},
	}, toggle)
}
