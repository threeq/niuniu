//go:build !darwin

package hotkey

import (
	"golang.design/x/hotkey"
)

// primaryModifiers is the OS-conventional global-hotkey modifier set for this
// platform (Ctrl+Shift on Windows/Linux, Cmd+Shift on macOS — see the darwin
// build variant).
func primaryModifiers() []hotkey.Modifier {
	return []hotkey.Modifier{hotkey.ModCtrl, hotkey.ModShift}
}

// parseModifier maps an upper-cased accelerator modifier token to the Windows/
// Linux hotkey modifier plus its canonical display name. The darwin build has
// its own variant (Cmd/Option). Returns ok=false for non-modifier tokens.
func parseModifier(up string) (hotkey.Modifier, string, bool) {
	switch up {
	case "CTRL", "CONTROL", "⌃":
		return hotkey.ModCtrl, "Ctrl", true
	case "SHIFT", "⇧":
		return hotkey.ModShift, "Shift", true
	case "ALT", "OPTION", "⌥":
		return modAlt, "Alt", true
	}
	return 0, "", false
}

// Register binds Ctrl+Shift+N to toggle the local main window.
func Register(toggle func()) (cleanup func(), err error) {
	return register(primaryModifiers(), hotkey.KeyN, toggle)
}

// RegisterAI binds a global hotkey to toggle the AI-aggregation window, preferring
// Ctrl+Shift+A and falling back to alternatives when it's already claimed by
// another app. Returns the label of the combo that actually bound.
func RegisterAI(toggle func()) (cleanup func(), label string, err error) {
	return registerFirst([]Combo{
		{Mods: []hotkey.Modifier{hotkey.ModCtrl, hotkey.ModShift}, Key: hotkey.KeyA, Label: "Ctrl+Shift+A"},
		{Mods: []hotkey.Modifier{hotkey.ModCtrl, modAlt}, Key: hotkey.KeyA, Label: "Ctrl+Alt+A"},
		{Mods: []hotkey.Modifier{hotkey.ModCtrl, hotkey.ModShift}, Key: hotkey.KeySpace, Label: "Ctrl+Shift+Space"},
	}, toggle)
}
