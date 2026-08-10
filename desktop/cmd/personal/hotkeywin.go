package main

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/niuniu-dev/niuniu-desktop/internal/config"
	"github.com/niuniu-dev/niuniu-desktop/internal/hotkey"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// hotkeywin.go owns the two CONFIGURABLE global hotkeys — the LOCAL main window
// toggle and the AI-aggregation window toggle — addressed by a `target`
// ("window" | "ai"). It registers each combo from the desktop config (AI falls
// back to hotkey.RegisterAI's candidate list when its combo is taken), and lets
// the SPA "通用设置" page change / enable / disable either one over the
// raw-message bridge (niuniu-hotkey-* — see runnerwin.go HandleRawWebviewMessage).

// hotkeyTarget identifies which global hotkey a settings-bridge message or an
// (un)register call addresses.
type hotkeyTarget string

const (
	hotkeyTargetWindow hotkeyTarget = "window" // the LOCAL main window toggle
	hotkeyTargetAI     hotkeyTarget = "ai"     // the AI-aggregation window toggle
)

// normalizeHotkeyTarget maps a raw bridge-message target to a known target,
// defaulting to "ai" so older SPA builds (which omit the field) keep working.
func normalizeHotkeyTarget(s string) hotkeyTarget {
	if s == string(hotkeyTargetWindow) {
		return hotkeyTargetWindow
	}
	return hotkeyTargetAI
}

// hotkeyToggleFunc returns the window-toggle action for a target.
func (a *App) hotkeyToggleFunc(target hotkeyTarget) func() {
	if target == hotkeyTargetWindow {
		return func() { a.toggleMainWindow() }
	}
	return func() { a.ToggleAIWindow() }
}

// hotkeyConfigFor reads the enabled flag and accelerator for a target under
// cfgMu, falling back to the platform default accelerator when the config is
// missing.
func (a *App) hotkeyConfigFor(target hotkeyTarget) (enabled bool, accel string) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if target == hotkeyTargetWindow {
		if a.cfg != nil {
			return a.cfg.Hotkey.ToggleWindowEnabled, a.cfg.Hotkey.ToggleWindow
		}
		return true, config.DefaultWindowAccelerator()
	}
	if a.cfg != nil {
		return a.cfg.Hotkey.ToggleAIEnabled, a.cfg.Hotkey.ToggleAI
	}
	return true, config.DefaultAIAccelerator()
}

// takeHotkeyCleanup swaps out the current cleanup for a target (returning the old
// one to run OUTSIDE the lock).
func (a *App) takeHotkeyCleanup(target hotkeyTarget) (old func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if target == hotkeyTargetWindow {
		old, a.windowHotkeyCleanup = a.windowHotkeyCleanup, nil
		return old
	}
	old, a.aiHotkeyCleanup = a.aiHotkeyCleanup, nil
	return old
}

// storeHotkeyState records the outcome of a (re)registration for a target.
func (a *App) storeHotkeyState(target hotkeyTarget, cleanup func(), combo string, enabled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if target == hotkeyTargetWindow {
		a.windowHotkeyCleanup = cleanup
		a.windowHotkeyCombo = combo
		a.windowHotkeyEnabled = enabled
		return
	}
	a.aiHotkeyCleanup = cleanup
	a.aiHotkeyCombo = combo
	a.aiHotkeyEnabled = enabled
}

// hotkeyConfigJSON marshals the current hotkey config for both targets, applying
// platform defaults for any empty accelerator. Shared by the JS-injection and
// URL-hash delivery paths.
func (a *App) hotkeyConfigJSON() []byte {
	a.cfgMu.Lock()
	winEnabled, winAccel := true, config.DefaultWindowAccelerator()
	aiEnabled, aiAccel := true, config.DefaultAIAccelerator()
	if a.cfg != nil {
		winEnabled, winAccel = a.cfg.Hotkey.ToggleWindowEnabled, a.cfg.Hotkey.ToggleWindow
		aiEnabled, aiAccel = a.cfg.Hotkey.ToggleAIEnabled, a.cfg.Hotkey.ToggleAI
	}
	a.cfgMu.Unlock()
	if strings.TrimSpace(winAccel) == "" {
		winAccel = config.DefaultWindowAccelerator()
	}
	if strings.TrimSpace(aiAccel) == "" {
		aiAccel = config.DefaultAIAccelerator()
	}
	payload, _ := json.Marshal(map[string]any{
		"window": map[string]any{"enabled": winEnabled, "accelerator": winAccel},
		"ai":     map[string]any{"enabled": aiEnabled, "accelerator": aiAccel},
	})
	return payload
}

