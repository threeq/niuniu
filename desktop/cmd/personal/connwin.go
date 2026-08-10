package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"github.com/niuniu-dev/niuniu-desktop/internal/config"
	"github.com/niuniu-dev/niuniu-desktop/internal/connection"
	"github.com/niuniu-dev/niuniu-desktop/internal/discovery"
	"github.com/niuniu-dev/niuniu-desktop/internal/i18n"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// connwin.go absorbs cmd/connect's remote-connection management into the merged
// personal binary (方案 A: 全量合并). The LOCAL server is connection #0 and keeps
// its dedicated fields/lifecycle in app.go (mainWindow/handle/serverAddr/sse/
// monitor, boot/RestartServer/HardResetMain). The machinery here manages
// additional REMOTE nodes the user opens on demand — these only connect, never
// spawn, and their windows truly close (vs. local close = hide to tray).
//
// Locking: connWindows / connRebuilding are guarded by a.mu (shared with the
// local fields). INVARIANT (inherited from connect): RebuildTray() acquires a.mu
// internally, so it must NEVER be called while the caller already holds a.mu.

// ConnWindow holds the runtime state for one remote connection window.
type ConnWindow struct {
	key              string                     // "host:port" — map key
	conn             config.Connection          // connection config snapshot (by value to avoid stale pointers)
	window           *application.WebviewWindow // Wails window
	monitor          *connection.Monitor        // health checker
	sse              *connection.SSEListener    // event listener
	closeUnsubscribe func()                     // deregisters the WindowClosing hook
}

// startRemoteServices starts the mDNS scanner and registers the global hotkey
// (toggle the LOCAL main window). Called once from boot() after the Wails
// message pump is running. Remote auto-connect-on-startup is intentionally NOT
// ported: 首启永远是本地主窗口，远端能力都是按需入口、绝不抢首启.
func (a *App) startRemoteServices() {
	a.scanner = discovery.NewScanner(30*time.Second, func(instances []discovery.Instance) {
		slog.Debug("mDNS discovered", "count", len(instances))
	})
	a.scanner.Start()

	// Two configurable global hotkeys, each user-configurable (change / enable /
	// disable) via 通用设置 — both read the desktop config (see hotkeywin.go):
	//   - the LOCAL main window toggle (default Ctrl+Shift+N), and
	//   - the AI-aggregation window toggle (default Ctrl+Shift+Z).
	// Separate keys so both surfaces have their own shortcut.
	_, _ = a.applyHotkeyFromConfig(hotkeyTargetWindow)
	_, _ = a.applyHotkeyFromConfig(hotkeyTargetAI)

	// Positional open-hotkeys for saved connections: Ctrl/Cmd+Shift+1..9 open the
	// Nth saved connection in list order (see connhotkey.go). Fixed keys, not
	// user-configurable; failures on individual digits are skipped.
	a.registerConnectionHotkeys()
}

// toggleMainWindow shows the LOCAL main window if hidden and hides it if visible.
// Wired to the configurable main-window global hotkey (see hotkeywin.go).
func (a *App) toggleMainWindow() {
	a.mu.Lock()
	win := a.mainWindow
	a.mu.Unlock()
	if win == nil {
		return
	}
	if win.IsVisible() {
		win.Hide()
		return
	}
	win.Restore()
	win.Show()
	// macOS: skip Focus to avoid the WebKit::ServicesController dispatch_sync
	// deadlock. See app.go openWebview for the full trace.
	if runtime.GOOS != "darwin" {
		win.Focus()
	}
}

