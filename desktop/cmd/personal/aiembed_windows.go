//go:build windows

package main

import (
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// aiembed_windows.go docks each AI-service webview over the hub window's "stage"
// region so the whole AI hub looks like a single window with an in-window switcher.
//
// Design note (why OWNED top-level window, not a WS_CHILD):
// An earlier version reparented the service window into the hub as a Win32
// WS_CHILD. That renders correctly, but a child window has ABNORMAL browser
// semantics — Chromium can treat it as occluded/background and its document never
// reports as a normal focused/visible top-level context. Cloudflare Turnstile
// distrusts that and re-challenges "after a while". The Tauri-based AnyChat app
// (same WebView2 underneath) avoids this by keeping each service as a SEPARATE
// top-level window docked over the content — a real browser window.
//
// So we make the service window an OWNED top-level window: it stays a normal
// top-level WebView2 window (normal visibility/focus → Cloudflare happy), but by
// setting its OWNER to the hub (GWLP_HWNDPARENT) it always floats above the hub,
// shows no taskbar button, and closes/minimises with it. We position it in SCREEN
// coordinates over the stage and re-sync on hub move/resize.
//
// All functions here touch HWNDs and MUST run on the main UI thread — callers wrap
// them in application.InvokeSync.

const aiEmbedSupported = true

var (
	// user32 is declared in webview2_check_windows.go (same package); reuse it.
	procGetWindowLong  = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLong  = user32.NewProc("SetWindowLongPtrW")
	procSetWindowPos   = user32.NewProc("SetWindowPos")
	procShowWindow     = user32.NewProc("ShowWindow")
	procSetFocus       = user32.NewProc("SetFocus")
	procClientToScreen = user32.NewProc("ClientToScreen")

	// GWL_STYLE / GWL_EXSTYLE / GWLP_HWNDPARENT are negative indices; kept as vars so
	// the negative→uintptr conversion happens at runtime (a negative constant→uintptr
	// is a compile error).
	gwlStyle       = int32(-16)
	gwlExStyle     = int32(-20)
	gwlpHwndParent = int32(-8) // sets the OWNER window (misleading name; not a child)
)

const (
	wsChild       = 0x40000000
	wsPopup       = 0x80000000
	wsCaption     = 0x00C00000
	wsThickframe  = 0x00040000
	wsMinimizebox = 0x00020000
	wsMaximizebox = 0x00010000
	wsSysmenu     = 0x00080000
	wsBorder      = 0x00800000
	wsDlgframe    = 0x00400000
	// Frame/caption bits stripped so the service window renders flush over the
	// stage with no title bar. WS_POPUP (top-level, no frame) is kept/added;
	// WS_CHILD is explicitly removed (we are an owned top-level, not a child).
	wsFrameBits = wsCaption | wsThickframe | wsMinimizebox | wsMaximizebox | wsSysmenu | wsBorder | wsDlgframe

	wsExAppWindow  = 0x00040000
	wsExToolWindow = 0x00000080 // no taskbar button

	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020

	hwndTop = 0

	swHide           = 0
	swShowNoActivate = 4
)

// offscreenPos moves a STILL-SHOWN window off every monitor. We stash the service
// webview here (instead of SW_HIDE) while the loading splash / an overlay is up: a
// hidden (SW_HIDE) WebView2 suspends compositing and comes back BLANK when shown (the
// intermittent white page); kept shown but off-screen — and un-throttled by
// --disable-backgrounding-occluded-windows — it keeps rendering, so revealing it is
// just a move and the page is already painted. Kept as a var (not const) so the
// negative→uintptr conversion happens at runtime (a negative constant→uintptr is a
// compile error), mirroring gwlStyle etc. above.
var offscreenPos = int32(-32000)

// hwndOf extracts the HWND (as uintptr) from a Wails window; 0 if unavailable.
func hwndOf(w *application.WebviewWindow) uintptr {
	if w == nil {
		return 0
	}
	return uintptr(w.NativeWindow())
}

// clientOrigin returns the screen coordinates of a window's client-area top-left.
func clientOrigin(hwnd uintptr) (int32, int32) {
	var pt struct{ X, Y int32 }
	procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
	return pt.X, pt.Y
}

// aiEmbedOwn makes child a frameless OWNED top-level window of hub (floats above
// the hub, no taskbar button, no frame) WITHOUT making it a child. Does not move
// or show it — callers reveal via aiEmbedReveal / stash via aiEmbedStash.
func aiEmbedOwn(hub, child *application.WebviewWindow) {
	ph, ch := hwndOf(hub), hwndOf(child)
	if ph == 0 || ch == 0 {
		return
	}
	style, _, _ := procGetWindowLong.Call(ch, uintptr(gwlStyle))
	style = (style &^ uintptr(wsFrameBits|wsChild)) | uintptr(wsPopup)
	procSetWindowLong.Call(ch, uintptr(gwlStyle), style)

	ex, _, _ := procGetWindowLong.Call(ch, uintptr(gwlExStyle))
	ex = (ex &^ uintptr(wsExAppWindow)) | uintptr(wsExToolWindow)
	procSetWindowLong.Call(ch, uintptr(gwlExStyle), ex)

	// Set the owner (GWLP_HWNDPARENT on a top-level window sets its OWNER, not a
	// child relationship): the service window now always sits above the hub.
	procSetWindowLong.Call(ch, uintptr(gwlpHwndParent), ph)

	// SWP_FRAMECHANGED so the style strip takes effect; no move/size/show/z here.
	procSetWindowPos.Call(ch, 0, 0, 0, 0, 0,
		uintptr(swpNoZOrder|swpNoActivate|swpFrameChanged|swpNoSize|swpNoMove))
}

// aiEmbedPosition moves/resizes the docked service window to sit exactly over the
// hub's stage. x,y,w,h are the stage rect in physical px, CLIENT-relative to the
// hub; we convert to screen coordinates via the hub's client origin. Does not
// change visibility or z-order.
func aiEmbedPosition(hub, child *application.WebviewWindow, x, y, w, h int) {
	ch := hwndOf(child)
	if ch == 0 {
		return
	}
	var ox, oy int32
	if ph := hwndOf(hub); ph != 0 {
		ox, oy = clientOrigin(ph)
	}
	procSetWindowPos.Call(ch, 0,
		uintptr(ox+int32(x)), uintptr(oy+int32(y)), uintptr(int32(w)), uintptr(int32(h)),
		uintptr(swpNoZOrder|swpNoActivate))
}

// aiEmbedReveal brings the docked service window onto the hub's stage: it positions
// the window over the stage (x,y,w,h are physical px, CLIENT-relative to the hub),
// ensures it is shown, raises it above the hub and gives it input focus so its
// document reports focused/visible — Cloudflare Turnstile keeps re-challenging a
// webview that never gets focus. Because the window was only STASHED off-screen
// (never SW_HIDE'd), WebView2 kept compositing while it loaded, so it appears here
// already-painted — this is what eliminates the intermittent blank page on switch.
//
// While stashed off-screen the window's client size may be stale: if the hub was
// resized/maximised meanwhile, WebView2 can defer/skip that resize and then render the
// web content at the OLD (smaller) size, leaving black gaps around it. So we resize in
// TWO steps while the window is on-screen and visible — first to h+1, then to the real
// h — guaranteeing a real WM_SIZE with a non-zero delta that forces WebView2 to
// re-layout the page at the current stage size.
func aiEmbedReveal(hub, child *application.WebviewWindow, x, y, w, h int) {
	ch := hwndOf(child)
	if ch == 0 {
		return
	}
	var ox, oy int32
	if ph := hwndOf(hub); ph != 0 {
		ox, oy = clientOrigin(ph)
	}
	procShowWindow.Call(ch, uintptr(swShowNoActivate))
	px, py := uintptr(ox+int32(x)), uintptr(oy+int32(y))
	// Nudge (h+1) then settle (h) — forces WebView2 to recompute its size on re-show.
	procSetWindowPos.Call(ch, uintptr(hwndTop), px, py, uintptr(int32(w)), uintptr(int32(h)+1),
		uintptr(swpNoActivate)) // move on-stage AND raise above the hub
	procSetWindowPos.Call(ch, uintptr(hwndTop), px, py, uintptr(int32(w)), uintptr(int32(h)),
		uintptr(swpNoActivate))
	procSetFocus.Call(ch)
}

// aiEmbedStash hides the service window from view WITHOUT SW_HIDE: it moves the window
// fully off every monitor but keeps it SHOWN, so WebView2 keeps rendering the page in
// the background (un-throttled by --disable-backgrounding-occluded-windows) instead of
// suspending and blanking. Used while the loading splash / an HTML overlay is up. The
// window keeps the stage size so revealing it needs no reflow; it is never focused.
func aiEmbedStash(child *application.WebviewWindow, w, h int) {
	ch := hwndOf(child)
	if ch == 0 {
		return
	}
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	// Move off-screen FIRST (harmless while a freshly-created window is still hidden),
	// THEN ensure it is shown — so the window never flashes at its default on-screen
	// spot before we've parked it off-screen.
	procSetWindowPos.Call(ch, 0,
		uintptr(offscreenPos), uintptr(offscreenPos), uintptr(int32(w)), uintptr(int32(h)),
		uintptr(swpNoZOrder|swpNoActivate))
	procShowWindow.Call(ch, uintptr(swShowNoActivate))
}
