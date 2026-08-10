package main

import (
	"html"
	"log/slog"
	"runtime"

	"github.com/niuniu-dev/niuniu-desktop/internal/config"
	"github.com/niuniu-dev/niuniu-desktop/internal/i18n"
	"github.com/niuniu-dev/niuniu-desktop/internal/webviewreset"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// aiwin.go implements the "AI 直达" aggregation window (spec: 桌面端 AI 网页聚合).
// It is fully decoupled from the SPA / embedded server: a SINGLE hub window hosts
// the embedded frontend (/ai.html) — a left rail + topbar + an empty "stage"
// region — and each opened service's webview is embedded INTO that stage so the
// whole thing is one window with an in-window switcher (see aiembed_windows.go).
// On non-Windows we fall back to one independent window per service. The helper
// features here wrap AROUND the webviews — global hotkey, prompt-library
// clipboard, window management — never injecting JS into the pages.
//
// Locking mirrors connwin.go: aiHubWindow / aiPool / aiPoolOrder / aiActiveService /
// aiStage are guarded by a.mu; the persisted AI config (a.cfg.AI) is guarded by
// a.cfgMu. The two locks are never held simultaneously. The Win32 embed calls must
// run on the main UI thread (application.InvokeSync / InvokeAsync).

// aiServiceEntry is one live service webview kept in the LRU pool so returning to a
// recently-used service is instant — no re-navigation, and its page/login/scroll are
// preserved. All fields guarded by App.mu.
type aiServiceEntry struct {
	win      *application.WebviewWindow
	embedded bool // Win32 owner-reparent (aiEmbedOwn) done
	loaded   bool // a page has finished loading at least once (→ reveal + instant revisit)
}

// aiPoolMax caps how many service webviews stay alive at once. Each is roughly a
// browser tab (~100–300 MB); 9 keeps a generous recent set instant to switch between
// (the crash was never window-count — it was the non-string-postMessage os.Exit, now
// guarded — so this is bounded only by memory). The least-recently-used one is closed
// when a 10th is opened.
const aiPoolMax = 9

// lruTouch marks id as most-recently-used (moves it to the back of aiPoolOrder).
// Caller must hold a.mu.
func (a *App) lruTouch(id string) {
	a.lruRemove(id)
	a.aiPoolOrder = append(a.aiPoolOrder, id)
}

// lruRemove drops id from the LRU order list. Caller must hold a.mu.
func (a *App) lruRemove(id string) {
	for i, v := range a.aiPoolOrder {
		if v == id {
			a.aiPoolOrder = append(a.aiPoolOrder[:i], a.aiPoolOrder[i+1:]...)
			return
		}
	}
}

// aiServiceGuardJS is injected into the AI-service webview (via
// AddScriptToExecuteOnDocumentCreated, so it runs on every navigation, before the
// page's own scripts). It coerces window.chrome.webview.postMessage to always send
// a STRING. Without this, an external AI site posting a non-string web message makes
// go-webview2's MessageReceived handler fail TryGetWebMessageAsString → errorCallback
// → os.Exit(1), hard-crashing the whole app. Idempotent (guarded by __niuGuard).
const aiServiceGuardJS = `(function(){
  function patch(){
    try {
      var wv = window.chrome && window.chrome.webview;
      if (wv && wv.postMessage && !wv.__niuGuard){
        var orig = wv.postMessage.bind(wv);
        wv.postMessage = function(m){ try{ orig(typeof m === 'string' ? m : JSON.stringify(m)); }catch(e){} };
        wv.__niuGuard = true;
      }
    } catch(e){}
  }
  patch();
  try { document.addEventListener('DOMContentLoaded', patch); } catch(e){}
})();`

// aiBootstrapHTML is the page a freshly-created service window shows BEFORE (and,
// crucially, DURING) the navigation to the real service URL. WebView2 keeps the
// current document visible until the new page's first paint, so on a slow site this
// page is what fills the ~seconds of network fetch — we make it a dark, on-brand
// loading animation (matching the hub splash) with the service name, so that gap reads
// as "the site is loading" instead of a black screen. The service name is HTML-escaped.
func aiBootstrapHTML(name string) string {
	n := html.EscapeString(name)
	return `<!doctype html><html><head><meta charset="utf-8"><title>` + n + `</title><style>` +
		`html,body{height:100%;margin:0}` +
		`body{background:radial-gradient(900px 460px at 50% -12%,#16223b 0,#0b1220 62%);` +
		`display:flex;flex-direction:column;align-items:center;justify-content:center;gap:16px;` +
		`font-family:'Noto Sans SC',system-ui,-apple-system,sans-serif;color:#e6ecf5;user-select:none}` +
		`.n{font-size:16px;font-weight:600;opacity:.92}` +
		`.b{position:relative;width:min(420px,60vw);height:3px;border-radius:3px;background:#1b2740;overflow:hidden}` +
		`.b i{position:absolute;left:0;top:0;height:100%;width:36%;border-radius:3px;` +
		`background:linear-gradient(90deg,transparent,#3b82f6,#22d3ee,transparent);animation:r 1.2s linear infinite}` +
		`@keyframes r{0%{transform:translateX(-120%)}100%{transform:translateX(340%)}}</style></head>` +
		`<body><div class="n">` + n + `</div><div class="b"><i></i></div></body></html>`
}

// raiseWindow restores + shows + focuses a window. macOS skips Focus to avoid
// the WebKit::ServicesController dispatch_sync deadlock documented in
// app.go/openWebview.
func raiseWindow(w *application.WebviewWindow) {
	if w == nil {
		return
	}
	w.Restore()
	w.Show()
	if runtime.GOOS != "darwin" {
		w.Focus()
	}
}

// OpenAIWindow shows and focuses the AI hub (left-rail switcher). Bound for the
// tray menu, the SPA sidebar (spec entry #3), and the global hotkey. The hub is
// never opened at first launch — only on demand (方案 A: 绝不抢首启).
func (a *App) OpenAIWindow() {
	a.mu.Lock()
	win := a.aiHubWindow
	a.mu.Unlock()
	if win == nil {
		return
	}
	a.markAuxWindowOpened("ai-hub")
	raiseWindow(win)
	a.setHubVisible(true)
}

// ToggleAIWindow shows the hub if hidden and hides it if visible. Wired to the
// global hotkey so one keystroke wakes or dismisses the whole AI surface.
func (a *App) ToggleAIWindow() {
	a.mu.Lock()
	win := a.aiHubWindow
	a.mu.Unlock()
	if win == nil {
		return
	}
	if win.IsVisible() {
		win.Hide()
		a.setHubVisible(false)
		return
	}
	a.markAuxWindowOpened("ai-hub")
	raiseWindow(win)
	a.setHubVisible(true)
}

// setHubVisible records whether the hub is visible and re-applies the docked
// service window's visibility. Called from every path that shows/hides the hub
// (OpenAIWindow / ToggleAIWindow / the hub close hook) — this is the deterministic
// replacement for the unreliable WM_SHOWWINDOW-event + ExecJS plumbing.
func (a *App) setHubVisible(v bool) {
	a.mu.Lock()
	a.aiHubVisible = v
	a.mu.Unlock()
	a.applyAIServiceVisibility()
}

// applyAIServiceVisibility reveals the docked service window on the stage iff the hub
// is visible, a service is active, and no HTML overlay (modal/settings/loading) is up;
// otherwise it STASHES the window off-screen (kept shown, never SW_HIDE — see
// aiEmbedStash for why: a hidden WebView2 blanks on the next show). Single source of
// truth for the service window's on-screen state. Runs on the main UI thread via
// InvokeSync; callers on the main thread (window-event hooks) must use the async
// variant instead to avoid the InvokeSync-from-main-thread deadlock.
func (a *App) applyAIServiceVisibility() { a.applyAIServiceVisibilityMode(false) }

func (a *App) applyAIServiceVisibilityMode(async bool) {
	if !aiEmbedSupported {
		return
	}
	a.mu.Lock()
	hub := a.aiHubWindow
	target := a.revealTargetLocked()
	x, y, w, h := a.aiStage[0], a.aiStage[1], a.aiStage[2], a.aiStage[3]
	// Reveal ONLY the active service's window (target); every other pooled window is
	// stashed off-screen (kept alive & rendering). The frontend keeps its loading
	// splash up (overlay) until the picked service's navigation has started, so a
	// reveal here shows the site's OWN loading UI, never another service's content
	// (each pool window only ever showed the dark bootstrap page + its own site).
	type visItem struct {
		win    *application.WebviewWindow
		reveal bool
	}
	items := make([]visItem, 0, len(a.aiPool))
	for _, e := range a.aiPool {
		if e == nil || e.win == nil {
			continue
		}
		items = append(items, visItem{e.win, e.win == target})
	}
	a.mu.Unlock()
	if len(items) == 0 {
		return
	}
	apply := func() {
		for _, it := range items {
			if it.reveal {
				aiEmbedReveal(hub, it.win, x, y, w, h)
			} else {
				aiEmbedStash(it.win, w, h)
			}
		}
	}
	if async {
		application.InvokeAsync(apply)
	} else {
		application.InvokeSync(apply)
	}
}

// revealTargetLocked returns the pooled webview that SHOULD be on the stage right now
// (hub visible, no HTML overlay up, an active service exists), or nil. The single
// source of truth for both revealing (applyAIServiceVisibility) and re-docking
// (SetAIStageRect / repositionActiveAIService), so a drag/resize never yanks a stashed
// (loading/overlay) window on-stage. Caller must hold a.mu.
func (a *App) revealTargetLocked() *application.WebviewWindow {
	if !a.aiHubVisible || a.aiOverlayOpen || a.shuttingDown || a.aiActiveService == "" {
		return nil
	}
	if e := a.aiPool[a.aiActiveService]; e != nil {
		return e.win
	}
	return nil
}

// SetAIOverlayOpen is called by the hub frontend whenever an HTML overlay (an
// add-service / settings modal, or the loading splash) opens or closes. While one
// is up the docked service must stay hidden so it doesn't paint over the dialog.
func (a *App) SetAIOverlayOpen(open bool) {
	a.mu.Lock()
	a.aiOverlayOpen = open
	a.mu.Unlock()
	a.applyAIServiceVisibility()
}

// onAIServiceNavigated fires on each WebViewNavigationCompleted of a pooled service
// window. We mark the entry loaded (so a revisit is instant) and, if it's the ACTIVE
// service, tell the hub to drop its splash and reveal the webview. The real page is
// navigated from window-creation time (createServiceEntry SetURLs immediately, so its
// network fetch starts ASAP — fast sites stay fast); the frontend also caps the splash
// so a slow site reveals within a couple seconds rather than blocking for its full load.
func (a *App) onAIServiceNavigated(id string) {
	a.mu.Lock()
	e := a.aiPool[id]
	if e == nil {
		a.mu.Unlock()
		return
	}
	e.loaded = true
	hub := a.aiHubWindow
	active := a.aiActiveService
	down := a.shuttingDown
	a.mu.Unlock()
	if hub == nil || down || id != active {
		return
	}
	hub.ExecJS("window.onServiceLoaded && window.onServiceLoaded()")
}

// GetAIHotkey returns the human-readable label of the global hotkey that actually
// bound for the hub (e.g. "Ctrl+Shift+A", or a fallback when that combo was already
// claimed). Empty when no hotkey could be registered. The hub UI shows it so the
// user always knows the live shortcut.
func (a *App) GetAIHotkey() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.aiHotkeyCombo
}

