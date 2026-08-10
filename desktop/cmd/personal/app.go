package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu-desktop/internal/bundle"
	"github.com/niuniu-dev/niuniu-desktop/internal/config"
	"github.com/niuniu-dev/niuniu-desktop/internal/connection"
	"github.com/niuniu-dev/niuniu-desktop/internal/discovery"
	"github.com/niuniu-dev/niuniu-desktop/internal/i18n"
	"github.com/niuniu-dev/niuniu-desktop/internal/notify"
	"github.com/niuniu-dev/niuniu-desktop/internal/probe"
	"github.com/niuniu-dev/niuniu-desktop/internal/runnerstore"
	"github.com/niuniu-dev/niuniu-desktop/internal/updater"
	"github.com/niuniu-dev/niuniu-desktop/internal/webviewreset"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// personalVersion is injected at build time via -ldflags.
// When running under `go run` it is empty and versionCompatible() will
// treat an empty/empty pair as "dev build" and accept any server.
var personalVersion = "" // set via -X github.com/.../main.personalVersion=vX.Y.Z

// Flags captured from argv before Wails app runs.
type runtimeFlags struct {
	DevURL         string // when set, skip probe+spawn and open webview on this URL
	AutoStart      bool   // marks a login-triggered launch (shows window by default)
	StartMinimized bool   // when set, start minimized to the tray
}

type App struct {
	ctx        context.Context
	wailsApp   *application.App
	mainWindow *application.WebviewWindow
	tray       *application.SystemTray
	notifier   *notify.Service
	upd        *updater.Updater
	flags      runtimeFlags
	dataDir    string

	// lang is the OS-resolved UI language ("zh"/"en"), cached once at startup
	// and used to assemble native window titles (see internal/i18n).
	lang string

	// Remote-connection management, absorbed from cmd/connect. The LOCAL server
	// is connection #0 and keeps its dedicated fields below (handle/serverAddr/
	// sse/monitor/mainWindow); these manage additional REMOTE nodes opened on
	// demand. See connwin.go. connWindows is keyed by "host:port".
	cfg            *config.DesktopConfig
	cfgPath        string
	pickerWindow   *application.WebviewWindow
	connWindows    map[string]*ConnWindow

	// auxUserOpened latches (by window Name) the auxiliary windows the user has
	// explicitly opened at least once (picker / AI hub / runners). It gates the
	// macOS first-launch re-hide guard (guardAuxWindowFirstLaunch): until a window
	// is opened on demand, a spurious macOS "become key" must not reveal it. Guarded
	// by a.mu.
	auxUserOpened map[string]bool

	// Global "本地 Runner 管理" window (#526·子C) + its desktop-owned registry.
	// runners aggregates every workspace's local execution Runner across all
	// remote connections; runnersWindow hosts the embedded /runners.html manager.
	// The store carries its own mutex, so it is not guarded by a.mu / cfgMu.
	runnersWindow *application.WebviewWindow
	runners       *runnerstore.Store
	// runnerMgr owns the live reverse-channel clients + per-connection auth
	// tokens for started Runners (#526·子D). It bridges the registry (runners)
	// to the execution engine (internal/localrunner). Carries its own mutex.
	runnerMgr      *runnerManager
	connRebuilding map[string]bool // guards concurrent HardResetConnection per key
	scanner        *discovery.Scanner
	// windowHotkeyCleanup unregisters the LOCAL main-window global toggle hotkey.
	// windowHotkeyCombo/Enabled mirror the current registration and are broadcast
	// to the settings UI (see hotkeywin.go). All three guarded by a.mu.
	windowHotkeyCleanup func()
	windowHotkeyCombo   string
	windowHotkeyEnabled bool

	// connHotkeyCleanups unregisters the positional connection open-hotkeys
	// (Ctrl/Cmd+Shift+1..9), each opening the Nth saved connection in list order.
	// See connhotkey.go. Guarded by a.mu.
	connHotkeyCleanups []func()

	// cfgMu guards all access to a.cfg / a.cfg.Connections (reads, mutations,
	// and the JSON marshal inside config.SaveTo). Picker bindings run on Wails
	// message-processor goroutines while RebuildTray reads the saved list from
	// event/close-hook goroutines, so the slice needs its own lock. Kept
	// separate from a.mu and NEVER nested with it (no path holds both).
	cfgMu sync.Mutex

	// bootOnce guards the deferred startup sequence so StartBoot runs it once.
	// bootFn overrides the boot function in tests; nil means use a.boot.
	bootOnce sync.Once
	bootFn   func()

	mu                   sync.Mutex
	handle               *bundle.Handle
	reusedServer         bool
	serverAddr           string
	sse                  *connection.SSEListener
	monitor              *connection.Monitor
	shuttingDown         bool
	restarting           bool
	rebuilding           bool
	mainCloseUnsubscribe func() // saved from RegisterHook, used to deregister before HardResetMain

	// bootLock is held for the app's entire lifetime to enforce single-instance.
	// A second personal launch will fail to acquire this lock and exit silently.
	// Released in ServiceShutdown.
	bootLock *probe.BootLock

	// petWindow is reserved for the future pet-mode overlay window.
	// Not populated in the MVP; mentioned in spec §6.1 and §12.
	petWindow *application.WebviewWindow

	// AI-aggregation window ("AI 直达"). aiHubWindow is the single hub window: it
	// hosts the embedded frontend (left rail + topbar + an empty "stage" region).
	//
	// Content model (spec 方案: 单窗口聚合切换): on Windows each service is a live
	// WebView2 window kept in a BOUNDED LRU POOL (aiPool, cap aiPoolMax) and docked
	// over the hub's stage. Switching to a service already in the pool just REVEALS
	// its window (instant — the page, login and scroll position are all still there),
	// instead of re-navigating a single shared webview (which reloaded on every click).
	// Only a brand-new service pays a load; when the pool overflows the least-recently
	// used window is closed (aiPoolOrder is the LRU list, front = oldest). An earlier
	// single-reused-window design existed only because rapid switching "crashed" — that
	// crash was actually the non-string-postMessage os.Exit(1) (now fixed by the
	// injected guard script), NOT the window count, so a small pool is safe. On
	// non-Windows we fall back to independent per-service windows (aiServiceWindows;
	// see aiembed_other.go).
	//
	// aiActiveService is the service meant to be on-stage; which window is actually
	// revealed is derived on demand from live state (revealTargetLocked). aiStage is
	// the last stage rect reported by the frontend (physical px, client-relative). All
	// guarded by a.mu. aiHotkeyCleanup unregisters the global toggle hotkey at shutdown.
	aiHubWindow      *application.WebviewWindow
	aiPool           map[string]*aiServiceEntry            // Windows: live service webviews (LRU)
	aiPoolOrder      []string                              // Windows: LRU order, front = least-recently-used
	aiServiceWindows map[string]*application.WebviewWindow // non-Windows fallback: one window per service
	aiActiveService  string
	aiStage          [4]int
	// aiHubVisible / aiOverlayOpen drive the docked service window's visibility
	// DETERMINISTICALLY (no reliance on flaky WM_SHOWWINDOW events / ExecJS): the
	// service shows iff the hub is visible (set explicitly in OpenAIWindow /
	// ToggleAIWindow / the hub close hook) AND a service is active AND no HTML
	// overlay (modal / settings / loading splash) is up (reported by the frontend
	// via SetAIOverlayOpen). Guarded by a.mu.
	aiHubVisible    bool
	aiOverlayOpen   bool
	aiHotkeyCleanup func()
	// aiHotkeyCombo is the human-readable label of the global hotkey that actually
	// bound (RegisterAI falls back through candidates when Ctrl+Shift+A is taken).
	// Empty if none registered. Surfaced to the hub UI via GetAIHotkey. Guarded by a.mu.
	aiHotkeyCombo string
	// aiHotkeyEnabled mirrors config.Hotkey.ToggleAIEnabled for the current
	// registration; false when the user disabled the shortcut. Guarded by a.mu.
	aiHotkeyEnabled bool
}

