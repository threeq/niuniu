//go:build windows

package main

import (
	"log/slog"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// WebView2 Runtime download landing page (Microsoft official).
const webview2DownloadURL = "https://developer.microsoft.com/microsoft-edge/webview2/"

// webview2ClientGUID is the Microsoft-published Client ID for the Evergreen
// WebView2 Runtime. Detection contract is documented at:
// https://learn.microsoft.com/microsoft-edge/webview2/concepts/distribution#detect-if-a-suitable-webview2-runtime-is-already-installed
const webview2ClientGUID = "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

type registryProbe struct {
	root registry.Key
	path string
	desc string // for log diagnostics so we know which rung succeeded
}

// webview2RegistryProbes returns the (root, path) pairs Microsoft documents
// for runtime detection. Order matters: per-machine 64-bit → per-machine
// 32-bit view → per-user; first match wins.
func webview2RegistryProbes() []registryProbe {
	base := `Microsoft\EdgeUpdate\Clients\` + webview2ClientGUID
	return []registryProbe{
		{registry.LOCAL_MACHINE, `SOFTWARE\` + base, "HKLM-64"},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\` + base, "HKLM-WOW64"},
		{registry.CURRENT_USER, `Software\` + base, "HKCU"},
	}
}

// findWebView2Version walks the documented registry paths and returns the
// `pv` (version) string from the first one that has it. Empty string ⇒ not
// installed (or installed in a non-standard way we can't detect, in which
// case we'd rather fail safe and prompt the user than launch into a black
// hole).
func findWebView2Version() (version string, source string) {
	for _, p := range webview2RegistryProbes() {
		k, err := registry.OpenKey(p.root, p.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		pv, _, err := k.GetStringValue("pv")
		_ = k.Close()
		if err == nil && pv != "" {
			return pv, p.desc
		}
	}
	return "", ""
}

// Win32 MessageBox + ShellExecute bindings via syscall — we deliberately do
// NOT depend on a Wails dialog API here, because the whole reason this
// check exists is that Wails (and its webview2 binding) are about to fail
// on this exact machine. Native Win32 is the only safe surface.

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW     = user32.NewProc("MessageBoxW")
	shell32             = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteW   = shell32.NewProc("ShellExecuteW")
)

const (
	mbOKCancel      uintptr = 0x00000001
	mbIconError     uintptr = 0x00000010
	mbSetForeground uintptr = 0x00010000
	idOK                    = 1
)

func messageBoxW(title, text string, flags uintptr) int {
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	textPtr, _ := syscall.UTF16PtrFromString(text)
	ret, _, _ := procMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		flags,
	)
	return int(ret)
}

// shellOpenURL invokes ShellExecuteW("open", url, …) which routes through
// the user's default browser without flashing a console window (unlike
// `cmd /c start`). Best-effort — failure is logged but never fatal because
// we're already on the way out the door.
func shellOpenURL(url string) {
	verb, _ := syscall.UTF16PtrFromString("open")
	target, _ := syscall.UTF16PtrFromString(url)
	const swShowNormal uintptr = 1
	ret, _, err := procShellExecuteW.Call(
		0, // hwnd
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(target)),
		0, // params
		0, // dir
		swShowNormal,
	)
	// ShellExecute returns >32 on success.
	if ret <= 32 {
		slog.Warn("ShellExecuteW open URL failed", "url", url, "ret", ret, "err", err)
	}
}

// showWebView2MissingDialog blocks until the user dismisses a native modal.
// Returns true if the user clicked OK (and we attempted to open the download
// page in their browser); false on Cancel.
func showWebView2MissingDialog() bool {
	title := "需要安装 WebView2 Runtime"
	body := "牛牛桌面版 需要 Microsoft Edge WebView2 Runtime 才能显示界面。\n\n" +
		"您当前的 Windows 版本（如 LTSC、企业精简版、Windows Server 等）默认不预装该组件。\n\n" +
		"点击「确定」打开 Microsoft 官方下载页（请下载 \"Evergreen Standalone Installer\" x64），安装后重新启动 牛牛桌面版。"
	flags := mbOKCancel | mbIconError | mbSetForeground
	if messageBoxW(title, body, flags) == idOK {
		shellOpenURL(webview2DownloadURL)
		return true
	}
	return false
}