// ShowLocalWindow brings the local niuniu main window to the front. Wired to the
// hub's top "牛牛" brand button so the user can jump back to the local workspace
// from the AI hub (the hub stays open behind it).
func (a *App) ShowLocalWindow() {
	a.showMainWindow()
}

// snapshotAICfg returns a copy of the persisted AI config under cfgMu, safe to
// read without racing a concurrent mutation.
func (a *App) snapshotAICfg() config.AIConfig {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.cfg == nil {
		return config.AIConfig{}
	}
	return a.cfg.AI
}

// GetAIServices returns the merged built-in + custom service list (minus the
// user's hidden built-ins), ordered and annotated for the rail.
func (a *App) GetAIServices() []AIServiceView {
	return mergeAIServices(builtinAIServices(), a.snapshotAICfg())
}

// OpenAIService opens (or focuses) the independent webview window for a service
// and records it as the last-used service. Returns false for an unknown/hidden
// ID or when the app isn't wired yet.
func (a *App) OpenAIService(id string) bool {
	svc, ok := findAIService(builtinAIServices(), a.snapshotAICfg(), id)
	if !ok || a.wailsApp == nil {
		return false
	}

	// Remember last-used (spec: 记住上次使用的服务). Persist best-effort.
	a.cfgMu.Lock()
	if a.cfg != nil {
		a.cfg.AI.LastServiceID = id
		_ = config.SaveTo(a.cfg, a.cfgPath)
	}
	a.cfgMu.Unlock()

	// Fast path: window already open — just focus it.
	a.mu.Lock()
	if w, ok := a.aiServiceWindows[id]; ok {
		a.mu.Unlock()
		raiseWindow(w)
		return true
	}
	a.mu.Unlock()

	window := a.wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "aisvc-" + id,
		Title:  i18n.AIServiceTitle(a.lang, svc.Name),
		Width:  1200,
		Height: 820,
		URL:    svc.URL,
		Linux:  application.LinuxWindow{Icon: appIconPNG},
	})

	// Register the close hook and the map entry under the lock, guarding against
	// a racing second OpenAIService for the same ID.
	a.mu.Lock()
	if existing, ok := a.aiServiceWindows[id]; ok {
		a.mu.Unlock()
		window.Close()
		raiseWindow(existing)
		return true
	}
	// Service windows truly close (unlike the local main window, which hides):
	// the close hook just drops the map entry so a re-open builds fresh.
	window.RegisterHook(events.Common.WindowClosing, func(_ *application.WindowEvent) {
		a.mu.Lock()
		delete(a.aiServiceWindows, id)
		a.mu.Unlock()
		slog.Info("AI service window closed", "id", id)
	})
	a.aiServiceWindows[id] = window
	a.mu.Unlock()

	raiseWindow(window)
	slog.Info("AI service window opened", "id", id, "url", svc.URL)
	return true
}

