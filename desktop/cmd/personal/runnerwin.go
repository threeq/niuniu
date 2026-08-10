package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"

	"github.com/niuniu-dev/niuniu-desktop/internal/runnerstore"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// runnerwin.go implements the global "本地 Runner 管理" window (#526·子C): the
// desktop-owned surface that aggregates every workspace's local execution Runner
// across ALL remote connections. The window is a hidden-until-opened Wails
// webview loading the embedded /runners.html frontend, which calls the App
// bindings below. Registry state lives in a.runners (internal/runnerstore).
//
// 方案 A: the real Runner (process, reverse channel, tool injection) is stubbed,
// so Start/Stop/Delete here mutate registry status + persist; 子 B drives the
// same records from the live connection. Delete = "解绑" (drop the binding
// record);清白名单/停长连 are backend concerns 子 B wires to the same id.

// RunnerView is a registry Runner plus live desktop context for the manager UI.
// The embedded Runner's snake_case fields are flattened into the JSON object, so
// the frontend sees every Runner field alongside conn_active.
type RunnerView struct {
	runnerstore.Runner
	// ConnActive is true when a connection window for this Runner's server is
	// currently open (the reverse channel could be live). Derived, not persisted.
	ConnActive bool `json:"conn_active"`
}

// connActiveKeys snapshots the set of currently-open remote connection keys.
func (a *App) connActiveKeys() map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	keys := make(map[string]bool, len(a.connWindows))
	for key := range a.connWindows {
		keys[key] = true
	}
	return keys
}

// ListLocalRunners returns all registered runners enriched with live
// connection-active state. Bound for the management frontend.
func (a *App) ListLocalRunners() []RunnerView {
	if a.runners == nil {
		return nil
	}
	runners := a.runners.List()
	active := a.connActiveKeys()
	out := make([]RunnerView, len(runners))
	for i, r := range runners {
		out[i] = RunnerView{Runner: r, ConnActive: active[r.ConnectionKey]}
	}
	return out
}

// RegisterLocalRunner upserts a Runner binding and returns its id. This is the
// entry point 子 B (or a desktop-shell bridge) calls when a workspace saves its
// local-executor config, so the per-workspace bottom bar (#526·子A) and this
// global manager share one source of truth.
func (a *App) RegisterLocalRunner(connKey, connName, workspaceID, workspaceName, localDir string) string {
	if a.runners == nil {
		return ""
	}
	r, err := a.runners.Upsert(runnerstore.Runner{
		ConnectionKey:  connKey,
		ConnectionName: connName,
		WorkspaceID:    workspaceID,
		WorkspaceName:  workspaceName,
		LocalDir:       localDir,
		Status:         runnerstore.StatusActive,
	})
	if err != nil {
		slog.Warn("register local runner: persist failed", "error", err)
	}
	return r.ID
}

// StartLocalRunner brings a Runner online: it builds the security gateway +
// reverse-channel client and connects to the bound node (#526·子D). If the
// connection's auth token has not been harvested from its webview yet, the
// runner enters "connecting" and comes online automatically once the token
// arrives (SetLocalRunnerToken). Returns false for an unknown id.
func (a *App) StartLocalRunner(id string) bool {
	if a.runners == nil || a.runnerMgr == nil {
		return false
	}
	r, ok := a.runners.Get(id)
	if !ok {
		return false
	}
	err := a.runnerMgr.start(r)
	switch {
	case err == nil:
		// Connected (or connecting) — the client's OnStatus drives the registry
		// status from here; just note the intent.
		_, _ = a.runners.AppendLog(id, runnerstore.LogEntry{Level: "system", Text: "启动本地执行器 / starting local runner"})
	case errors.Is(err, errAwaitingToken):
		_, _ = a.runners.SetStatus(id, runnerstore.StatusConnecting)
		_, _ = a.runners.AppendLog(id, runnerstore.LogEntry{Level: "system", Text: "等待连接鉴权（请打开该连接窗口并登录）/ awaiting auth — open & sign in to the connection window"})
	default:
		_, _ = a.runners.SetStatus(id, runnerstore.StatusError)
		_, _ = a.runners.AppendLog(id, runnerstore.LogEntry{Level: "system", Text: "启动失败 / start failed: " + err.Error()})
	}
	return true
}