// Connect opens or focuses a window for the given remote connection's backend.
// Returns true if a window was opened or focused, false if the click was a
// no-op (rebuild in progress, or app not wired). The silent param is retained
// for signature/binding stability but no longer affects behavior. Window
// creation and goroutine startup happen outside the lock; only the map
// read/write is protected by mu.
func (a *App) Connect(conn *config.Connection, silent bool) bool {
	key := connection.KeyFor(conn.Host, conn.Port)
	url := connection.BuildURL(conn.Host, conn.Port)

	// Fast path: window already exists — just focus it. Also bounce if a hard
	// reset is in progress for this key.
	a.mu.Lock()
	if cw, ok := a.connWindows[key]; ok {
		a.mu.Unlock()
		cw.window.Show()
		if runtime.GOOS != "darwin" {
			cw.window.Focus()
		}
		return true
	}
	if a.connRebuilding[key] {
		a.mu.Unlock()
		return false
	}
	a.mu.Unlock()

	if a.wailsApp == nil {
		return false
	}

	// Open the window immediately and let the webview load the URL — do NOT
	// pre-flight a blocking health check. A slow or briefly-unreachable server
	// otherwise swallowed the menu click ("sometimes the window won't open").
	// The per-connection Monitor started inside createAndRegisterConnWindow
	// polls health and fires ConnectionLost/Restored, and the remote SPA shows
	// its own first-paint loader while it boots; an unreachable host surfaces a
	// native webview error in the window instead of silently doing nothing.
	connName := conn.Name
	if connName == "" {
		connName = key
	}
	a.createAndRegisterConnWindow(key, conn, connName, url, nil)
	a.RebuildTray()
	slog.Info("remote window opened", "name", connName, "key", key, "url", url)
	return true
}

// --- Frontend bindings (called from the picker JS) ---

// snapshotConnections returns a copy of the saved connections taken under cfgMu,
// safe to range or hand back to Wails without racing a concurrent Add/Remove.
func (a *App) snapshotConnections() []config.Connection {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if a.cfg == nil {
		return nil
	}
	out := make([]config.Connection, len(a.cfg.Connections))
	copy(out, a.cfg.Connections)
	return out
}

func (a *App) GetConnections() []config.Connection {
	return a.snapshotConnections()
}

func (a *App) GetDiscoveredInstances() []discovery.Instance {
	if a.scanner == nil {
		return nil
	}
	return a.scanner.Instances()
}

func (a *App) AddConnection(name, host string, port int) string {
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	a.cfgMu.Lock()
	a.cfg.Connections = append(a.cfg.Connections, config.Connection{
		ID: id, Name: name, Host: host, Port: port, CreatedAt: time.Now(),
	})
	_ = config.SaveTo(a.cfg, a.cfgPath)
	a.cfgMu.Unlock()
	a.RebuildTray()
	return id
}

func (a *App) RemoveConnection(id string) {
	var removedHost string
	var removedPort int
	a.cfgMu.Lock()
	for i, c := range a.cfg.Connections {
		if c.ID == id {
			removedHost = c.Host
			removedPort = c.Port
			a.cfg.Connections = append(a.cfg.Connections[:i], a.cfg.Connections[i+1:]...)
			break
		}
	}
	_ = config.SaveTo(a.cfg, a.cfgPath)
	a.cfgMu.Unlock()

	// Close the open window for this connection if one exists.
	if removedHost != "" {
		key := connection.KeyFor(removedHost, removedPort)
		a.mu.Lock()
		cw, ok := a.connWindows[key]
		a.mu.Unlock()
		if ok {
			// Close triggers the WindowClosing handler which cleans up
			// monitor/SSE/map and calls RebuildTray, so we skip the refresh below.
			cw.window.Close()
			return
		}
	}

	a.RebuildTray()
}

// MoveConnection reorders a saved connection by swapping it with its adjacent
// neighbor: delta<0 moves it toward the front (up), delta>0 toward the back
// (down). Magnitude is ignored — it always steps one slot. List order defines the
// positional open-hotkeys (Ctrl/Cmd+Shift+1..9, see connhotkey.go), so reordering
// here immediately changes which digit targets the connection (the tray is
// rebuilt to reflect the new labels). No-op when the id is unknown or the move
// would run off either end.
func (a *App) MoveConnection(id string, delta int) {
	if delta == 0 {
		return
	}
	a.cfgMu.Lock()
	conns := a.cfg.Connections
	idx := -1
	for i := range conns {
		if conns[i].ID == id {
			idx = i
			break
		}
	}
	target := idx + 1
	if delta < 0 {
		target = idx - 1
	}
	if idx < 0 || target < 0 || target >= len(conns) {
		a.cfgMu.Unlock()
		return
	}
	conns[idx], conns[target] = conns[target], conns[idx]
	_ = config.SaveTo(a.cfg, a.cfgPath)
	a.cfgMu.Unlock()
	a.RebuildTray()
}

