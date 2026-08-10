package main

import (
	"embed"
	"encoding/base64"
	"flag"
	"log"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/niuniu-dev/niuniu-desktop/internal/i18n"
	"github.com/niuniu-dev/niuniu-desktop/internal/notify"
	"github.com/niuniu-dev/niuniu-desktop/internal/updater"
	"github.com/niuniu-dev/niuniu/go-shared/releasecheck"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// userHomeErrString is filled at startup so the fingerprint line can report
// whether resolveLogPath() had to fall back from ~/.niuniu/. Empty = no error.
var userHomeErrString string

// appIconPNG is used for the Wails-rendered runtime icon (window title bar,
// taskbar, dock). The Windows .exe file icon is embedded separately via the
// goversioninfo-generated *.syso resource produced during _personal-prepare.
//
//go:embed appicon.png
var appIconPNG []byte

// appIconDataURI is the high-res product logo (appicon.png) as a base64 data URI,
// for inlining into the connecting-splash data: URL (which has a null origin and
// can't reference /icon.svg). Computed once at startup from the embedded bytes.
var appIconDataURI = "data:image/png;base64," + base64.StdEncoding.EncodeToString(appIconPNG)

// assets embeds the connection-manager (picker) frontend, absorbed from the
// retired cmd/connect. The picker window loads it at the asset-server root
// ("/"); the local main window and remote connection windows load absolute
// http URLs (the embedded server / remote nodes), which bypass this handler.
//
//go:embed all:frontend
var assets embed.FS

func main() {
	logPath, logCloser := setupPersonalLog()
	defer logCloser.Close()
	defer recoverAndLog()

	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	slog.Info("niuniu-desktop starting",
		"version", personalVersion,
		"pid", os.Getpid(),
		"exe", exe,
		"cwd", cwd,
		"log_path", logPath,
		"goos", runtime.GOOS,
		"goarch", runtime.GOARCH,
		"args", os.Args,
		"user_home_err", userHomeErrString,
		"env_userprofile", os.Getenv("USERPROFILE"))

	// Pre-flight: WebView2 Runtime is required by Wails on Windows. LTSC /
	// Enterprise / Server SKUs ship without it, in which case Wails crashes
	// silently inside webview2.Embed() — which is exactly the failure mode
	// captured in personal-2026-05-11.log on the first remote machine.
	// Detect explicitly and surface a native modal so the user knows what
	// to install instead of staring at a missing window.
	wv2Ver, wv2Src := findWebView2Version()
	if runtime.GOOS == "windows" && wv2Ver == "" {
		slog.Error("WebView2 Runtime not detected; showing dialog and exiting",
			"download_url", "https://developer.microsoft.com/microsoft-edge/webview2/")
		showWebView2MissingDialog()
		return
	}
	slog.Info("WebView2 Runtime detected", "version", wv2Ver, "source", wv2Src)

	var flags runtimeFlags
	fs := flag.NewFlagSet("niuniu-desktop", flag.ExitOnError)
	fs.StringVar(&flags.DevURL, "dev-url", "",
		"skip probe/spawn and open webview on this URL (e.g. http://localhost:5173)")
	fs.BoolVar(&flags.AutoStart, "autostart", false,
		"marks a login-triggered launch (shows the main window by default)")
	fs.BoolVar(&flags.StartMinimized, "minimized", false,
		"start minimized to the tray instead of showing the main window")
	_ = fs.Parse(os.Args[1:])

	// Hand our own executable path to the embedded server so it can register a
	// launch-at-login item pointing back at this binary (GET/PUT /api/autostart).
	// The child server inherits this via os.Environ() at spawn time.
	if exe != "" {
		_ = os.Setenv("NIUNIU_PERSONAL_EXE", exe)
	}

	dataDir := defaultDataDir()
	slog.Info("data dir resolved", "data_dir", dataDir)
	app := NewApp(flags, dataDir)

	// Single-instance gate BEFORE building the Wails app: a second launch must
	// exit without ever flashing a window or tray icon. dev-url mode is exempt
	// (it never owned the boot-lock and runs against an external dev server).
	if flags.DevURL == "" {
		if !app.AcquireSingleInstance() {
			return
		}
	}

	// OS-resolved UI language, cached once on the App (see internal/i18n).
	// Used for the process app name and every native window title.
	lang := app.lang

	// Pin the WebView2 user-data folder to a STABLE path so localStorage (e.g. a
	// remote login's "remember me", per-origin browser storage) survives app
	// restarts AND version upgrades. Wails' default is %APPDATA%\<binaryName>.exe,
	// and our binary name carries a version timestamp — so the folder, and all
	// its storage, would change on every new build. Windows-only option; macOS/
	// Linux use their platforms' persistent web data stores by default.
	webviewDataDir := filepath.Join(dataDir, "webview2")
	_ = os.MkdirAll(webviewDataDir, 0o755)

	// Reduce false-positive bot challenges (Cloudflare Turnstile "请验证您是真人")
	// on embedded AI sites (Claude / Perplexity …). Two causes are addressed:
	//   1. --disable-blink-features=AutomationControlled: don't advertise as automated.
	//   2. The AI-service webview is reparented into the hub as a Win32 CHILD window;
	//      Chromium can treat such a window as OCCLUDED/backgrounded and throttle its
	//      renderer, so Turnstile's periodic keep-alive stops running and the site
	//      re-challenges "after a while". These flags keep the renderer fully live
	//      regardless of window occlusion/visibility.
	//   3. --user-agent: present a clean, mainstream Chrome UA instead of WebView2's
	//      default (which carries an "Edg/…" token and a WebView2 fingerprint that
	//      Cloudflare distrusts). The Tauri-based AnyChat app relies on the same
	//      trick (it sets a real Chrome UA per platform) — Wails alpha.74 has no
	//      per-window UA API, so we set it globally via the Chromium flag. Applies to
	//      the local SPA / remote windows too, which don't care about UA. Bump the
	//      Chrome major periodically so it doesn't read as an outdated browser.
	// WebView2 reads WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS from the environment when
	// it creates its browser process, so this MUST be set before the Wails app runs.
	// Windows-only mechanism; harmless elsewhere. An existing user value is preserved.
	const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"
	const wv2Args = "--disable-blink-features=AutomationControlled " +
		"--disable-backgrounding-occluded-windows " +
		"--disable-renderer-backgrounding " +
		"--disable-background-timer-throttling " +
		"--user-agent=\"" + chromeUA + "\""
	if prev := os.Getenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"); prev != "" {
		if !strings.Contains(prev, "AutomationControlled") {
			_ = os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", prev+" "+wv2Args)
		}
	} else {
		_ = os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", wv2Args)
	}

	notifier := notifications.New()
	wailsApp := application.New(application.Options{
		// Options.Name drives the macOS Dock name / Windows taskbar group:
		// the brand, localized. macOS can't per-window-distinguish the Dock
		// entry once connect is merged in, so it shows the brand uniformly.
		Name:     i18n.AppName(lang),
		Icon:     appIconPNG,
		Logger:   slog.Default(),
		LogLevel: slog.LevelDebug,
		Windows: application.WindowsOptions{
			WebviewUserDataPath: webviewDataDir,
		},
		// Serve the embedded picker frontend. Absolute http URLs (local server,
		// remote nodes) bypass this; only the picker's "/" resolves here.
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Services: []application.Service{
			application.NewService(app),
			application.NewService(notifier),
		},
		// #526·子D: raw postMessage bridge. The remote-connection webview injects
		// a harvester that posts its auth token here (origin-independent, unlike
		// the Wails runtime call which targets the remote page's own origin).
		RawMessageHandler: func(win application.Window, message string, _ *application.OriginInfo) {
			app.HandleRawWebviewMessage(message)
			// "在 OpenPencil 中打开" bridge (pencil-design scene). Needs the sender
			// window to post the launch result back; its own substring guard keeps
			// it from touching the niuniu-runner-* messages above.
			app.HandleOpenPencilMessage(win, message)
		},
		KeyBindings: map[string]func(application.Window){
			"ctrl+shift+r": func(w application.Window) { app.HardReset(w) },
			"cmd+shift+r":  func(w application.Window) { app.HardReset(w) },
			"f11":          func(w application.Window) { w.ToggleFullscreen() },
		},
	})

	// Start visible with an inline loading page; openWebview later replaces
	// the URL once the embedded server is ready. A hidden-then-Show() pattern
	// is unreliable on some Wails v3 builds — showing immediately with
	// placeholder content gives the user feedback during the 1-5s boot.
	loadingHTML := loadingSplashURL(lang)

	mainWin := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "main",
		Title:  i18n.LocalTitle(lang),
		Width:  1440,
		Height: 900,
		URL:    loadingHTML,
		// Publish the hotkey config to the SPA up front (reliable document-created
		// injection) so 通用设置 renders the configured combos without depending on
		// the flaky async query→ExecJS echo. See hotkeywin.go hotkeyBootstrapJS.
		JS: app.hotkeyBootstrapJS(),
		// Only when the user opted into "start minimized" do we keep the window
		// in the tray: create it hidden so no loading splash flashes on boot.
		// A plain autostart launch shows the window like a normal launch.
		Hidden: flags.StartMinimized,
		// Linux window manager reads the per-window iconified icon from here.
		// On Windows the runtime icon is sourced from the .exe resources via
		// goversioninfo; on macOS it comes from the .app bundle's Info.plist.
		Linux: application.LinuxWindow{Icon: appIconPNG},
	})
	// Re-publish the hotkey global on every navigation (the main window is created
	// on a splash URL then SetURL'd to the local SPA; options.JS alone does not
	// survive that hand-off). See hotkeywin.go injectHotkeyBootstrap.
	app.injectHotkeyBootstrap(mainWin)

	tray := wailsApp.SystemTray.New()
	// Use the Niuniu app icon on every platform — the macOS menu bar included —
	// so the tray shows our bull instead of the Wails default placeholder and
	// matches the Windows/Linux tray. SetTemplateIcon would force a monochrome
	// silhouette; we want the full-color icon, same as the dock/taskbar.
	tray.SetDarkModeIcon(appIconPNG)
	tray.SetIcon(appIconPNG)
	tray.SetTooltip(i18n.AppName(lang))

	// Picker (connection-manager) window — absorbed from cmd/connect. Created
	// hidden; opened on demand from the tray / SPA. It loads the embedded
	// frontend at "/", and never preempts first-launch (the local main window).
	picker := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "picker",
		Title:  i18n.ManageTitle(lang),
		Width:  1280,
		Height: 800,
		URL:    "/",
		Hidden: true,
		// Settings can also be reached from the picker window; publish the same
		// hotkey bootstrap global here (see hotkeywin.go hotkeyBootstrapJS).
		JS: app.hotkeyBootstrapJS(),
		Linux:  application.LinuxWindow{Icon: appIconPNG},
	})
	app.injectHotkeyBootstrap(picker)
	// Close picker = hide to tray (not quit / not destroy). RegisterHook runs the
	// cancel synchronously before the default destroy listener.
	picker.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		event.Cancel()
		picker.Hide()
	})
	// Keep the picker hidden on first launch until opened on demand (macOS quirk;
	// see guardAuxWindowFirstLaunch).
	app.guardAuxWindowFirstLaunch(picker, "picker")

	// AI-aggregation hub window ("AI 直达") — the left-rail service switcher.
	// Created hidden; opened on demand via the global hotkey (Ctrl/Cmd+Shift+A),
	// the tray menu, or the bound OpenAIWindow() from the SPA. Like the picker it
	// loads the embedded frontend (/ai.html) and never preempts first launch — the
	// local SPA main window always owns the boot (方案 A: 绝不抢首启).
	aiHub := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "ai-hub",
		Title:  i18n.AITitle(lang),
		Width:  980,
		Height: 720,
		URL:    "/ai.html",
		Hidden: true,
		// The docked AI-service window is a separate owned window repositioned on
		// WindowDidMove. Wails debounces that event by 50ms by default, so the
		// service only snapped into place AFTER the drag stopped. Drop the debounce
		// to ~1ms so WindowDidMove fires continuously and the service tracks the hub
		// during the drag.
		Windows: application.WindowsWindow{WindowDidMoveDebounceMS: 1},
		Linux:   application.LinuxWindow{Icon: appIconPNG},
	})
	// Close hub = hide to tray, not quit. Also mark the hub hidden so the docked
	// service window is hidden with it (deterministic, no reliance on WM events).
	aiHub.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		event.Cancel()
		aiHub.Hide()
		app.setHubVisible(false)
	})
	// The docked AI-service window is an OWNED top-level window, not a child, so it
	// does NOT move with the hub automatically — re-dock it on hub move/resize, and
	// hide it when the hub is dismissed. (Re-showing on hub show is driven by the
	// hub frontend's visibilitychange handler so it respects open modals.)
	// Re-dock the service window when the hub is dragged/resized. These fire as
	// events.Windows.* on Windows (NOT events.Common.* — Wails matches hooks by
	// exact event ID with no platform→common bridge for move/resize). Show/hide is
	// NOT driven by window events (WM_SHOWWINDOW proved unreliable to observe);
	// it's handled deterministically via setHubVisible in OpenAIWindow /
	// ToggleAIWindow / the close hook above.
	aiHub.RegisterHook(events.Windows.WindowDidMove, func(_ *application.WindowEvent) {
		app.repositionActiveAIService()
	})
	aiHub.RegisterHook(events.Windows.WindowDidResize, func(_ *application.WindowEvent) {
		app.repositionActiveAIService()
	})
	// macOS: the embedded service is an NSWindow child (aiembed_darwin.go); keep it
	// pinned to the hub's stage as the hub moves/resizes. Wrong-platform hooks never fire.
	aiHub.RegisterHook(events.Mac.WindowDidMove, func(_ *application.WindowEvent) {
		app.repositionActiveAIService()
	})
	aiHub.RegisterHook(events.Mac.WindowDidResize, func(_ *application.WindowEvent) {
		app.repositionActiveAIService()
	})
	// Keep the AI hub hidden on first launch until opened on demand (macOS quirk;
	// see guardAuxWindowFirstLaunch).
	app.guardAuxWindowFirstLaunch(aiHub, "ai-hub")

	// Global "本地 Runner 管理" window (#526·子C). Created hidden; opened on demand
	// from the tray / SPA. Loads the embedded /runners.html manager and, like the
	// picker/AI hub, never preempts first launch. Close = hide to tray (the
	// desktop-owned registry persists across opens).
	runnersWin := newRunnersWindow(wailsApp, i18n.RunnersTitle(lang))
	runnersWin.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		event.Cancel()
		runnersWin.Hide()
	})
	// Keep the runners window hidden on first launch until opened on demand (macOS
	// quirk; see guardAuxWindowFirstLaunch).
	app.guardAuxWindowFirstLaunch(runnersWin, "runners")

	n := notify.New(notifier)
	app.SetWails(wailsApp, mainWin, tray, n)
	app.pickerWindow = picker
	app.aiHubWindow = aiHub
	app.runnersWindow = runnersWin

	// Route notification clicks: connection notifications (conn_lost_/restored_/
	// max_<key>) focus the matching remote window; everything else falls back to
	// the local main window.
	notifier.OnNotificationResponse(func(result notifications.NotificationResult) {
		if result.Response.ActionIdentifier != "OPEN" && result.Response.ActionIdentifier != notifications.DefaultActionIdentifier {
			return
		}
		id := result.Response.ID
		var connKey string
		for _, prefix := range []string{"conn_lost_", "conn_restored_", "conn_max_"} {
			if key, ok := strings.CutPrefix(id, prefix); ok {
				connKey = key
				break
			}
		}
		if connKey != "" {
			app.mu.Lock()
			cw, ok := app.connWindows[connKey]
			app.mu.Unlock()
			if ok {
				cw.window.Restore()
				cw.window.Show()
				if runtime.GOOS != "darwin" {
					cw.window.Focus()
				}
				return
			}
		}
		// Fall back to the current local main window (re-read inside the helper
		// so a HardResetMain replacement is honored).
		app.showMainWindow()
	})

	// Defer the heavy boot sequence (probe → spawn server → open webview →
	// build tray) until the Wails event loop is actually running. Doing it in
	// ServiceStartup ran it synchronously BEFORE the message pump and before the
	// window/tray were created, which on Windows intermittently deadlocked the
	// whole launch (process alive, no window, no tray). ApplicationStarted fires
	// once the pump is up, so the window/tray InvokeSync calls inside boot are
	// serviced instead of blocking forever.
	wailsApp.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
		app.StartBoot()
	})

	// Close main window = hide to tray, NOT quit.
	app.registerMainCloseHookOn(mainWin)

	// Tray left-click → show the current local main window (re-read inside the
	// helper so it survives a HardResetMain window replacement; macOS Focus is
	// skipped there to avoid the WebKit::ServicesController dispatch_sync deadlock).
	tray.OnClick(func() {
		app.showMainWindow()
	})

	app.RebuildTray()

	// Release distribution lives at https://github.com/threeq/niuniu-public/releases,
	// but we check for new versions via the official website changelog
	// (www.niu6ai.com/changelog) — api.github.com returns HTTP 403 from mainland
	// China, where our users are. The page is built from the same GitHub releases
	// and is Aliyun-hosted, so it's always reachable. We only check on boot + every
	// 6h from the SPA.
	app.upd = updater.NewWebsite(personalVersion, releasecheck.DefaultBaseURL)

	if err := wailsApp.Run(); err != nil {
		slog.Error("wailsApp.Run returned error", "error", err)
		log.Fatal(err)
	}
	slog.Info("niuniu-desktop exited cleanly")
}