// StopLocalRunner tears down a Runner's live reverse channel and marks it
// stopped (停长连; the binding record stays — use DeleteLocalRunner to 解绑).
// Returns false for an unknown id.
func (a *App) StopLocalRunner(id string) bool {
	if a.runnerMgr != nil {
		a.runnerMgr.stop(id)
	}
	return a.setRunnerStatus(id, runnerstore.StatusStopped, "runner stopped")
}

// SetLocalRunnerToken records the bearer token harvested from a remote
// connection's webview and (re)starts any non-stopped Runners bound to that
// connection that are waiting on it (#526·子D reverse-registration). Bound so
// the RawMessageHandler bridge can call it; safe to call repeatedly.
func (a *App) SetLocalRunnerToken(connKey, token string) {
	if a.runnerMgr == nil || a.runners == nil {
		return
	}
	for _, id := range a.runnerMgr.setToken(connKey, token) {
		r, ok := a.runners.Get(id)
		if !ok {
			continue
		}
		if err := a.runnerMgr.start(r); err != nil && !errors.Is(err, errAwaitingToken) {
			_, _ = a.runners.SetStatus(id, runnerstore.StatusError)
			_, _ = a.runners.AppendLog(id, runnerstore.LogEntry{Level: "system", Text: "自动启动失败 / auto-start failed: " + err.Error()})
		}
	}
}

// HandleRawWebviewMessage parses a raw postMessage from any webview (the Wails
// RawMessageHandler bridge). It handles the signals the remote-connection
// injection posts (see connwin.go):
//   - niuniu-runner-ping     — diagnostic: proves the JS->Go bridge delivers.
//   - niuniu-runner-token    — the harvested JWT → SetLocalRunnerToken.
//   - niuniu-runner-config   — a workspace's saved local-executor config →
//     RegisterLocalRunner + StartLocalRunner ("保存即连接" #526·子E).
//   - niuniu-runner-unbind   — that config disappeared (解绑) → Stop + Delete.
//   - niuniu-runner-pick-dir — SPA asked for a native folder picker → open it
//     and post the chosen path back into the sender's connection window.
//
// The cheap substring guard keeps unrelated webview messages from being parsed.
func (a *App) HandleRawWebviewMessage(message string) {
	if !strings.Contains(message, "niuniu-runner-") && !strings.Contains(message, "niuniu-hotkey-") {
		return
	}
	var probe struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(message), &probe) != nil {
		return
	}
	// Diagnostic: every recognized bridge message is logged so a failing
	// end-to-end path leaves direct evidence in personal-*.log.
	slog.Info("local-runner: raw webview message", "type", probe.Type)
	switch probe.Type {
	case "niuniu-runner-loaded":
		// Diagnostic: records which document the bridge script executed in.
		var msg struct {
			ConnKey string `json:"connKey"`
			Href    string `json:"href"`
		}
		_ = json.Unmarshal([]byte(message), &msg)
		slog.Info("local-runner: bridge script ran in page", "conn", msg.ConnKey, "href", msg.Href)
	case "niuniu-runner-ping":
		// No-op beyond the log above — its arrival is the signal.
	case "niuniu-runner-token":
		var msg struct {
			ConnKey string `json:"connKey"`
			Token   string `json:"token"`
			Origin  string `json:"origin"`
		}
		if json.Unmarshal([]byte(message), &msg) != nil || msg.ConnKey == "" || msg.Token == "" {
			return
		}
		a.setRunnerBaseURL(msg.ConnKey, msg.Origin)
		slog.Info("local-runner: token harvested", "conn", msg.ConnKey)
		a.SetLocalRunnerToken(msg.ConnKey, msg.Token)
	case "niuniu-runner-config":
		var msg struct {
			ConnKey       string `json:"connKey"`
			WorkspaceID   string `json:"workspaceId"`
			WorkspaceName string `json:"workspaceName"`
			LocalDir      string `json:"localDir"`
			Origin        string `json:"origin"`
		}
		if json.Unmarshal([]byte(message), &msg) != nil {
			return
		}
		a.setRunnerBaseURL(msg.ConnKey, msg.Origin)
		slog.Info("local-runner: config harvested", "conn", msg.ConnKey, "ws", msg.WorkspaceID, "dir", msg.LocalDir)
		a.applyRunnerConfig(msg.ConnKey, msg.WorkspaceID, msg.WorkspaceName, msg.LocalDir)
	case "niuniu-runner-unbind":
		var msg struct {
			ConnKey     string `json:"connKey"`
			WorkspaceID string `json:"workspaceId"`
		}
		if json.Unmarshal([]byte(message), &msg) != nil {
			return
		}
		a.unbindLocalRunner(msg.ConnKey, msg.WorkspaceID)
	case "niuniu-runner-pick-dir":
		var msg struct {
			ConnKey string `json:"connKey"`
		}
		if json.Unmarshal([]byte(message), &msg) != nil || msg.ConnKey == "" {
			return
		}
		a.pickLocalRunnerDir(msg.ConnKey)
	case "niuniu-hotkey-query":
		// Settings UI asked for a target's hotkey state → broadcast it back.
		// Target defaults to "ai" when omitted (older SPA builds).
		var msg struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal([]byte(message), &msg)
		a.broadcastHotkeyConfig(normalizeHotkeyTarget(msg.Target), true, "")
	case "niuniu-hotkey-set":
		var msg struct {
			Target      string `json:"target"`
			Enabled     bool   `json:"enabled"`
			Accelerator string `json:"accelerator"`
		}
		if json.Unmarshal([]byte(message), &msg) != nil {
			return
		}
		a.setHotkey(normalizeHotkeyTarget(msg.Target), msg.Enabled, msg.Accelerator)
	}
}