// AIActivateResult is ActivateAIService's return. Loaded is true when the picked
// service was ALREADY in the pool and loaded — the frontend uses it to skip the
// loading splash entirely and reveal instantly (returning to a recent service).
type AIActivateResult struct {
	OK     bool `json:"ok"`
	Loaded bool `json:"loaded"`
}

// ActivateAIService shows a service inside the hub's stage region. On Windows each
// service is a live WebView2 window held in a bounded LRU pool (aiPool): if the picked
// service is already pooled we just mark it active and REVEAL it (instant — page/login/
// scroll preserved); otherwise we create its window (once, HTML+guard-script so it can't
// crash the app, then SetURL), evicting the least-recently-used window if the pool is
// full. The window is left stashed off-screen until its page finishes loading, then the
// frontend clears the overlay and applyAIServiceVisibility reveals it. On non-Windows it
// falls back to OpenAIService (an independent window). Records the last-used service.
func (a *App) ActivateAIService(id string) AIActivateResult {
	if !aiEmbedSupported {
		return AIActivateResult{OK: a.OpenAIService(id)}
	}
	svc, ok := findAIService(builtinAIServices(), a.snapshotAICfg(), id)
	if !ok || a.wailsApp == nil {
		return AIActivateResult{}
	}

	// Remember last-used (spec: 记住上次使用的服务). Persist best-effort.
	a.cfgMu.Lock()
	if a.cfg != nil {
		a.cfg.AI.LastServiceID = id
		_ = config.SaveTo(a.cfg, a.cfgPath)
	}
	a.cfgMu.Unlock()

	a.mu.Lock()
	hub := a.aiHubWindow
	entry := a.aiPool[id]
	loaded := entry != nil && entry.loaded
	// Mark active BEFORE creating the window: the bootstrap loader's NavigationCompleted
	// can fire almost immediately, and its handler only sends the reveal signal when the
	// completed window IS the active service — so active must already be id by then.
	a.aiActiveService = id
	a.mu.Unlock()

	if entry == nil {
		entry = a.createServiceEntry(id, svc, hub) // may evict the LRU window
	}

	// Bump LRU (most-recently-used).
	a.mu.Lock()
	a.lruTouch(id)
	win := entry.win
	a.mu.Unlock()

	// Reparent into the hub once (Win32 owner), on the main UI thread.
	application.InvokeSync(func() {
		a.mu.Lock()
		e := a.aiPool[id]
		a.mu.Unlock()
		if e == nil || e.win == nil {
			return
		}
		if !e.embedded {
			aiEmbedOwn(hub, e.win)
			a.mu.Lock()
			e.embedded = true
			a.mu.Unlock()
		}
	})
	// Positioning + on-screen state are handled entirely by applyAIServiceVisibility:
	// a not-yet-loaded window stays stashed off-screen (behind the splash); a pooled &
	// loaded one is revealed at once. It never flashes another service's content.
	a.applyAIServiceVisibility()
	slog.Info("AI service activated", "id", id, "url", svc.URL, "pooled", loaded, "win", win != nil)
	return AIActivateResult{OK: true, Loaded: loaded}
}