// loadingSplashURL is the local main window's boot splash, shown while the
// embedded server spawns and before openWebview navigates to it. Localized via
// the OS-resolved lang (i18n) so it matches the native window chrome.
func loadingSplashURL(lang string) string {
	return spinnerSplashURL(i18n.AppName(lang), i18n.LocalBootHeading(lang), i18n.T(lang, i18n.KeyInitLocalService))
}

// connectingSplashURL is the initial document for a remote connection window. It
// shows a feature-highlight slideshow while connecting, then navigates to the
// node itself once it is reachable (so the window never sits blank-white during
// the connect). The page owns the navigation — it polls /api/health and only
// then `location.replace`s to the node, after a short minimum so the slideshow
// is actually seen; a hard cap proceeds regardless so a down/unreachable host
// still surfaces its real error page. name is the connection's display name,
// target is the node base URL.
//
// The body is percent-encoded for the same url.Parse reason as spinnerSplashURL.
func connectingSplashURL(lang, name, target string) string {
	connecting := "正在连接"
	if lang == "en" {
		connecting = "Connecting"
	}
	body := connectingSplashTemplate
	body = strings.ReplaceAll(body, "__LANG__", lang)
	body = strings.ReplaceAll(body, "__BRAND__", htmlEscape(i18n.Brand(lang)))
	body = strings.ReplaceAll(body, "__LOGO__", appIconDataURI)
	body = strings.ReplaceAll(body, "__CONNECTING__", connecting)
	body = strings.ReplaceAll(body, "__NAME__", htmlEscape(name))
	body = strings.ReplaceAll(body, "__TARGET__", target)
	return "data:text/html;charset=utf-8," + url.PathEscape(body)
}

