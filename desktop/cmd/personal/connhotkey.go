package main

import (
	"log/slog"
	"runtime"
	"strconv"

	"github.com/niuniu-dev/niuniu-desktop/internal/hotkey"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// connhotkey.go owns the POSITIONAL global open-hotkeys for saved remote
// connections: Ctrl+Shift+<1-9> (Cmd+Shift on macOS) opens — or focuses — the
// Nth saved connection in list order. The mapping is purely positional and is
// resolved fresh on every keypress (connectByPosition snapshots the current
// list), so reordering / adding / removing a connection changes which node a
// digit targets with no re-registration. The matching shortcut label is shown
// on each connection's tray entry (see RebuildTray in app.go).

// connHotkeyMax is the highest position addressable by a global connection
// hotkey. Positions are 1-based and map to the first connHotkeyMax entries of
// the saved-connections list; digits 1-9 are the only keys usable here.
const connHotkeyMax = 9

// connHotkeyModifierPrefix is the platform-conventional modifier prefix for the
// position hotkeys: Cmd+Shift on macOS, Ctrl+Shift elsewhere — matching the
// existing window/AI toggle hotkeys (see config.DefaultWindowAccelerator).
func connHotkeyModifierPrefix() string {
	if runtime.GOOS == "darwin" {
		return "Cmd+Shift+"
	}
	return "Ctrl+Shift+"
}

// connHotkeyLabel returns the human-readable accelerator label for a 1-based
// position, e.g. "Ctrl+Shift+1" ("Cmd+Shift+1" on macOS). Shown next to the
// connection's tray entry so the shortcut is discoverable.
func connHotkeyLabel(pos int) string {
	return connHotkeyModifierPrefix() + strconv.Itoa(pos)
}

// registerConnectionHotkeys binds the global position hotkeys. A digit whose
// combo the OS rejects (already owned by another app) is skipped; the rest still
// bind. Called once from startRemoteServices after the Wails pump is up.
func (a *App) registerConnectionHotkeys() {
	prefix := connHotkeyModifierPrefix()
	cleanups := make([]func(), 0, connHotkeyMax)
	for d := 1; d <= connHotkeyMax; d++ {
		d := d
		spec := prefix + strconv.Itoa(d)
		cleanup, label, err := hotkey.RegisterAccelerator(spec, func() {
			a.connectByPosition(d)
		})
		if err != nil {
			slog.Warn("connection hotkey rejected (already in use?), skipping", "combo", spec, "error", err)
			continue
		}
		cleanups = append(cleanups, cleanup)
		slog.Info("connection hotkey active", "combo", label, "position", d)
	}

	// The "0" slot is special-cased: toggle the manage-connections (picker)
	// window instead of opening a positional connection.
	zeroSpec := prefix + "0"
	if cleanup, label, err := hotkey.RegisterAccelerator(zeroSpec, func() {
		application.InvokeAsync(a.TogglePickerWindow)
	}); err != nil {
		slog.Warn("manage-connections hotkey rejected (already in use?), skipping", "combo", zeroSpec, "error", err)
	} else {
		cleanups = append(cleanups, cleanup)
		slog.Info("manage-connections hotkey active", "combo", label)
	}

	a.mu.Lock()
	a.connHotkeyCleanups = cleanups
	a.mu.Unlock()
}

// connectByPosition opens (or focuses) the saved connection at the given 1-based
// position in list order. Runs on the hotkey goroutine, so it snapshots the
// current connection list (reflecting any reorder/add/remove since registration)
// and dispatches the window open onto the main UI thread via InvokeAsync — Wails
// window creation/mutation must not run off the main thread. A position past the
// end of the list is a silent no-op.
func (a *App) connectByPosition(pos int) {
	conns := a.snapshotConnections()
	idx := pos - 1
	if idx < 0 || idx >= len(conns) {
		slog.Debug("connection hotkey: no connection at position", "position", pos, "count", len(conns))
		return
	}
	conn := conns[idx]
	slog.Info("connection hotkey triggered", "position", pos, "name", conn.Name)
	application.InvokeAsync(func() {
		a.Connect(&conn, false)
	})
}