func (a *App) SetDefaultConnection(id string) {
	a.cfgMu.Lock()
	a.cfg.SetDefault(id)
	_ = config.SaveTo(a.cfg, a.cfgPath)
	a.cfgMu.Unlock()
}

func (a *App) ConnectByID(id string) bool {
	// Copy the matching connection out under cfgMu — Connect must not hold a
	// pointer into the shared backing array (a concurrent append could realloc).
	a.cfgMu.Lock()
	var found *config.Connection
	for i := range a.cfg.Connections {
		if a.cfg.Connections[i].ID == id {
			c := a.cfg.Connections[i]
			found = &c
			break
		}
	}
	a.cfgMu.Unlock()
	if found == nil {
		return false
	}
	return a.Connect(found, false)
}

func (a *App) ConnectToAddress(host string, port int) bool {
	// Match a saved connection first — avoids ID mismatch. Copy out under cfgMu.
	a.cfgMu.Lock()
	var found *config.Connection
	for i := range a.cfg.Connections {
		if a.cfg.Connections[i].Host == host && a.cfg.Connections[i].Port == port {
			c := a.cfg.Connections[i]
			found = &c
			break
		}
	}
	a.cfgMu.Unlock()
	if found != nil {
		return a.Connect(found, false)
	}
	conn := &config.Connection{
		ID: fmt.Sprintf("%d", time.Now().UnixNano()), Name: fmt.Sprintf("%s:%d", host, port),
		Host: host, Port: port,
	}
	return a.Connect(conn, false)
}

// ConnectFromPicker connects to a saved connection by ID and hides the picker
// window on success. Called from the picker frontend.
func (a *App) ConnectFromPicker(id string) bool {
	if a.ConnectByID(id) {
		if a.pickerWindow != nil {
			a.pickerWindow.Hide()
		}
		return true
	}
	return false
}

// ConnectToAddressFromPicker connects to an address and hides the picker window
// on success. Called from the picker frontend (manual form, discovered instances).
func (a *App) ConnectToAddressFromPicker(host string, port int) bool {
	if a.ConnectToAddress(host, port) {
		if a.pickerWindow != nil {
			a.pickerWindow.Hide()
		}
		return true
	}
	return false
}

// GetActiveConnections returns the keys ("host:port") of all open remote
// connection windows. Called by the picker frontend to show active state.
func (a *App) GetActiveConnections() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	keys := make([]string, 0, len(a.connWindows))
	for key := range a.connWindows {
		keys = append(keys, key)
	}
	return keys
}

// OpenPicker shows the connection-manager (picker) window. Bound for the SPA
// (optional in-app entry) and called from the tray.
func (a *App) OpenPicker() {
	if a.pickerWindow == nil {
		return
	}
	a.markAuxWindowOpened("picker")
	a.pickerWindow.Restore()
	a.pickerWindow.Show()
	if runtime.GOOS != "darwin" {
		a.pickerWindow.Focus()
	}
}

// TogglePickerWindow shows the connection-manager (picker) window if hidden and
// hides it if visible. Wired to the special Ctrl/Cmd+Shift+0 global hotkey (see
// connhotkey.go): the "0" slot toggles the manage-connections page rather than
// opening a positional connection.
func (a *App) TogglePickerWindow() {
	if a.pickerWindow == nil {
		return
	}
	if a.pickerWindow.IsVisible() {
		a.pickerWindow.Hide()
		return
	}
	a.OpenPicker()
}