func NewApp(flags runtimeFlags, dataDir string) *App {
	cfg, err := config.Load()
	if err != nil {
		slog.Warn("failed to load desktop config, using defaults", "error", err)
		cfg = &config.DesktopConfig{
			Notifications: true,
			Hotkey: config.HotkeyConfig{
				ToggleWindow:        config.DefaultWindowAccelerator(),
				ToggleWindowEnabled: true,
				ToggleAI:            config.DefaultAIAccelerator(),
				ToggleAIEnabled:     true,
			},
		}
	}
	runners := runnerstore.New(filepath.Join(config.DefaultDir(), "runners.json"))
	if err := runners.Load(); err != nil {
		slog.Warn("failed to load local-runner registry, starting empty", "error", err)
	}
	lang := i18n.DetectLang()
	runnerMgr := newRunnerManager(runners, newNativeApprover(lang), filepath.Join(config.DefaultDir(), "runner-audit"))
	return &App{
		flags:            flags,
		dataDir:          dataDir,
		lang:             lang,
		cfg:              cfg,
		cfgPath:          config.DefaultPath(),
		connWindows:      make(map[string]*ConnWindow),
		connRebuilding:   make(map[string]bool),
		auxUserOpened:    make(map[string]bool),
		runners:          runners,
		runnerMgr:        runnerMgr,
		aiPool:           make(map[string]*aiServiceEntry),
		aiServiceWindows: make(map[string]*application.WebviewWindow),
	}
}

func (a *App) SetWails(app *application.App, mainWin *application.WebviewWindow, tray *application.SystemTray, n *notify.Service) {
	a.wailsApp = app
	a.mainWindow = mainWin
	a.tray = tray
	a.notifier = n
	// A reverse-channel 401 means the harvested token expired: ask that
	// connection's webview (the sole owner of the rotating refresh token) to
	// refresh + re-post a fresh one. Wired here because it needs the live windows.
	if a.runnerMgr != nil {
		a.runnerMgr.onAuthExpired = a.reharvestConnToken
	}
}

func (a *App) registerMainCloseHookOn(win *application.WebviewWindow) {
	a.mainCloseUnsubscribe = win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		a.mu.Lock()
		rebuilding := a.rebuilding
		a.mu.Unlock()
		if rebuilding {
			return
		}
		e.Cancel()
		win.Hide()
	})
}