// hotkeyBootstrapJS returns a document-created script that publishes the current
// hotkey config as window.__NIUNIU_HOTKEYS__. Secondary path only — kept for the
// picker window (loads "/" directly) and same-session refreshes; the PRIMARY,
// reliable delivery to the URL-loaded main SPA is the URL hash (hotkeyURLHash),
// because ExecJS / document-created JS do not survive the main window's
// splash→SetURL→SPA hand-off on this WebView2 build.
func (a *App) hotkeyBootstrapJS() string {
	return `window.__NIUNIU_HOTKEYS__=` + string(a.hotkeyConfigJSON()) + `;`
}

// hotkeyURLHash returns the "#__nnhk=<base64url json>" fragment appended to the
// local SPA URL when Go navigates the main window. The settings page reads it
// SYNCHRONOUSLY from location.hash at boot (see web src/main.tsx) — the only
// Go→URL-loaded-SPA channel that cannot be dropped by a navigation race. base64url
// (no padding) keeps the fragment free of characters that need URL-escaping.
func (a *App) hotkeyURLHash() string {
	return "#__nnhk=" + base64.RawURLEncoding.EncodeToString(a.hotkeyConfigJSON())
}

// withHotkeyHash appends hotkeyURLHash to a local SPA URL (stripping any existing
// fragment first) so a navigation carries the current hotkey config to the page.
func (a *App) withHotkeyHash(url string) string {
	if i := strings.IndexByte(url, '#'); i >= 0 {
		url = url[:i]
	}
	return url + a.hotkeyURLHash()
}

// injectHotkeyBootstrap re-publishes window.__NIUNIU_HOTKEYS__ on every completed
// navigation of a settings-hosting window (main / picker). The document-created
// options.JS is NOT sufficient on Windows: the main window is created on a splash
// data-URL and later SetURL'd to the local SPA, and the injected global does not
// survive that hand-off (globals are per-document and options.JS is not re-run for
// the SetURL navigation). This nav-completed ExecJS mirrors connwin's
// injectRunnerBridge — the reliable Go→URL-loaded-SPA channel — and reads FRESH
// config each fire, so a restart shows the persisted combo, not the default.
func (a *App) injectHotkeyBootstrap(window *application.WebviewWindow) {
	inject := func(_ *application.WindowEvent) {
		slog.Debug("hotkey bootstrap: (re)injecting __NIUNIU_HOTKEYS__ on navigation")
		window.ExecJS(a.hotkeyBootstrapJS())
	}
	window.RegisterHook(events.Windows.WebViewNavigationCompleted, inject)
	window.RegisterHook(events.Mac.WebViewDidFinishNavigation, inject)
}

// applyHotkeyFromConfig (re)registers the target's global hotkey to match the
// current config: it unregisters any previous binding first, then — when enabled
// — binds the configured accelerator. The AI target falls back to
// hotkey.RegisterAI's candidate list when the accelerator is empty or the OS
// rejects it (combo already owned by another app); the window target has no
// candidate fallback (Ctrl+Shift+N is rarely contended — on conflict it stays
// unbound and the user can pick another combo). Returns the human label that
// actually bound ("" when disabled/failed). Safe to call repeatedly.
func (a *App) applyHotkeyFromConfig(target hotkeyTarget) (label string, err error) {
	enabled, accel := a.hotkeyConfigFor(target)

	// Drop any existing binding before rebinding (or leaving unbound).
	if old := a.takeHotkeyCleanup(target); old != nil {
		old()
	}

	if !enabled {
		a.storeHotkeyState(target, nil, "", false)
		slog.Info("global hotkey disabled by config", "target", target)
		return "", nil
	}

	toggle := a.hotkeyToggleFunc(target)
	var cleanup func()
	if strings.TrimSpace(accel) != "" {
		cleanup, label, err = hotkey.RegisterAccelerator(accel, toggle)
		if err != nil {
			slog.Warn("configured hotkey rejected, trying fallback", "target", target, "accel", accel, "error", err)
		}
	}
	if cleanup == nil && target == hotkeyTargetAI {
		cleanup, label, err = hotkey.RegisterAI(toggle)
	}
	if cleanup == nil {
		a.storeHotkeyState(target, nil, "", false)
		slog.Warn("global hotkey registration failed", "target", target, "error", err)
		return "", err
	}

	a.storeHotkeyState(target, cleanup, label, true)
	slog.Info("global hotkey active", "target", target, "combo", label)
	return label, nil
}