// createServiceEntry builds a fresh pooled service window (HTML bootstrap + guard
// script so an external site can't crash the app, then SetURL to the real page),
// registers its navigation/close hooks, inserts it into the pool, and evicts the
// least-recently-used window if the pool now exceeds aiPoolMax. Safe against a racing
// concurrent create for the same id (returns the winner). Runs off the main thread.
func (a *App) createServiceEntry(id string, svc AIServiceView, hub *application.WebviewWindow) *aiServiceEntry {
	created := a.wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "ai-service-" + id,
		Title:  svc.Name,
		Width:  1200,
		Height: 820,
		// Create with HTML (not URL) + JS so Wails registers our injected script via
		// AddScriptToExecuteOnDocumentCreated (Wails only does that in HTML mode); the
		// registration then persists across every later SetURL navigation. The script
		// neuters chrome.webview.postMessage so an external AI site posting a non-string
		// web message can't crash the whole app (go-webview2's MessageReceived calls
		// os.Exit(1) on a bad message). The bootstrap page is an on-brand dark loading
		// animation (aiBootstrapHTML) shown until the real site paints.
		HTML:      aiBootstrapHTML(svc.Name),
		JS:        aiServiceGuardJS,
		Hidden:    true, // revealed by applyAIServiceVisibility once the page loads
		Frameless: true, // docked over the hub's stage; the hub provides the chrome
		Linux:     application.LinuxWindow{Icon: appIconPNG},
	})
	e := &aiServiceEntry{win: created}
	created.RegisterHook(events.Windows.WebViewNavigationCompleted, func(_ *application.WindowEvent) {
		a.onAIServiceNavigated(id)
	})
	// SetURL immediately so the real site's network fetch starts ASAP (fast sites stay
	// fast). WebView2 keeps showing the bootstrap loader until the real page paints.
	created.SetURL(svc.URL)
	created.RegisterHook(events.Common.WindowClosing, func(_ *application.WindowEvent) {
		a.mu.Lock()
		delete(a.aiPool, id)
		a.lruRemove(id)
		if a.aiActiveService == id {
			a.aiActiveService = ""
		}
		a.mu.Unlock()
		slog.Info("AI service window closed", "id", id)
	})

	a.mu.Lock()
	if existing, ok := a.aiPool[id]; ok { // a concurrent call already created it
		a.mu.Unlock()
		created.Close()
		return existing
	}
	a.aiPool[id] = e
	a.lruTouch(id)
	// Evict the least-recently-used window when over capacity. The just-added id is at
	// the back of the LRU list, so aiPoolOrder[0] is always a safe (older) victim.
	var evict *application.WebviewWindow
	if len(a.aiPoolOrder) > aiPoolMax {
		victim := a.aiPoolOrder[0]
		if ve, ok := a.aiPool[victim]; ok && victim != id {
			evict = ve.win
			delete(a.aiPool, victim)
			a.lruRemove(victim)
			if a.aiActiveService == victim {
				a.aiActiveService = ""
			}
		}
	}
	a.mu.Unlock()
	if evict != nil {
		evict.Close() // its own close hook is a no-op now (already removed above)
		slog.Info("AI service evicted from pool (LRU)", "keep", aiPoolMax)
	}
	return e
}

