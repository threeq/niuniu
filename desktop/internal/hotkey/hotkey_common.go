package hotkey

import (
	"fmt"
	"log/slog"
	"strings"

	"golang.design/x/hotkey"
)

// Combo is one candidate global-hotkey binding plus a human-readable label.
type Combo struct {
	Mods  []hotkey.Modifier
	Key   hotkey.Key
	Label string
}

// keyByName maps the accelerator key tokens the UI can produce (letters, digits,
// Space) to the platform hotkey.Key. Modifiers are resolved separately by the
// platform-specific parseModifier (Ctrl/Shift/Alt on Windows-Linux, Cmd/Option/
// Ctrl/Shift on macOS).
var keyByName = map[string]hotkey.Key{
	"A": hotkey.KeyA, "B": hotkey.KeyB, "C": hotkey.KeyC, "D": hotkey.KeyD,
	"E": hotkey.KeyE, "F": hotkey.KeyF, "G": hotkey.KeyG, "H": hotkey.KeyH,
	"I": hotkey.KeyI, "J": hotkey.KeyJ, "K": hotkey.KeyK, "L": hotkey.KeyL,
	"M": hotkey.KeyM, "N": hotkey.KeyN, "O": hotkey.KeyO, "P": hotkey.KeyP,
	"Q": hotkey.KeyQ, "R": hotkey.KeyR, "S": hotkey.KeyS, "T": hotkey.KeyT,
	"U": hotkey.KeyU, "V": hotkey.KeyV, "W": hotkey.KeyW, "X": hotkey.KeyX,
	"Y": hotkey.KeyY, "Z": hotkey.KeyZ,
	"0": hotkey.Key0, "1": hotkey.Key1, "2": hotkey.Key2, "3": hotkey.Key3,
	"4": hotkey.Key4, "5": hotkey.Key5, "6": hotkey.Key6, "7": hotkey.Key7,
	"8": hotkey.Key8, "9": hotkey.Key9,
	"SPACE": hotkey.KeySpace,
}

func normalizeKeyName(up string) string {
	if up == "SPACE" {
		return "Space"
	}
	return up
}

// ParseAccelerator parses a "+"-delimited accelerator such as "Ctrl+Shift+Z" (it
// also accepts the symbol tokens ⌘ ⌥ ⌃ ⇧) into the platform hotkey modifiers,
// key, and a normalized display label ("Ctrl+Shift+Z" / "Cmd+Shift+Z"). Requires
// at least one modifier and exactly one non-modifier key — a bare key would be a
// dangerous global binding. Modifier token semantics are platform-specific (see
// parseModifier in hotkey.go / hotkey_darwin.go).
func ParseAccelerator(spec string) (mods []hotkey.Modifier, key hotkey.Key, label string, err error) {
	var displays []string
	var keyDisplay string
	keySet := false
	for _, raw := range strings.Split(spec, "+") {
		tok := strings.TrimSpace(raw)
		if tok == "" {
			continue
		}
		up := strings.ToUpper(tok)
		if m, disp, ok := parseModifier(up); ok {
			mods = append(mods, m)
			displays = append(displays, disp)
			continue
		}
		if k, ok := keyByName[up]; ok {
			if keySet {
				return nil, 0, "", fmt.Errorf("accelerator %q has more than one key", spec)
			}
			key = k
			keyDisplay = normalizeKeyName(up)
			keySet = true
			continue
		}
		return nil, 0, "", fmt.Errorf("unrecognized token %q in accelerator %q", tok, spec)
	}
	if len(mods) == 0 {
		return nil, 0, "", fmt.Errorf("accelerator %q needs at least one modifier", spec)
	}
	if !keySet {
		return nil, 0, "", fmt.Errorf("accelerator %q needs a key", spec)
	}
	return mods, key, strings.Join(append(displays, keyDisplay), "+"), nil
}

// RegisterAccelerator parses spec and binds it globally to toggle. Returns the
// normalized label of the bound combo. Unlike RegisterAI it does NOT fall back to
// alternative combos — the caller chose this exact accelerator.
func RegisterAccelerator(spec string, toggle func()) (cleanup func(), label string, err error) {
	mods, key, label, err := ParseAccelerator(spec)
	if err != nil {
		return nil, "", err
	}
	cl, err := register(mods, key, toggle)
	if err != nil {
		return nil, "", err
	}
	return cl, label, nil
}

// registerFirst tries each candidate in order and binds the FIRST the OS accepts.
// A global hotkey registration fails (RegisterHotKey returns an error) when another
// running process already owns that combo — extremely common for Ctrl+Shift+A on
// Windows (微信 / QQ / 搜狗输入法 / 截图工具 all grab it). Falling back to an
// alternative combo means the shortcut still works instead of silently doing
// nothing. Returns the cleanup, the label of the bound combo, and the last error
// if EVERY candidate was rejected.
func registerFirst(candidates []Combo, toggle func()) (cleanup func(), label string, err error) {
	for _, c := range candidates {
		cl, e := register(c.Mods, c.Key, toggle)
		if e == nil {
			return cl, c.Label, nil
		}
		slog.Warn("global hotkey candidate rejected (already in use?), trying next", "combo", c.Label, "error", e)
		err = e
	}
	return nil, "", err
}

// register binds a global hotkey (modifiers+key) to toggle and spawns a
// goroutine that invokes toggle on each keydown. Shared by the platform
// Register/RegisterAI entry points, which only differ in the modifier set.
func register(modifiers []hotkey.Modifier, key hotkey.Key, toggle func()) (cleanup func(), err error) {
	hk := hotkey.New(modifiers, key)
	if err := hk.Register(); err != nil {
		return nil, err
	}
	go func() {
		for range hk.Keydown() {
			slog.Debug("global hotkey triggered", "key", key)
			toggle()
		}
	}()
	slog.Info("global hotkey registered", "key", key)
	return func() { hk.Unregister() }, nil
}