// setHotkey validates, persists, and re-registers a target's hotkey per a request
// from the settings UI, then broadcasts the resulting state back to every window.
// An enabled request with an unparseable accelerator is rejected without changing
// anything; an OS conflict is not an error here (applyHotkeyFromConfig degrades
// gracefully and the broadcast label reflects what actually bound).
func (a *App) setHotkey(target hotkeyTarget, enabled bool, accelerator string) {
	// Validate only a non-empty accelerator: an enable toggle may arrive with an
	// empty string (e.g. the settings UI hasn't loaded the current combo yet), in
	// which case we keep the already-persisted accelerator rather than rejecting.
	if enabled && strings.TrimSpace(accelerator) != "" {
		if _, _, _, err := hotkey.ParseAccelerator(accelerator); err != nil {
			slog.Warn("rejecting invalid hotkey from settings", "target", target, "accel", accelerator, "error", err)
			a.broadcastHotkeyConfig(target, false, "invalid accelerator")
			return
		}
	}

	a.cfgMu.Lock()
	if a.cfg != nil {
		if target == hotkeyTargetWindow {
			a.cfg.Hotkey.ToggleWindowEnabled = enabled
			if strings.TrimSpace(accelerator) != "" {
				a.cfg.Hotkey.ToggleWindow = accelerator
			}
		} else {
			a.cfg.Hotkey.ToggleAIEnabled = enabled
			if strings.TrimSpace(accelerator) != "" {
				a.cfg.Hotkey.ToggleAI = accelerator
			}
		}
		if err := config.SaveTo(a.cfg, a.cfgPath); err != nil {
			slog.Warn("failed to persist hotkey config", "target", target, "error", err)
		}
	}
	a.cfgMu.Unlock()

	if _, err := a.applyHotkeyFromConfig(target); err != nil {
		a.broadcastHotkeyConfig(target, false, err.Error())
		return
	}
	a.broadcastHotkeyConfig(target, true, "")
}

// broadcastHotkeyConfig pushes a target's current hotkey state to the settings UI
// by dispatching a `niuniu:hotkey-config` CustomEvent (carrying `target`) into
// every managed window (the SPA is a remote origin, so we cannot use the Wails
// runtime call — ExecJS is the origin-independent channel, mirroring the runner
// dir-pick reply).
func (a *App) broadcastHotkeyConfig(target hotkeyTarget, ok bool, errMsg string) {
	a.mu.Lock()
	var enabled bool
	var label string
	if target == hotkeyTargetWindow {
		enabled = a.windowHotkeyEnabled
		label = a.windowHotkeyCombo
	} else {
		enabled = a.aiHotkeyEnabled
		label = a.aiHotkeyCombo
	}
	wins := make([]*application.WebviewWindow, 0, len(a.connWindows)+2)
	if a.mainWindow != nil {
		wins = append(wins, a.mainWindow)
	}
	for _, cw := range a.connWindows {
		if cw != nil && cw.window != nil {
			wins = append(wins, cw.window)
		}
	}
	a.mu.Unlock()

	a.cfgMu.Lock()
	accel := ""
	if a.cfg != nil {
		if target == hotkeyTargetWindow {
			accel = a.cfg.Hotkey.ToggleWindow
		} else {
			accel = a.cfg.Hotkey.ToggleAI
		}
	}
	a.cfgMu.Unlock()

	payload, _ := json.Marshal(map[string]any{
		"target":      string(target),
		"enabled":     enabled,
		"accelerator": accel,
		"label":       label,
		"ok":          ok,
		"error":       errMsg,
	})
	tgt, _ := json.Marshal(string(target))
	entry, _ := json.Marshal(map[string]any{"enabled": enabled, "accelerator": accel})
	// Set the injected global too (not just dispatch the event): the settings page
	// polls window.__NIUNIU_HOTKEYS__, so a query reply lands reliably even if the
	// one-shot CustomEvent races the React listener.
	js := `(function(){try{window.__NIUNIU_HOTKEYS__=window.__NIUNIU_HOTKEYS__||{};` +
		`window.__NIUNIU_HOTKEYS__[` + string(tgt) + `]=` + string(entry) + `;}catch(e){}` +
		`window.dispatchEvent(new CustomEvent('niuniu:hotkey-config',{detail:` + string(payload) + `}));})();`
	for _, w := range wins {
		w.ExecJS(js)
	}
}