// createAndRegisterConnWindow builds a remote webview window, starts its
// monitor + sse listeners, and registers the ConnWindow under key. existingOpts
// (nullable) supplies Width/Height/X/Y for rebuilds; when nil the defaults are
// used and the window shows immediately. Returns false if a racing goroutine
// already claimed the key.
func (a *App) createAndRegisterConnWindow(key string, conn *config.Connection, connName, url string, existingOpts *application.WebviewWindowOptions) bool {
	opts := application.WebviewWindowOptions{
		Name:   "conn-" + key,
		Title:  i18n.RemoteTitle(a.lang, connName, key),
		Width:  1280,
		Height: 800,
		// Open on the connecting slideshow splash. The splash itself polls the
		// node's health and navigates to it once reachable (see
		// connectingSplashURL), so the window shows a branded feature slideshow
		// during the connect instead of a blank-white page — and the remote SPA's
		// own first-paint loader covers its JS load after the hand-off.
		URL: connectingSplashURL(a.lang, connName, url),
	}
	if existingOpts != nil {
		if existingOpts.Width > 0 {
			opts.Width = existingOpts.Width
		}
		if existingOpts.Height > 0 {
			opts.Height = existingOpts.Height
		}
		opts.X = existingOpts.X
		opts.Y = existingOpts.Y
		opts.Hidden = true
	}
	// #526·子D/子E: inject the reverse-channel bridge scripts (diagnostic ping +
	// token + config harvesters) so the SPA registers its local runner and keeps
	// the auth token fresh. The SPA itself decides whether the "local executor"
	// entry (#526·子A) is visible purely from the raw-message bridge — there is no
	// injected __NIUNIU_DESKTOP__ global anymore (its ExecJS was unreliable on
	// remote pages, so it was removed). opts.JS is a document-created script (runs
	// before the SPA boots, on every navigation) and is the RELIABLE injection
	// path on Windows for URL-loaded remote pages; the nav-completed hook below is
	// a macOS/belt-and-suspenders re-inject.
	opts.JS = combinedBridgeJS(key)
	window := a.wailsApp.Window.NewWithOptions(opts)
	a.injectRunnerBridge(window, key)

	// #526·子D: this connection's base URL is needed to open the reverse channel
	// + fetch the workspace diff. Record it now so a started Runner can connect
	// the moment its auth token is harvested from the webview below.
	if a.runnerMgr != nil {
		a.runnerMgr.setBaseURL(key, url)
	}

	monitor := connection.NewMonitor(connection.MonitorConfig{
		URL:               url,
		Interval:          10 * time.Second,
		ReconnectInterval: 5 * time.Second,
		MaxFailures:       6,
		OnDisconnect: func() {
			if a.notifier != nil {
				a.notifier.ConnectionLost(connName, key)
			}
		},
		OnReconnect: func() {
			if a.notifier != nil {
				a.notifier.ConnectionRestored(connName, key)
			}
		},
		OnMaxFailures: func() {
			slog.Warn("max reconnect failures", "key", key)
			if a.notifier != nil {
				a.notifier.ConnectionAbandoned(connName, key)
			}
		},
	})
	monitor.Start()

	sse := connection.NewSSEListener(url+"/api/events/stream", func(eventType, content string, workspaceID int64) {
		a.mu.Lock()
		_, alive := a.connWindows[key]
		a.mu.Unlock()
		if !alive {
			return
		}
		switch eventType {
		case "agent_done":
			if a.notifier != nil && !window.IsFocused() {
				a.notifier.AgentDone(fmt.Sprintf("Workspace %d", workspaceID))
			}
		case "agent_failed":
			if a.notifier != nil {
				a.notifier.AgentFailed(fmt.Sprintf("Workspace %d", workspaceID), content)
			}
		}
	})
	sse.Start()

	a.mu.Lock()
	if existing, ok := a.connWindows[key]; ok {
		a.mu.Unlock()
		monitor.Stop()
		sse.Stop()
		window.Close()
		existing.window.Show()
		if runtime.GOOS != "darwin" {
			existing.window.Focus()
		}
		return false
	}

	// RegisterHook is synchronous in the event goroutine; saving the unsubscribe
	// func lets HardResetConnection deregister before Close. Remote close = true
	// close (cleanup monitor/sse/map), unlike the local main window (hide).
	closeUnsub := window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		a.mu.Lock()
		if a.connRebuilding[key] {
			a.mu.Unlock()
			return
		}
		if cw, ok := a.connWindows[key]; ok {
			cw.monitor.Stop()
			cw.sse.Stop()
			delete(a.connWindows, key)
		}
		shutting := a.shuttingDown
		a.mu.Unlock()
		if !shutting {
			a.RebuildTray()
		}
		slog.Info("remote connection window closed", "key", key)
	})

	a.connWindows[key] = &ConnWindow{
		key:              key,
		conn:             *conn,
		window:           window,
		monitor:          monitor,
		sse:              sse,
		closeUnsubscribe: closeUnsub,
	}
	a.mu.Unlock()

	if existingOpts != nil {
		window.Show()
	}
	return true
}