// pickLocalRunnerDir opens a native folder picker (issue: 配置弹框需要目录选择器)
// attached to the sender's connection window, then posts the chosen absolute
// path back into that window as a `niuniu:runner-dir-picked` CustomEvent the SPA
// config dialog listens for. No-op (silent) when the user cancels.
func (a *App) pickLocalRunnerDir(connKey string) {
	a.mu.Lock()
	cw := a.connWindows[connKey]
	a.mu.Unlock()
	if cw == nil {
		slog.Warn("local-runner: pick-dir for unknown connection", "conn", connKey)
		return
	}
	win := cw.window
	dlg := a.wailsApp.Dialog.OpenFile().
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true).
		SetTitle("选择本地工作目录 / Choose local working directory")
	if win != nil {
		dlg.AttachToWindow(win)
	}
	dir, err := dlg.PromptForSingleSelection()
	if err != nil || dir == "" {
		slog.Debug("local-runner: pick-dir cancelled or failed", "conn", connKey, "err", err)
		return
	}
	slog.Info("local-runner: pick-dir chosen", "conn", connKey, "dir", dir)
	payload, _ := json.Marshal(dir)
	if win != nil {
		win.ExecJS(`window.dispatchEvent(new CustomEvent('niuniu:runner-dir-picked',{detail:{path:` + string(payload) + `}}));`)
	}
}

// applyRunnerConfig upserts the binding harvested from a workspace's saved
// local-executor config and brings it online — the last bridge of "保存即连接"
// (#526·子E). connName is derived from the open connection window (the SPA
// payload carries only the directory config); workspaceName may be empty.
//
// Idempotent by construction: RegisterLocalRunner is an Upsert on
// (connKey, workspaceID), and Start is skipped when a live reverse channel
// already exists — so a re-harvest (page reload, unchanged config) neither
// duplicates the binding nor the connection. When the auth token has not been
// harvested yet, StartLocalRunner leaves the runner "connecting" and the
// existing SetLocalRunnerToken path auto-starts it once the token arrives.
func (a *App) applyRunnerConfig(connKey, workspaceID, workspaceName, localDir string) {
	if connKey == "" || workspaceID == "" || localDir == "" {
		return
	}
	id := a.RegisterLocalRunner(connKey, a.connNameFor(connKey), workspaceID, workspaceName, localDir)
	if id == "" {
		return
	}
	if a.runnerMgr != nil && a.runnerMgr.running(id) {
		return // already live — nothing to (re)start
	}
	a.StartLocalRunner(id)
}

// unbindLocalRunner stops and removes the binding for (connKey, workspaceID) when
// its config disappears from the workspace bottom bar (解绑). No-op when no
// matching binding exists.
func (a *App) unbindLocalRunner(connKey, workspaceID string) {
	if a.runners == nil || connKey == "" || workspaceID == "" {
		return
	}
	for _, r := range a.runners.List() {
		if r.ConnectionKey == connKey && r.WorkspaceID == workspaceID {
			a.StopLocalRunner(r.ID)
			a.DeleteLocalRunner(r.ID)
			return
		}
	}
}

// connNameFor returns the display name of the open remote connection for connKey,
// or "" when the connection isn't currently tracked.
func (a *App) connNameFor(connKey string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cw, ok := a.connWindows[connKey]; ok {
		return cw.conn.Name
	}
	return ""
}