// markAuxWindowOpened latches an auxiliary window (by Name) as user-opened, so the
// first-launch re-hide guard stops interfering once the user has raised it on
// demand. Safe to call with a.mu unheld.
func (a *App) markAuxWindowOpened(name string) {
	a.mu.Lock()
	if a.auxUserOpened == nil {
		a.auxUserOpened = make(map[string]bool)
	}
	a.auxUserOpened[name] = true
	a.mu.Unlock()
}

// auxWindowOpened reports whether the user has explicitly opened the named
// auxiliary window at least once. Nil-map safe (returns false).
func (a *App) auxWindowOpened(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.auxUserOpened[name]
}

// guardAuxWindowFirstLaunch keeps an auxiliary window (picker / AI hub / runners)
// hidden on first launch even though it was created with Hidden:true, so only the
// local main window (牛牛·本地) appears on boot (方案 A: 绝不抢首启).
//
// Why this is needed: Wails v3 alpha.74's macOS window run() has an else-branch
// (webview_window_darwin.go) that, for a Hidden window, calls Show() the FIRST time
// the window "becomes key" — a shadow-removal hack. During the 1–5s boot gap before
// the local main window paints (its embedded server has to spawn first), these
// instantly-loaded embedded windows can be spuriously keyed and thus revealed, so
// the user is greeted by a stack of windows instead of just the local one. Windows
// and Linux honor Hidden at creation (webview_window_{windows,linux}.go), and
// events.Mac.WindowDidBecomeKey never fires there, so this guard is inert off macOS.
//
// The guard re-hides the window whenever it becomes key while the user has not yet
// opened it on demand (auxWindowOpened == false; flipped true by markAuxWindowOpened
// in Open{Picker,AIWindow,RunnersWindow}). OnWindowEvent listeners run on their own
// goroutine, so the short re-hide loop below can outlast Wails' one-shot Show()
// (which cancels itself after firing once) without blocking the UI thread.
func (a *App) guardAuxWindowFirstLaunch(win *application.WebviewWindow, name string) {
	win.OnWindowEvent(events.Mac.WindowDidBecomeKey, func(_ *application.WindowEvent) {
		if a.auxWindowOpened(name) {
			return
		}
		// Wails' own becomeKey handler Show()s the window exactly once. Re-hide a few
		// times over a short window so our Hide lands AFTER that Show regardless of
		// goroutine scheduling; once the one-shot has fired, the final Hide sticks.
		for i := 0; i < 5; i++ {
			if a.auxWindowOpened(name) {
				return
			}
			win.Hide()
			time.Sleep(50 * time.Millisecond)
		}
	})
}

// ServiceStartup runs synchronously inside Wails' Run() BEFORE the platform
// message loop starts and before the deferred window/tray are created. It must
// therefore do NO blocking work and touch NO window/tray method (those route
// through InvokeSync, which deadlocks against a pump that has not started yet —
// the "process alive but no window/no tray" bug). All heavy boot work is
// deferred to StartBoot, wired to events.Common.ApplicationStarted in main().
func (a *App) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	a.ctx = ctx
	slog.Info("ServiceStartup (deferring boot to ApplicationStarted)", "data_dir", a.dataDir, "dev_url", a.flags.DevURL)
	return nil
}

// AcquireSingleInstance creates the data dir and grabs the boot-lock so only one
// niuniu-desktop runs at a time. It is called from main() BEFORE the Wails app
// is built, so a second launch exits without ever flashing a window or tray
// icon. Returns false if another instance already holds the lock (or the data
// dir cannot be created) — the caller should exit. The lock is held for the
// app's lifetime and released in ServiceShutdown.
func (a *App) AcquireSingleInstance() bool {
	if err := os.MkdirAll(a.dataDir, 0o755); err != nil {
		slog.Error("create data dir failed; cannot start", "error", err, "data_dir", a.dataDir)
		return false
	}
	bootLockPath := filepath.Join(a.dataDir, "personal.boot.lock")
	slog.Info("step: AcquireBootLock", "path", bootLockPath)
	bootLock, err := probe.AcquireBootLock(bootLockPath)
	if err != nil {
		// Another personal is already running. Enforce single-instance:
		// exit silently rather than opening a second window against the
		// same server.
		slog.Info("another niuniu-desktop is already running; exiting silently", "error", err)
		return false
	}
	slog.Info("step: AcquireBootLock done")
	a.mu.Lock()
	a.bootLock = bootLock
	a.mu.Unlock()
	return true
}

// StartBoot launches the heavy startup sequence exactly once, on a background
// goroutine. It MUST be invoked only after the Wails event loop is running (we
// wire it to events.Common.ApplicationStarted) so the window/tray operations
// inside boot — which dispatch onto the main thread via InvokeSync — are
// actually serviced by the pump instead of blocking forever against a loop that
// has not started.
func (a *App) StartBoot() {
	a.bootOnce.Do(func() {
		fn := a.bootFn
		if fn == nil {
			fn = a.boot
		}
		go fn()
	})
}