// runnerBridgeDefJS defines the cross-platform raw-message bridge the harvester
// snippets below use. The desktop shell exposes different native channels per
// webview, both routed to Go's Wails RawMessageHandler
// (App.HandleRawWebviewMessage):
//   - Windows WebView2:  window.chrome.webview.postMessage(str)
//   - macOS WKWebView:   window.webkit.messageHandlers.external.postMessage(str)
// It installs window.__nnRunnerPost(str)->bool and window.__nnRunnerBridge()->bool
// so the other snippets stay platform-agnostic. Prepended by combinedBridgeJS
// (must precede any use). Mirrors lib/desktop-runner-context.ts#rawBridge.
func runnerBridgeDefJS() string {
	return `(function(){` +
		`window.__nnRunnerPost=function(s){try{` +
		`if(window.chrome&&window.chrome.webview&&window.chrome.webview.postMessage){window.chrome.webview.postMessage(s);return true;}` +
		`if(window.webkit&&window.webkit.messageHandlers&&window.webkit.messageHandlers.external&&window.webkit.messageHandlers.external.postMessage){window.webkit.messageHandlers.external.postMessage(s);return true;}` +
		`}catch(e){}return false;};` +
		`window.__nnRunnerBridge=function(){return !!((window.chrome&&window.chrome.webview&&window.chrome.webview.postMessage)||(window.webkit&&window.webkit.messageHandlers&&window.webkit.messageHandlers.external&&window.webkit.messageHandlers.external.postMessage));};` +
		`})();`
}

// runnerRuntimeReadyJS completes Wails' JS<->Go runtime handshake through the raw
// native bridge so Go->SPA ExecJS actually reaches this window.
//
// Why it is needed: a remote-connection window opens on a data:URL splash and is
// then navigated to the REMOTE SPA — neither page ships the Wails JS runtime, so
// Wails never receives its usual `wails:runtime:ready` message and the window's
// WebviewWindow.runtimeLoaded stays false forever. While it is false, the App-side
// WebviewWindow.ExecJS SILENTLY QUEUES every script into pendingJS and never runs
// it. That is exactly why Go->SPA replies to these windows never arrive — the
// native folder-picker path (runnerwin.go pickLocalRunnerDir dispatches its
// `niuniu:runner-dir-picked` CustomEvent via win.ExecJS) and hotkey broadcasts.
// The picked directory therefore never reached the local-executor config dialog
// and the path field stayed empty (observed on macOS; the same defect hit Windows
// remote connections, whose "browse" button is the only place this path runs).
//
// Posting the literal "wails:runtime:ready" over the same native channel the
// harvesters use (window.__nnRunnerPost -> WebView2/WKWebView bridge -> Go window
// message buffer -> Window.HandleMessage) flips runtimeLoaded true and flushes the
// queue, after which ExecJS delivers normally. Guarded to post once per document;
// no-op until the bridge is present.
func runnerRuntimeReadyJS() string {
	return `(function(){try{
  if(window.__nnRuntimeReadySignaled)return;
  if(!window.__nnRunnerBridge||!window.__nnRunnerBridge())return;
  window.__nnRuntimeReadySignaled=true;
  window.__nnRunnerPost('wails:runtime:ready');
}catch(e){}})();`
}

// runnerLoadedJS posts UNCONDITIONALLY (no guard) on every injection so the
// desktop log records exactly which document the bridge script actually runs in
// (href) — the definitive probe for "did injection reach the remote SPA?". The
// bridge is confirmed available on remote pages, so if this script executes at
// all, Go receives it.
func runnerLoadedJS(connKey string) string {
	key, _ := json.Marshal(connKey)
	return `(function(){try{if(window.__nnRunnerPost){window.__nnRunnerPost(JSON.stringify({type:'niuniu-runner-loaded',connKey:` + string(key) + `,href:location.href}));}}catch(e){}})();`
}