// connectingSplashTemplate is the slideshow loading page. Placeholders are
// substituted in connectingSplashURL. No Go fmt is used (the CSS contains '%').
const connectingSplashTemplate = `<!doctype html><html lang="__LANG__"><head><meta charset="utf-8"><title>__BRAND__</title>` +
	`<style>` +
	`:root{--bg:#0b1220;--fg:#e6ecf5;--mut:#8a97ad;--ac:#3b82f6;--ac2:#22d3ee}` +
	`*{box-sizing:border-box}html,body{height:100%}` +
	`body{margin:0;background:radial-gradient(1100px 560px at 50% -8%,#16223b 0,#0b1220 62%);color:var(--fg);` +
	`font-family:'Noto Sans SC',system-ui,-apple-system,sans-serif;display:flex;flex-direction:column;` +
	`align-items:center;justify-content:center;gap:30px;overflow:hidden;user-select:none}` +
	`.brand{display:flex;align-items:center;gap:12px}` +
	`.logo{width:54px;height:54px;border-radius:15px;object-fit:contain;` +
	`box-shadow:0 8px 30px rgba(59,130,246,.35)}` +
	`.bname{font-size:21px;font-weight:600;letter-spacing:.5px}` +
	`.stage{height:148px;width:min(560px,84vw);display:flex;align-items:center;justify-content:center}` +
	`.slide{text-align:center;transition:opacity .35s ease,transform .35s ease;opacity:0;transform:translateY(8px)}` +
	`.slide.on{opacity:1;transform:none}` +
	`.ico{font-size:40px;margin-bottom:14px}.ttl{font-size:19px;font-weight:600;margin-bottom:8px}` +
	`.dsc{font-size:14px;line-height:1.6;color:var(--mut);max-width:460px;margin:0 auto}` +
	`.dots{display:flex;gap:8px}.dot{width:7px;height:7px;border-radius:50%;background:#2a3650;transition:all .3s}` +
	`.dot.on{background:var(--ac);width:20px;border-radius:4px}` +
	`.foot{position:fixed;left:0;right:0;bottom:32px;display:flex;flex-direction:column;align-items:center;gap:13px}` +
	`.bar{position:relative;width:min(560px,84vw);height:3px;border-radius:3px;background:#1b2740;overflow:hidden}` +
	`.bar i{position:absolute;left:0;top:0;height:100%;width:38%;border-radius:3px;` +
	`background:linear-gradient(90deg,transparent,var(--ac),var(--ac2),transparent);animation:run 1.3s linear infinite}` +
	`@keyframes run{0%{transform:translateX(-120%)}100%{transform:translateX(330%)}}` +
	`.status{font-size:13px;color:var(--mut)}` +
	`@media (prefers-reduced-motion:reduce){.bar i{animation-duration:3s}.slide{transition:none}}` +
	`</style></head><body>` +
	`<div class="brand"><img class="logo" src="__LOGO__" alt=""><div class="bname">__BRAND__</div></div>` +
	`<div class="stage"><div id="slide" class="slide on"></div></div>` +
	`<div id="dots" class="dots"></div>` +
	`<div class="foot"><div class="bar"><i></i></div><div class="status">__CONNECTING__ __NAME__…</div></div>` +
	`<script>` +
	`var LANG="__LANG__",TARGET="__TARGET__";` +
	`var SL=LANG==="en"?[` +
	`["🧠","Parallel agents","Drive many Claude Code sessions at once — across projects and repos."],` +
	`["📋","Kanban orchestration","Manage issues and workflows on a board that auto-advances stages."],` +
	`["🗂️","Isolated workspaces","Each task gets its own git worktree, so you can run them side by side."],` +
	`["📊","Full observability","Messages, costs, code diffs and pipeline runs, all at a glance."],` +
	`["🔒","Local-first, connected","Your data stays on your machine — reach LAN, cloud or remote nodes."]` +
	`]:[` +
	`["🧠","多会话并行","同时驱动多个 Claude Code 智能体，跨项目、跨仓库高效协作。"],` +
	`["📋","看板式编排","用看板管理 issue 与工作流，自动推进开发阶段。"],` +
	`["🗂️","隔离工作空间","每个任务独立 git worktree，互不干扰，随时并行。"],` +
	`["📊","全程可观测","消息、成本、代码 diff、流水线运行一目了然。"],` +
	`["🔒","本地优先 · 多端互联","数据存在本机，安全可控；并可连接 LAN / 云 / 远端节点。"]` +
	`];` +
	`var slide=document.getElementById("slide"),dots=document.getElementById("dots"),i=0;` +
	`function dh(){var h="";for(var k=0;k<SL.length;k++){h+='<span class="dot'+(k===i?" on":"")+'"></span>'}dots.innerHTML=h}` +
	`function pa(){var s=SL[i];slide.innerHTML='<div class="ico">'+s[0]+'</div><div class="ttl">'+s[1]+'</div><div class="dsc">'+s[2]+'</div>';slide.classList.add("on");dh()}` +
	`pa();setInterval(function(){slide.classList.remove("on");setTimeout(function(){i=(i+1)%SL.length;pa()},350)},3600);` +
	`var t0=Date.now(),gone=false,MIN=1800,CAP=12000;` +
	`function go(){if(gone)return;gone=true;try{location.replace(TARGET)}catch(e){location.href=TARGET}}` +
	`function probe(){if(gone)return;fetch(TARGET+"/api/health",{mode:"no-cors",cache:"no-store"}).then(function(){setTimeout(go,Math.max(0,MIN-(Date.now()-t0)))}).catch(function(){setTimeout(probe,700)})}` +
	`setTimeout(go,CAP);probe();` +
	`</script></body></html>`