// boot performs the real startup work: probe → reuse/spawn the embedded server →
// open the webview → build the tray. It runs on a background goroutine after the
// message loop is up (see StartBoot). The window already shows the loading splash
// by this point (Wails created+showed it from pendingRun), so a slow spawn no
// longer leaves the user staring at an empty desktop.
func (a *App) boot() {
	slog.Info("boot begin", "data_dir", a.dataDir, "dev_url", a.flags.DevURL)
	defer slog.Info("boot return")

	// Remote-connection services (mDNS scanner + global hotkey). Started here,
	// after the message pump is up, so the hotkey/scanner setup is serviced like
	// the rest of boot. These do not touch the local server and run in all modes.
	a.startRemoteServices()

	if a.flags.DevURL != "" {
		slog.Info("dev-url mode; skipping probe/spawn", "url", a.flags.DevURL)
		a.serverAddr = hostPortFromURL(a.flags.DevURL)
		a.openWebview(a.flags.DevURL, true)
		a.startListeners()
		go a.checkUpdates()
		return
	}

	// Single-instance boot-lock + data-dir creation already happened in
	// AcquireSingleInstance (main(), before the Wails app was built).

	// Legacy-config scrub: older niuniu-desktop builds persisted the relay
	// password in plaintext inside ~/.niuniu/desktop/config.json.  Now that
	// the server owns the keychain-backed credential store, that plaintext
	// is pure liability.  Zero it out if present; the user will be prompted
	// to log in again through the browser UI on first launch.
	slog.Info("step: scrubLegacyRelayPassword")
	scrubLegacyRelayPassword()
	slog.Info("step: scrubLegacyRelayPassword done")

	// Step: decide reuse vs spawn vs refuse.
	slog.Info("step: probe.Decide", "version", personalVersion)
	decision, err := probe.Decide(a.dataDir, personalVersion)
	if err != nil {
		slog.Error("probe.Decide failed", "error", err)
		a.showStartupError(err)
		return
	}
	slog.Info("step: probe.Decide done",
		"refuse", decision.Refuse,
		"reuse", decision.Reuse != nil)
	if decision.Refuse != "" {
		slog.Warn("refusing to launch", "reason", decision.Refuse)
		a.showStartupError(fmt.Errorf("%s", decision.Refuse))
		return
	}
	if decision.Reuse != nil {
		slog.Info("reusing existing server", "addr", decision.Reuse.Addr, "source", decision.Reuse.Source)
		a.reusedServer = true
		a.serverAddr = decision.Reuse.Addr
		a.startListeners()
		// Personal edition is "开箱即用": no blocking setup wall. Missing
		// node/git/claude are surfaced in-app by the System Dependencies landing
		// gate (lib/system-deps-gate.ts), which offers one-click install and
		// Claude OAuth login — far better than a raw-HTML prereq page (#411).
		slog.Info("step: openWebview (reuse)", "url", "http://"+decision.Reuse.Addr+"/", "autostart", a.flags.AutoStart, "minimized", a.flags.StartMinimized)
		a.openWebview("http://"+decision.Reuse.Addr+"/", !a.flags.StartMinimized)
		slog.Info("step: openWebview done (reuse)")
		// RebuildTray after openWebview: on macOS 12.x + Wails v3 alpha-74,
		// rebuilding the NSStatusItem menu from a Wails service goroutine
		// before openWebview's Show+Focus deadlocks the AppKit main thread
		// (window stuck on data: loading page, tray menu unresponsive).
		// Reordering keeps the menu correct (Restart Server enabled once
		// handle is set) without sequencing two Cocoa-touching calls back-
		// to-back from the same goroutine.
		a.RebuildTray()
		go a.checkUpdates()
		return
	}

	// Step: spawn our own embedded server.
	logPath := filepath.Join(a.dataDir, "logs", "embedded-server.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	slog.Info("step: bundle.Spawn", "log", logPath)
	h, err := bundle.Spawn(a.ctx, bundle.Spec{
		DataDir:          a.dataDir,
		LogFile:          logPath,
		LogRetentionDays: 30,
		Timeout:          60 * time.Second,
		OnCrash:          a.onServerCrash,
	})
	if err != nil {
		slog.Error("bundle.Spawn failed", "error", err)
		a.showStartupError(err)
		return
	}
	slog.Info("step: bundle.Spawn done", "addr", h.Addr)
	a.mu.Lock()
	a.handle = h
	a.serverAddr = h.Addr
	a.mu.Unlock()
	slog.Info("embedded server ready", "addr", h.Addr)
	slog.Info("step: startListeners")
	a.startListeners()
	slog.Info("step: startListeners done")
	// No blocking first-run wall — see the reuse-path comment above (#411).
	slog.Info("step: openWebview", "url", "http://"+h.Addr+"/", "autostart", a.flags.AutoStart, "minimized", a.flags.StartMinimized)
	a.openWebview("http://"+h.Addr+"/", !a.flags.StartMinimized)
	slog.Info("step: openWebview done")
	// See reuse-path RebuildTray comment for the macOS/Wails reasoning.
	a.RebuildTray()
	go a.checkUpdates()
}

func (a *App) openWebview(url string, show bool) {
	// Per-call tracepoints: a webview2 crash here typically takes the whole
	// process down without a Go-recoverable error. The "begin … done" pairs
	// let us read the log and pinpoint which call vanished mid-flight.
	slog.Debug("openWebview SetURL begin", "url", url, "show", show)
	// Carry the hotkey config in the URL hash so the settings SPA reads it
	// synchronously on load (see hotkeywin.go hotkeyURLHash / web main.tsx).
	a.mainWindow.SetURL(a.withHotkeyHash(url))
	slog.Debug("openWebview SetURL done")
	// Autostart (login launch): load the URL so the window is ready, but keep it
	// hidden in the tray. The user reveals it via the tray icon / menu.
	if !show {
		slog.Debug("openWebview staying hidden (autostart)")
		a.mainWindow.Hide()
		return
	}
	slog.Debug("openWebview Restore begin")
	a.mainWindow.Restore()
	slog.Debug("openWebview Restore done")
	slog.Debug("openWebview Show begin")
	a.mainWindow.Show()
	slog.Debug("openWebview Show done")
	// macOS 12.x WKWebView deadlock: Focus() -> [NSWindow makeKeyAndOrderFront:]
	// transitions WKWebView to first-responder, which lazily initializes
	// WebKit::ServicesController via std::call_once. That init does a
	// dispatch_sync(mainQueue) to enumerate system Services — but main is
	// currently blocked inside makeKeyAndOrderFront waiting for WKWebView
	// to settle. Three-way deadlock: app freezes silently on every launch
	// (window stuck on data: loading page, tray icon unresponsive).
	// Confirmed via `sample` on v1.0.20 Monterey 12.1 — WebKit.ServicesController
	// queue stuck in __DISPATCH_WAIT_FOR_QUEUE__ while main is in
	// _pthread_cond_wait. Apple fixed WebKit's sync-dispatch in 13+.
	// Show() already orderFronts the window so it's visible; we lose
	// auto-key-window on first launch (user has to click to focus the
	// webview content), acceptable vs. permanently-frozen app.
	if runtime.GOOS != "darwin" {
		slog.Debug("openWebview Focus begin")
		a.mainWindow.Focus()
		slog.Debug("openWebview Focus done")
	}
}

func (a *App) onServerCrash(err error) {
	slog.Error("embedded server crashed", "error", err)
	// Restart logic wired in Task E4 / E5 via RestartServer().
}

func (a *App) showStartupError(err error) {
	// Minimal: render a small about:blank page with error message.
	html := `<html><body style="font-family:system-ui;padding:32px"><h1>无法启动 牛牛桌面版</h1><p>` + htmlEscape(err.Error()) + `</p></body></html>`
	a.mainWindow.SetHTML(html)
	a.mainWindow.Show()
}

func (a *App) startListeners() {
	url := "http://" + a.serverAddr

	// Health monitor
	monitor := connection.NewMonitor(connection.MonitorConfig{
		URL:               url,
		Interval:          10 * time.Second,
		ReconnectInterval: 5 * time.Second,
		MaxFailures:       6,
		OnDisconnect: func() {
			a.mu.Lock()
			ours := a.handle != nil && !a.reusedServer
			a.mu.Unlock()
			if ours {
				slog.Warn("health failure — will attempt restart")
				_ = a.RestartServer()
			} else if a.notifier != nil {
				a.notifier.ConnectionLost("external server", a.serverAddr)
			}
		},
		OnReconnect: func() {
			slog.Info("health restored")
		},
		OnMaxFailures: func() {
			slog.Error("health check exhausted retries", "addr", a.serverAddr)
		},
	})
	monitor.Start()

	// SSE listener (agent done/failed notifications + desktop UI signals).
	sse := connection.NewSSEListener(url+"/api/events/stream", func(eventType, content string, workspaceID int64) {
		// "open_ai_window" is the SPA→desktop bridge for the top-nav AI 直达
		// button (personal edition): the local server publishes it when the
		// button is clicked, and we raise the native AI hub here. Handled before
		// the notifier guard since it needs no notifier.
		if eventType == "open_ai_window" {
			a.OpenAIWindow()
			return
		}
		if a.notifier == nil {
			return
		}
		switch eventType {
		case "agent_done":
			if !a.mainWindow.IsFocused() {
				a.notifier.AgentDone(fmt.Sprintf("Workspace %d", workspaceID))
			}
		case "agent_failed":
			a.notifier.AgentFailed(fmt.Sprintf("Workspace %d", workspaceID), content)
		}
	})
	sse.Start()

	a.mu.Lock()
	a.monitor = monitor
	a.sse = sse
	a.mu.Unlock()
}

func hostPortFromURL(u string) string {
	// Accept http://host:port[/...] and return host:port. Naive; OK for dev-url.
	s := strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// RebuildTray constructs the unified tray menu: a LOCAL top block (show/reload/
// rebuild/restart-server for connection #0), then active REMOTE connection
// submenus, saved-but-inactive connections, mDNS-discovered nodes, the picker
// entry, mobile access, and Quit.
//
// INVARIANT (inherited from connect's RefreshTrayMenu): must NOT be called while
// the caller already holds a.mu — it acquires a.mu internally to snapshot state.
func (a *App) RebuildTray() {
	if a.tray == nil || a.wailsApp == nil {
		return
	}

	// Snapshot lock-guarded state once, then build the menu lock-free.
	type activeEntry struct{ key, name string }
	a.mu.Lock()
	if a.shuttingDown {
		a.mu.Unlock()
		return
	}
	canRestart := a.handle != nil && !a.reusedServer
	activeKeys := make(map[string]bool, len(a.connWindows))
	activeList := make([]activeEntry, 0, len(a.connWindows))
	for key, cw := range a.connWindows {
		activeKeys[key] = true
		activeList = append(activeList, activeEntry{key: key, name: cw.conn.Name})
	}
	activeCount := len(a.connWindows)
	a.mu.Unlock()

	// Snapshot the saved connections once (under cfgMu, inside snapshotConnections)
	// and map each connection key to its 1-based position in list order. The
	// positional open-hotkeys (Ctrl/Cmd+Shift+1..9, see connhotkey.go) target the
	// same order, so shortcutSuffix appends the matching label to the tray entry —
	// on both the active-submenu title and the inactive saved line. Only the first
	// connHotkeyMax positions get a shortcut (digits 1-9).
	saved := a.snapshotConnections()
	posByKey := make(map[string]int, len(saved))
	for i, conn := range saved {
		if i >= connHotkeyMax {
			break
		}
		k := connection.KeyFor(conn.Host, conn.Port)
		if _, exists := posByKey[k]; !exists {
			posByKey[k] = i + 1
		}
	}
	shortcutSuffix := func(key string) string {
		if pos, ok := posByKey[key]; ok {
			return "  " + connHotkeyLabel(pos)
		}
		return ""
	}

	menu := a.wailsApp.Menu.New()

	// --- LOCAL block (connection #0) ---
	menu.Add("Show Niuniu").OnClick(func(ctx *application.Context) {
		a.mainWindow.Restore()
		a.mainWindow.Show()
		// macOS: skip Focus to avoid WebKit::ServicesController deadlock.
		// See openWebview comment for full diagnosis.
		if runtime.GOOS != "darwin" {
			a.mainWindow.Focus()
		}
	})
	menu.Add("刷新页面").OnClick(func(ctx *application.Context) {
		a.mu.Lock()
		win := a.mainWindow
		a.mu.Unlock()
		go webviewreset.Reload(win)
	})
	menu.Add("重建窗口").OnClick(func(ctx *application.Context) {
		a.HardResetMain()
	})
	restart := menu.Add("Restart Server")
	if !canRestart {
		restart.SetEnabled(false)
	} else {
		restart.OnClick(func(ctx *application.Context) {
			if err := a.RestartServer(); err != nil {
				slog.Error("restart server", "error", err)
			}
		})
	}

	// --- AI 直达 (independent AI-aggregation window; its own group, right under
	// the local block so it never splits the remote-nodes group below). ---
	menu.AddSeparator()
	menu.Add(i18n.AITitle(a.lang) + "…").OnClick(func(ctx *application.Context) {
		a.OpenAIWindow()
	})
	// 本地 Runner 管理 (#526·子C): global manager for local execution Runners across
	// all remote connections. Grouped with the AI hub as an independent desktop
	// surface, above the remote-nodes group.
	menu.Add(i18n.T(a.lang, i18n.KeyRunners) + "…").OnClick(func(ctx *application.Context) {
		a.OpenRunnersWindow()
	})

	// --- REMOTE-NODES group: active connections, saved nodes, discovered nodes
	// AND the connect/manage entry all live in ONE group (they are all "other
	// nodes"). A single leading separator opens the group; no separators split it. ---
	menu.AddSeparator()

	// Active REMOTE connections — submenu per connection.
	if len(activeList) > 0 {
		for _, entry := range activeList {
			entry := entry
			sub := menu.AddSubmenu("● " + entry.name + " (" + entry.key + ")" + shortcutSuffix(entry.key))
			sub.Add("聚焦").OnClick(func(ctx *application.Context) {
				a.mu.Lock()
				cw, ok := a.connWindows[entry.key]
				a.mu.Unlock()
				if ok {
					cw.window.Restore()
					cw.window.Show()
					if runtime.GOOS != "darwin" {
						cw.window.Focus()
					}
				}
			})
			sub.Add("刷新页面").OnClick(func(ctx *application.Context) {
				a.mu.Lock()
				cw, ok := a.connWindows[entry.key]
				a.mu.Unlock()
				if ok {
					go webviewreset.Reload(cw.window)
				}
			})
			sub.Add("重建窗口").OnClick(func(ctx *application.Context) {
				a.HardResetConnection(entry.key)
			})
			sub.AddSeparator()
			sub.Add("关闭连接").OnClick(func(ctx *application.Context) {
				a.mu.Lock()
				cw, ok := a.connWindows[entry.key]
				a.mu.Unlock()
				if ok {
					cw.window.Close()
				}
			})
		}
	}

	// --- Saved connections that are NOT currently active ---
	// `saved` was snapshotted above (under cfgMu) so a concurrent
	// AddConnection/RemoveConnection append can't race this range.
	for _, conn := range saved {
		key := connection.KeyFor(conn.Host, conn.Port)
		if activeKeys[key] {
			continue
		}
		conn := conn
		menu.Add(conn.Name + " (" + key + ")" + shortcutSuffix(key)).OnClick(func(ctx *application.Context) {
			a.Connect(&conn, false)
		})
	}

	// --- Discovered instances (mDNS) — same REMOTE-NODES group ---
	if a.scanner != nil {
		instances := a.scanner.Instances()
		if len(instances) > 0 {
			menu.Add("Discovered").SetEnabled(false)
			for _, inst := range instances {
				inst := inst
				name := inst.Hostname
				if name == "" {
					name = inst.Host
				}
				menu.Add("  " + name + " (" + inst.Host + ")").OnClick(func(ctx *application.Context) {
					a.ConnectToAddress(inst.Host, inst.Port)
				})
			}
		}
	}

	// Connect to / manage other nodes → opens the picker window. Last item of the
	// REMOTE-NODES group so it sits WITH the connection entries (spec: 接入其他节点
	// 应与远程连接同组), not isolated on its own.
	menu.Add("连接其他节点 / 管理连接…" + "  " + connHotkeyLabel(0)).OnClick(func(ctx *application.Context) {
		a.OpenPicker()
	})

	menu.AddSeparator()
	// Mobile access — opens the local main window on Settings → 移动接入.
	menu.Add("移动接入…").OnClick(func(ctx *application.Context) {
		a.openMobileAccessSettings()
	})
	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(ctx *application.Context) {
		a.wailsApp.Quit()
	})

	a.tray.SetMenu(menu)

	// Tooltip reflects active remote count.
	if activeCount > 0 {
		a.tray.SetTooltip(fmt.Sprintf("%s — %d active", i18n.AppName(a.lang), activeCount))
	} else {
		a.tray.SetTooltip(i18n.AppName(a.lang))
	}
}

// RestartServer terminates the current embedded-server subprocess and
// spawns a fresh one. No-op if we don't own the running server, are
// shutting down, or a restart is already in progress.
func (a *App) RestartServer() error {
	a.mu.Lock()
	if a.shuttingDown || a.restarting || a.rebuilding {
		a.mu.Unlock()
		return nil
	}
	if a.handle == nil {
		a.mu.Unlock()
		return nil
	}
	a.restarting = true
	old := a.handle
	oldSSE := a.sse
	oldMon := a.monitor
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		a.restarting = false
		a.mu.Unlock()
	}()

	// Stop listeners pointed at the old addr BEFORE shutting down the server,
	// so Monitor.OnDisconnect doesn't fire during the transition.
	if oldSSE != nil {
		oldSSE.Stop()
	}
	if oldMon != nil {
		oldMon.Stop()
	}

	if err := old.Shutdown(); err != nil {
		return err
	}
	_ = old.Wait()

	logPath := filepath.Join(a.dataDir, "logs", "embedded-server.log")
	h, err := bundle.Spawn(a.ctx, bundle.Spec{
		DataDir:          a.dataDir,
		LogFile:          logPath,
		LogRetentionDays: 30,
		Timeout:          60 * time.Second,
		OnCrash:          a.onServerCrash,
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.handle = h
	a.serverAddr = h.Addr
	a.sse = nil
	a.monitor = nil
	a.mu.Unlock()

	a.mainWindow.SetURL(a.withHotkeyHash("http://" + h.Addr + "/"))
	a.startListeners() // rebind Monitor + SSE to new addr
	a.RebuildTray()
	return nil
}

// checkUpdates runs once per launch. Non-blocking, errors silent.
func (a *App) checkUpdates() {
	if a.upd == nil {
		return
	}
	res, err := a.upd.Check()
	if err != nil {
		slog.Debug("update check failed", "error", err)
		return
	}
	if res.Available && a.notifier != nil {
		a.notifier.UpdateAvailable(res.Version)
	}
}

// openMobileAccessSettings routes the main window to the Settings → 移动接入
// page. The React SPA drives relay login + pair + trusted list entirely via
// server HTTP endpoints (/api/relay/*), so the desktop shell just navigates.
func (a *App) openMobileAccessSettings() {
	a.mu.Lock()
	win := a.mainWindow
	addr := a.serverAddr
	a.mu.Unlock()
	if win == nil || addr == "" {
		return
	}
	win.SetURL(a.withHotkeyHash("http://" + addr + "/settings?tab=mobile-access"))
	win.Restore()
	win.Show()
	// macOS: skip Focus — see openWebview comment.
	if runtime.GOOS != "darwin" {
		win.Focus()
	}
}

func (a *App) ServiceShutdown() error {
	slog.Info("niuniu-desktop shutting down")

	// Tear down live local-runner reverse channels first so their goroutines
	// unwind before the rest of shutdown (#526·子D). Safe if none are running.
	if a.runnerMgr != nil {
		a.runnerMgr.stopAll()
	}

	// Stop the global hotkeys first (OS-level, no lock needed).
	if a.windowHotkeyCleanup != nil {
		a.windowHotkeyCleanup()
	}
	if a.aiHotkeyCleanup != nil {
		a.aiHotkeyCleanup()
	}
	for _, cleanup := range a.connHotkeyCleanups {
		if cleanup != nil {
			cleanup()
		}
	}

	a.mu.Lock()
	a.shuttingDown = true
	sse := a.sse
	monitor := a.monitor
	handle := a.handle
	bootLock := a.bootLock
	scanner := a.scanner
	// Close the docked AI-service windows (owned windows of the hub) up front so
	// their owner relationship can't confuse Wails' window teardown order at quit.
	aiSvcs := make([]*application.WebviewWindow, 0, len(a.aiPool))
	for id, e := range a.aiPool {
		if e != nil && e.win != nil {
			aiSvcs = append(aiSvcs, e.win)
		}
		delete(a.aiPool, id)
	}
	a.aiPoolOrder = nil
	a.aiActiveService = ""
	// Snapshot remote listeners and clear the map under the lock; stop them
	// AFTER releasing a.mu. The remote SSE callback takes a.mu (connwin.go), so
	// stopping listeners while holding a.mu would self-deadlock the moment
	// Stop() ever becomes synchronous. shuttingDown=true makes the close hooks
	// skip RebuildTray.
	remoteListeners := make([]*ConnWindow, 0, len(a.connWindows))
	for key, cw := range a.connWindows {
		remoteListeners = append(remoteListeners, cw)
		delete(a.connWindows, key)
	}
	cfgPath := a.cfgPath
	a.mu.Unlock()

	for _, w := range aiSvcs {
		w.Close()
	}

	for _, cw := range remoteListeners {
		cw.monitor.Stop()
		cw.sse.Stop()
	}
	if sse != nil {
		sse.Stop()
	}
	if monitor != nil {
		monitor.Stop()
	}
	if scanner != nil {
		scanner.Stop()
	}
	if handle != nil {
		_ = handle.Shutdown()
		_ = handle.Wait()
	}
	a.cfgMu.Lock()
	if a.cfg != nil {
		_ = config.SaveTo(a.cfg, cfgPath)
	}
	a.cfgMu.Unlock()
	if bootLock != nil {
		_ = bootLock.Release()
	}
	return nil
}

func (a *App) HardResetMain() {
	a.mu.Lock()
	if a.shuttingDown || a.restarting || a.rebuilding {
		a.mu.Unlock()
		return
	}
	a.rebuilding = true
	oldSSE := a.sse
	oldMon := a.monitor
	oldWin := a.mainWindow
	url := a.withHotkeyHash("http://" + a.serverAddr + "/")
	a.mu.Unlock()

	if oldSSE != nil {
		oldSSE.Stop()
	}
	if oldMon != nil {
		oldMon.Stop()
	}

	// Create new window BEFORE closing old one — Wails quits when zero
	// windows exist, so we must have the replacement ready first.
	newWin := webviewreset.Rebuild(oldWin, webviewreset.RebuildSpec{
		Name:  "main",
		Title: i18n.LocalTitle(a.lang),
		URL:   url,
	})
	if newWin == nil {
		slog.Error("hard reset: Rebuild returned nil window")
		a.mu.Lock()
		a.mainWindow = nil
		a.rebuilding = false
		a.mu.Unlock()
		return
	}

	// Wire up the new window: register close hook, re-publish the hotkey global on
	// navigation (Rebuild carries no options.JS), assign to state.
	a.registerMainCloseHookOn(newWin)
	a.injectHotkeyBootstrap(newWin)
	a.mu.Lock()
	a.mainWindow = newWin
	a.sse = nil
	a.monitor = nil
	a.rebuilding = false
	a.mu.Unlock()

	// Now safe to close the old window — new one already exists.
	oldWin.Close()

	newWin.Show()
	// macOS: skip Focus — see openWebview comment. HardReset rebuilds the
	// WKWebView from scratch, so Focus would trigger the same first-key-
	// window ServicesController dispatch_sync deadlock as initial startup.
	if runtime.GOOS != "darwin" {
		newWin.Focus()
	}
	a.startListeners()
}

// showMainWindow restores+shows the current local main window. It re-reads
// a.mainWindow under the lock so it follows a HardResetMain replacement rather
// than acting on a stale captured handle (the old window is closed after reset).
func (a *App) showMainWindow() {
	a.mu.Lock()
	win := a.mainWindow
	a.mu.Unlock()
	if win == nil {
		return
	}
	win.Restore()
	win.Show()
	// macOS: skip Focus — see openWebview comment for the dispatch_sync deadlock.
	if runtime.GOOS != "darwin" {
		win.Focus()
	}
}

func (a *App) HardReset(w application.Window) {
	if w == nil {
		return
	}
	name := w.Name()
	if name == "main" {
		a.HardResetMain()
		return
	}
	// Remote connection windows are named "conn-<host:port>".
	if key, ok := strings.CutPrefix(name, "conn-"); ok {
		a.HardResetConnection(key)
	}
}

// scrubLegacyRelayPassword removes any plaintext relay password persisted by
// older niuniu-desktop builds in ~/.niuniu/desktop/config.json.  This is a
// best-effort one-shot upgrade: if the file is missing, malformed, or
// unwritable, we log and move on — a long-lived plaintext in an unreadable
// file is still less of a regression than refusing to boot over it.
func scrubLegacyRelayPassword() {
	cfg, err := config.Load()
	if err != nil {
		slog.Debug("legacy-relay scrub: load failed", "error", err)
		return
	}
	if !cfg.LegacyRelay.HasLegacyPassword() {
		return
	}
	slog.Info("legacy-relay scrub: clearing plaintext password from config.json; log in again via the browser UI")
	cfg.LegacyRelay = config.LegacyRelayConfig{}
	if err := config.Save(cfg); err != nil {
		slog.Warn("legacy-relay scrub: save failed", "error", err)
	}
}