// runnerBridgePingJS posts a one-shot diagnostic ping so the desktop log proves
// the JS->Go raw-message bridge actually delivers, independent of any saved
// config. If no bridge is present it posts nothing and leaves the guard unset so
// a later injection retries.
func runnerBridgePingJS(connKey string) string {
	key, _ := json.Marshal(connKey)
	return `(function(){try{
  if(window.__niuniuRunnerPinged)return;
  if(!window.__nnRunnerBridge||!window.__nnRunnerBridge())return;
  window.__niuniuRunnerPinged=true;
  window.__nnRunnerPost(JSON.stringify({type:'niuniu-runner-ping',connKey:` + string(key) + `}));
}catch(e){}})();`
}

// combinedBridgeJS is the full script set injected into a remote-connection
// webview: a diagnostic bridge ping and the token + config harvesters. It is
// injected BOTH as a document-created script (options.JS — runs before the SPA
// boots, on every navigation) AND on navigation-completed, because on Windows
// only the document-created path reliably fires for a URL-loaded remote page:
// the nav-completed ExecJS hook was observed NOT to run for the splash->SPA
// hand-off, so the harvesters (formerly injected only there) never posted and no
// binding was ever created.
func combinedBridgeJS(connKey string) string {
	return runnerBridgeDefJS() +
		// Flip runtimeLoaded FIRST so any queued/subsequent Go->SPA ExecJS (the
		// dir-picker reply, hotkey broadcasts, the nav-completed re-inject below)
		// actually runs on this runtime-less remote window.
		runnerRuntimeReadyJS() +
		runnerLoadedJS(connKey) +
		runnerBridgePingJS(connKey) +
		runnerTokenHarvestJS(connKey) +
		runnerConfigHarvestJS(connKey)
}

// injectRunnerBridge wires the combined bridge script into a remote connection
// window on every navigation (belt-and-suspenders alongside the document-created
// options.JS set by the caller). Both platform event IDs are registered; the
// wrong-platform one simply never fires.
func (a *App) injectRunnerBridge(window *application.WebviewWindow, connKey string) {
	js := combinedBridgeJS(connKey)
	inject := func(_ *application.WindowEvent) {
		slog.Debug("local-runner: (re)injecting bridge JS on navigation", "conn", connKey)
		window.ExecJS(js)
	}
	window.RegisterHook(events.Windows.WebViewNavigationCompleted, inject)
	window.RegisterHook(events.Mac.WebViewDidFinishNavigation, inject)
}

// runnerTokenHarvestJS returns an idempotent snippet that reads the SPA's
// persisted JWT (zustand-persist store "niuniu-auth-storage") and posts it to Go
// via the raw-message bridge (window.__nnRunnerPost → the native WebView2 or
// WKWebView channel → application RawMessageHandler → App.HandleRawWebviewMessage).
// It polls until a token appears (the user may not be signed in at first paint),
// then stops. The bridge is origin-independent, which the standard Wails runtime
// call is NOT for a remote-origin page (it targets the page origin's /wails/runtime).
func runnerTokenHarvestJS(connKey string) string {
	key, _ := json.Marshal(connKey)
	return `(function(){try{
  if(window.__niuniuRunnerTokenTimer)return;
  var connKey=` + string(key) + `;
  function post(){try{
    var raw=localStorage.getItem('niuniu-auth-storage');if(!raw)return false;
    var st=JSON.parse(raw);var tok=st&&st.state&&st.state.accessToken;if(!tok)return false;
    if(window.__nnRunnerBridge&&window.__nnRunnerBridge()){
      window.__nnRunnerPost(JSON.stringify({type:'niuniu-runner-token',connKey:connKey,token:tok}));
      return true;
    }
  }catch(e){}return false;}
  if(post())return;
  window.__niuniuRunnerTokenTimer=setInterval(function(){if(post()){clearInterval(window.__niuniuRunnerTokenTimer);window.__niuniuRunnerTokenTimer=null;}},2000);
}catch(e){}})();`
}