// SetAIStageRect records the hub's stage rect (physical px, client-relative) as
// reported by the frontend on load / resize / DPI change, and repositions the
// embedded service over it. No-op on non-Windows.
func (a *App) SetAIStageRect(x, y, w, h int) {
	a.mu.Lock()
	unchanged := a.aiStage == [4]int{x, y, w, h}
	a.aiStage = [4]int{x, y, w, h}
	hub := a.aiHubWindow
	win := a.revealTargetLocked()
	a.mu.Unlock()
	// The frontend's ResizeObserver fires the same rect several times per switch;
	// skip the redundant repositions (the log showed 5× identical calls). Only re-dock
	// the window that's actually on-stage — stashed (loading/overlay) windows stay
	// off-screen and pick up the new rect when next revealed.
	if win == nil || !aiEmbedSupported || unchanged {
		return
	}
	application.InvokeSync(func() { aiEmbedPosition(hub, win, x, y, w, h) })
}

// repositionActiveAIService re-docks the service window over the hub's stage using
// the last reported stage rect. Wired to the hub's move/resize events so the docked
// window follows the hub (an owned top-level window doesn't move with its owner).
// Uses InvokeAsync, NOT InvokeSync: Wails window-event hooks run on the main UI
// thread, and InvokeSync from the main thread deadlocks (froze/"crashed" the app).
func (a *App) repositionActiveAIService() {
	if !aiEmbedSupported {
		return
	}
	a.mu.Lock()
	hub := a.aiHubWindow
	down := a.shuttingDown
	win := a.revealTargetLocked()
	x, y, w, h := a.aiStage[0], a.aiStage[1], a.aiStage[2], a.aiStage[3]
	a.mu.Unlock()
	// Only follow the hub while a window is on-stage; stashed windows stay off-screen
	// (moving one here would yank a loading/overlay-hidden webview into view mid-drag).
	if win == nil || down {
		return
	}
	application.InvokeAsync(func() { aiEmbedPosition(hub, win, x, y, w, h) })
}