// reharvestConnToken asks a connection's webview to mint a fresh auth token and
// re-post it — called when that connection's reverse channel is rejected with
// 401 (the harvested access token expired). The webview is the sole owner of the
// session's rotating (single-use) refresh token, so only it can safely refresh;
// __niuniuRunnerRefresh__ (defined by the SPA in main.tsx, desktop-only) runs
// refreshAccessToken() and posts the new token back through the usual bridge.
// No-op when the connection window isn't open (nothing to refresh against).
func (a *App) reharvestConnToken(connKey string) {
	a.mu.Lock()
	cw := a.connWindows[connKey]
	a.mu.Unlock()
	if cw == nil || cw.window == nil {
		return
	}
	slog.Info("local-runner: reverse channel 401 — asking webview to refresh token", "conn", connKey)
	cw.window.ExecJS(`window.__niuniuRunnerRefresh__ && window.__niuniuRunnerRefresh__();`)
}

// setRunnerBaseURL overrides the recorded base URL for a connection with the
// origin the SPA is actually served from (authoritative scheme+host). The
// connection may have been added as http:// while the server is really https://,
// which would make the reverse channel dial ws:// and fail the TLS handshake
// (observed reconnect loop). No-op on empty origin or when runnerMgr is unset.
func (a *App) setRunnerBaseURL(connKey, origin string) {
	if a.runnerMgr == nil || connKey == "" || origin == "" {
		return
	}
	a.runnerMgr.setBaseURL(connKey, origin)
}

func (a *App) setRunnerStatus(id, status, note string) bool {
	if a.runners == nil {
		return false
	}
	ok, err := a.runners.SetStatus(id, status)
	if err != nil {
		slog.Warn("set runner status: persist failed", "id", id, "error", err)
	}
	if ok {
		_, _ = a.runners.AppendLog(id, runnerstore.LogEntry{Level: "system", Text: note})
	}
	return ok
}

// DeleteLocalRunner removes a Runner binding entirely (解绑). Returns false for
// an unknown id. 清白名单 / 停长连 are backend concerns 子 B attaches to the same id.
func (a *App) DeleteLocalRunner(id string) bool {
	if a.runners == nil {
		return false
	}
	ok, err := a.runners.Remove(id)
	if err != nil {
		slog.Warn("delete local runner: persist failed", "id", id, "error", err)
	}
	return ok
}

// GetLocalRunnerLogs returns a Runner's buffered log tail (command + stdout/
// stderr + system lines).
func (a *App) GetLocalRunnerLogs(id string) []runnerstore.LogEntry {
	if a.runners == nil {
		return nil
	}
	return a.runners.Logs(id)
}

// OpenLocalRunnerDir opens a Runner's bound local directory in the OS file
// manager. Returns false for an unknown id or an empty directory.
func (a *App) OpenLocalRunnerDir(id string) bool {
	if a.runners == nil {
		return false
	}
	r, ok := a.runners.Get(id)
	if !ok || r.LocalDir == "" {
		return false
	}
	if err := openInFileManager(r.LocalDir); err != nil {
		slog.Warn("open runner dir", "id", id, "dir", r.LocalDir, "error", err)
		return false
	}
	return true
}

// GetUILang returns the OS-resolved desktop UI language ("zh"/"en") so the
// management frontend can localize without re-detecting the locale.
func (a *App) GetUILang() string {
	return a.lang
}

// OpenRunnersWindow shows the global runner-management window. Bound for the
// tray / SPA, mirroring OpenPicker.
func (a *App) OpenRunnersWindow() {
	a.mu.Lock()
	win := a.runnersWindow
	a.mu.Unlock()
	if win == nil {
		return
	}
	a.markAuxWindowOpened("runners")
	win.Restore()
	win.Show()
	if runtime.GOOS != "darwin" {
		win.Focus()
	}
}

// openInFileManager reveals dir in the OS file manager. Split out (and var-ized)
// so tests can assert the command without spawning a real process.
var openInFileManager = func(dir string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	return cmd.Start()
}

// newRunnersWindow builds the hidden management window. Close = hide to tray (the
// registry persists), like the picker/AI hub.
func newRunnersWindow(app *application.App, title string) *application.WebviewWindow {
	return app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "runners",
		Title:  title,
		Width:  920,
		Height: 680,
		URL:    "/runners.html",
		Hidden: true,
		Linux:  application.LinuxWindow{Icon: appIconPNG},
	})
}