// runnerConfigHarvestJS returns an idempotent snippet that watches the SPA's
// persisted per-workspace local-executor configs (localStorage keys
// "niuniu.localRunner.<wsId>", written by stores/local-runner-store.ts) and posts
// each to Go — the "保存即连接" bridge (#526·子E). It mirrors runnerTokenHarvestJS:
// same raw-message channel, same 2s poll. Unlike the one-shot token
// harvester it keeps polling, because config can change or be unbound at any
// time:
//   - a new/changed config → {type:'niuniu-runner-config', connKey, workspaceId,
//     localDir}, driving RegisterLocalRunner + StartLocalRunner on the Go side;
//   - a key that disappears (bottom-bar 解绑) → {type:'niuniu-runner-unbind',
//     connKey, workspaceId}, driving Stop + Delete.
// It dedupes per workspace by the raw stored string, so a steady state posts
// nothing (idempotent, like the token poll). workspaceName is intentionally
// omitted — the localStorage payload carries only the directory config; Go
// derives the connection name from the open window.
func runnerConfigHarvestJS(connKey string) string {
	key, _ := json.Marshal(connKey)
	return `(function(){try{
  if(window.__niuniuRunnerConfigTimer)return;
  var connKey=` + string(key) + `;
  var PREFIX='niuniu.localRunner.';
  var sent={};
  function scan(){try{
    if(!window.__nnRunnerBridge||!window.__nnRunnerBridge())return;
    var seen={};
    for(var i=0;i<localStorage.length;i++){
      var k=localStorage.key(i);
      if(!k||k.indexOf(PREFIX)!==0)continue;
      var wsId=k.slice(PREFIX.length);if(!wsId)continue;
      var raw=localStorage.getItem(k);if(!raw)continue;
      var cfg;try{cfg=JSON.parse(raw);}catch(e){continue;}
      if(!cfg||typeof cfg.localDir!=='string'||!cfg.localDir)continue;
      seen[wsId]=true;
      if(sent[wsId]===raw)continue;
      sent[wsId]=raw;
      window.__nnRunnerPost(JSON.stringify({type:'niuniu-runner-config',connKey:connKey,workspaceId:wsId,localDir:cfg.localDir}));
    }
    for(var id in sent){
      if(sent.hasOwnProperty(id)&&!seen[id]){
        delete sent[id];
        window.__nnRunnerPost(JSON.stringify({type:'niuniu-runner-unbind',connKey:connKey,workspaceId:id}));
      }
    }
  }catch(e){}}
  scan();
  window.__niuniuRunnerConfigTimer=setInterval(scan,2000);
}catch(e){}})();`
}

// HardResetConnection closes the remote connection window for key and recreates
// it with the same geometry and URL. No-op on unknown key or concurrent rebuild.
func (a *App) HardResetConnection(key string) {
	a.mu.Lock()
	if a.shuttingDown || a.connRebuilding[key] {
		a.mu.Unlock()
		return
	}
	cw, ok := a.connWindows[key]
	if !ok {
		a.mu.Unlock()
		return
	}
	a.connRebuilding[key] = true
	oldWin := cw.window
	oldMon := cw.monitor
	oldSSE := cw.sse
	conn := cw.conn
	a.mu.Unlock()

	url := connection.BuildURL(conn.Host, conn.Port)
	connName := conn.Name
	if connName == "" {
		connName = key
	}

	// Stop listeners before Close.
	oldMon.Stop()
	oldSSE.Stop()

	// Create new window BEFORE closing old one — prevents a zero-window moment
	// that would make Wails quit.
	w, h := oldWin.Size()
	x, y := oldWin.Position()

	// Remove the old entry so createAndRegisterConnWindow's race-check doesn't
	// reject the new window.
	a.mu.Lock()
	delete(a.connWindows, key)
	a.mu.Unlock()

	if !a.createAndRegisterConnWindow(key, &conn, connName, url, &application.WebviewWindowOptions{
		Width: w, Height: h, X: x, Y: y,
	}) {
		slog.Error("hard reset connection: window creation failed (race lost)")
		a.mu.Lock()
		delete(a.connRebuilding, key)
		a.mu.Unlock()
		return
	}

	// New window is wired up — now safe to close the old one.
	oldWin.Close()

	a.mu.Lock()
	delete(a.connRebuilding, key)
	a.mu.Unlock()

	a.RebuildTray()
}
