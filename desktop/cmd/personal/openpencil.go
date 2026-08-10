package main

import (
	"encoding/json"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// OpenPencil 官方仓库/发布页（开源、MIT、Tauri 桌面应用）。未安装时提示用户前往下载，
// 与 WebView2 缺失提示同一思路：给出说明 + 打开官方下载页。
const openPencilDownloadURL = "https://github.com/open-pencil/open-pencil/releases"

// openPencilResultEvent is the CustomEvent name Go dispatches back into the
// sender webview so the SPA can surface an inline hint (toast) about whether the
// launch succeeded or the app is missing.
const openPencilResultEvent = "niuniu:open-pencil-result"

// HandleOpenPencilMessage handles the "在 OpenPencil 中打开" bridge message the
// pencil-design scene's SPA entry posts via window.chrome.webview.postMessage:
//
//	{ "type": "niuniu-open-pencil", "filePath": "<optional .pen path>" }
//
// It launches the OpenPencil desktop app via os/exec. When the app can't be
// found it prompts the user to install it (mirroring the WebView2-missing
// prompt) and opens the official download page. Either way it posts a
// `niuniu:open-pencil-result` CustomEvent back into the sender window so the SPA
// can show an inline hint. The cheap substring guard keeps unrelated webview
// messages (e.g. the niuniu-runner-* bridge) from being parsed here.
func (a *App) HandleOpenPencilMessage(win application.Window, message string) {
	if !strings.Contains(message, "niuniu-open-pencil") {
		return
	}
	var msg struct {
		Type     string `json:"type"`
		FilePath string `json:"filePath"`
	}
	if json.Unmarshal([]byte(message), &msg) != nil || msg.Type != "niuniu-open-pencil" {
		return
	}
	slog.Info("open-pencil: launch requested", "file", msg.FilePath)
	launched, err := launchOpenPencil(msg.FilePath)
	if launched {
		slog.Info("open-pencil: app launched")
		a.postOpenPencilResult(win, true, "")
		return
	}
	slog.Warn("open-pencil: app not found — prompting install", "err", err)
	a.promptInstallOpenPencil(win)
	a.postOpenPencilResult(win, false, "not_installed")
}

// launchOpenPencil starts the OpenPencil desktop app (optionally opening
// filePath). Returns (true, nil) once the process is started, (false, nil) when
// no OpenPencil binary/app bundle could be located (→ install prompt), or
// (false, err) when a located binary failed to start. openPencilLaunchSpec is
// platform-specific (see openpencil_{windows,darwin,other}.go).
func launchOpenPencil(filePath string) (bool, error) {
	argv, ok := openPencilLaunchSpec(strings.TrimSpace(filePath))
	if !ok || len(argv) == 0 {
		return false, nil
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return false, err
	}
	// Detach: the app runs independently of the desktop shell. Reap in the
	// background so we neither block nor leak the process handle.
	go func() { _ = cmd.Wait() }()
	return true, nil
}

// postOpenPencilResult fires the result CustomEvent into the sender webview.
// No-op when the sender window is unknown.
func (a *App) postOpenPencilResult(win application.Window, ok bool, reason string) {
	if win == nil {
		return
	}
	payload, _ := json.Marshal(struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}{OK: ok, Reason: reason})
	win.ExecJS(`window.dispatchEvent(new CustomEvent('` + openPencilResultEvent +
		`',{detail:` + string(payload) + `}));`)
}

// promptInstallOpenPencil shows a native message dialog (cross-platform via
// Wails) that offers to open the OpenPencil download page — same intent as the
// WebView2 missing-runtime prompt. Best-effort; a browser-open failure is
// logged, never fatal.
func (a *App) promptInstallOpenPencil(win application.Window) {
	if a.wailsApp == nil {
		return
	}
	dlg := a.wailsApp.Dialog.Question().
		SetTitle("需要安装 OpenPencil / OpenPencil not installed").
		SetMessage("未检测到 OpenPencil 桌面应用。「画布设计」场景由 OpenPencil 应用实际执行画布读写，" +
			"需要先安装并运行它。\n\n" +
			"点击「打开下载页」前往 OpenPencil 官方发布页下载安装，安装并打开文档后重试「在 OpenPencil 中打开」。")
	if win != nil {
		dlg.AttachToWindow(win)
	}
	download := dlg.AddButton("打开下载页 / Download")
	download.OnClick(func() {
		if err := a.wailsApp.Browser.OpenURL(openPencilDownloadURL); err != nil {
			slog.Warn("open-pencil: open download URL failed", "url", openPencilDownloadURL, "err", err)
		}
	})
	download.SetAsDefault()
	dlg.AddButton("取消 / Cancel").SetAsCancel()
	dlg.Show()
}