// spinnerSplashURL builds a centered spinner splash (title/heading/sub) as a
// data: URL. The body is percent-encoded because Wails routes every window URL
// through url.Parse (assetserver.GetStartURL); a raw '%' (e.g. border-radius:50%)
// or '#' (hex colors) makes url.Parse FATAL ("invalid URL escape"), which on
// Windows killed the process before the window could show.
func spinnerSplashURL(title, heading, sub string) string {
	body := "<!doctype html><html><head><meta charset='utf-8'><title>" + htmlEscape(title) + "</title>" +
		"<style>body{font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#111;color:#ccc}" +
		".s{text-align:center}h1{font-weight:normal;margin:0 0 12px 0;font-size:24px}p{font-size:14px;color:#888}" +
		"@keyframes spin{to{transform:rotate(360deg)}}" +
		".spin{width:32px;height:32px;border:3px solid #333;border-top-color:#0af;border-radius:50%;animation:spin 1s linear infinite;margin:0 auto 16px}" +
		"</style></head><body><div class='s'><div class='spin'></div><h1>" + htmlEscape(heading) + "</h1><p>" + htmlEscape(sub) + "</p></div></body></html>"
	return "data:text/html;charset=utf-8," + url.PathEscape(body)
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		userHomeErrString = err.Error()
		return ".niuniu"
	}
	return filepath.Join(home, ".niuniu")
}