// ReloadActiveAIService reloads the currently-active service. It re-navigates to the
// service URL (SetURL) rather than calling ForceReload — ForceReload was a no-op on
// the docked service window, so the toolbar's reload button appeared to do nothing.
func (a *App) ReloadActiveAIService() {
	a.mu.Lock()
	id := a.aiActiveService
	var win *application.WebviewWindow
	if e := a.aiPool[id]; e != nil {
		win = e.win
		e.loaded = false // reload → gate the reveal on the fresh page's completion
	}
	a.mu.Unlock()
	if win == nil {
		return
	}
	if svc, ok := findAIService(builtinAIServices(), a.snapshotAICfg(), id); ok {
		win.SetURL(svc.URL)
		return
	}
	go webviewreset.Reload(win)
}

// AddAIService adds a user-defined service (name + URL). The URL is normalized
// (https:// prepended when no scheme). Returns the new service ID, or "" when
// name/URL are blank. Logo is auto-derived from the site favicon in the view.
func (a *App) AddAIService(name, rawURL string) string {
	url := normalizeServiceURL(rawURL)
	if url == "" {
		return ""
	}
	if name == "" {
		name = url
	}
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.cfg == nil {
		return ""
	}
	id := a.cfg.AI.AddAIService(name, url)
	_ = config.SaveTo(a.cfg, a.cfgPath)
	return id
}

// RemoveAIService removes a custom service, or hides a built-in one (built-ins
// are code-defined and can only be hidden). Any open window for the service is
// closed.
func (a *App) RemoveAIService(id string) {
	a.cfgMu.Lock()
	if a.cfg != nil {
		if !a.cfg.AI.RemoveAIService(id) {
			// Not a custom service → treat as a built-in and hide it.
			a.cfg.AI.HideBuiltin(id)
		}
		// Clear default/last if they pointed at the removed service.
		if a.cfg.AI.DefaultServiceID == id {
			a.cfg.AI.DefaultServiceID = ""
		}
		if a.cfg.AI.LastServiceID == id {
			a.cfg.AI.LastServiceID = ""
		}
		_ = config.SaveTo(a.cfg, a.cfgPath)
	}
	a.cfgMu.Unlock()

	a.mu.Lock()
	w, ok := a.aiServiceWindows[id] // non-Windows fallback window
	var pooled *application.WebviewWindow
	if e := a.aiPool[id]; e != nil { // Windows pooled window
		pooled = e.win
	}
	a.mu.Unlock()
	if ok {
		w.Close() // close hook removes the map entry
	}
	if pooled != nil {
		pooled.Close() // close hook removes the pool entry + LRU
	}
}

// SetDefaultAIService records the service auto-focused when the hub opens.
// Passing "" clears the default.
func (a *App) SetDefaultAIService(id string) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.cfg == nil {
		return
	}
	a.cfg.AI.DefaultServiceID = id
	_ = config.SaveTo(a.cfg, a.cfgPath)
}

// GetAIPrompts returns the clipboard prompt library.
func (a *App) GetAIPrompts() []config.AIPrompt {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.cfg == nil {
		return nil
	}
	out := make([]config.AIPrompt, len(a.cfg.AI.Prompts))
	copy(out, a.cfg.AI.Prompts)
	return out
}

// AddAIPrompt appends a prompt-library entry with optional tags. Returns the new
// ID, or "" when both title and content are blank.
func (a *App) AddAIPrompt(title, content string, tags []string) string {
	if title == "" && content == "" {
		return ""
	}
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.cfg == nil {
		return ""
	}
	id := a.cfg.AI.AddPrompt(title, content, tags)
	_ = config.SaveTo(a.cfg, a.cfgPath)
	return id
}

// RemoveAIPrompt drops a prompt-library entry by ID.
func (a *App) RemoveAIPrompt(id string) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.cfg == nil {
		return
	}
	if a.cfg.AI.RemovePrompt(id) {
		_ = config.SaveTo(a.cfg, a.cfgPath)
	}
}

// CopyToClipboard puts text on the system clipboard. This is how the prompt
// library works — clipboard-style, cross-service, never injected into a page
// (spec §6). Returns false if the app isn't wired.
func (a *App) CopyToClipboard(text string) bool {
	if a.wailsApp == nil || a.wailsApp.Clipboard == nil {
		return false
	}
	return a.wailsApp.Clipboard.SetText(text)
}

// OpenServiceInBrowser opens a service URL in the system default browser. This
// is the Google-login fallback (spec 已知坑 #1 / 验收): sites that block the
// embedded webview's Google OAuth ("此浏览器可能不安全") can be logged into in the
// real browser. Returns false for an unknown ID or when opening fails.
func (a *App) OpenServiceInBrowser(id string) bool {
	svc, ok := findAIService(builtinAIServices(), a.snapshotAICfg(), id)
	if !ok || a.wailsApp == nil || a.wailsApp.Browser == nil {
		return false
	}
	if err := a.wailsApp.Browser.OpenURL(svc.URL); err != nil {
		slog.Warn("open service in browser failed", "id", id, "error", err)
		return false
	}
	return true
}
