//go:build darwin

package main

// aiembed_darwin.go is the macOS equivalent of aiembed_windows.go: it docks each
// AI-service webview inside the hub window's stage region so the AI Hub embeds
// its services instead of opening independent windows (issue ①). Flipping
// aiEmbedSupported to true routes ActivateAIService through the SAME bounded LRU
// pool + visibility machinery as Windows (aiwin.go) — no aiwin.go changes needed.
//
// Mechanism: Wails' native window is an NSWindow subclass; NativeWindow() returns
// its pointer. We make the service window a borderless CHILD window of the hub
// ([NSWindow addChildWindow:ordered:]) so it follows the hub's moves, stays above
// it, hides/closes with it, and stays out of Mission Control. Positioning maps the
// frontend's stage rect (physical px, top-left origin, hub-content-relative) into
// AppKit's point-based, bottom-left-origin screen coordinates.
//
// ⚠️ DRAFT — authored on a Windows host; the darwin/Cgo build cannot be compiled
// or run here. Verify + tune on macOS via `make build-personal-darwin` following
// docs/macos-ai-window-embed-design.md (coordinate flip / Retina scale / main-
// thread are the likely tuning points). All AppKit work is marshalled to the main
// queue inside C so the shared (non-main) callers in aiwin.go need no changes.

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// aiOwn makes child a borderless child window of hub (follows/above/closes-with
// hub, hidden from Mission Control). Idempotent.
static void aiOwn(void *hubPtr, void *childPtr) {
	if (!hubPtr || !childPtr) return;
	NSWindow *hub = (NSWindow *)hubPtr;
	NSWindow *child = (NSWindow *)childPtr;
	dispatch_async(dispatch_get_main_queue(), ^{
		[child setStyleMask:NSWindowStyleMaskBorderless];
		[child setCollectionBehavior:NSWindowCollectionBehaviorTransient |
			NSWindowCollectionBehaviorIgnoresCycle];
		if ([child parentWindow] != hub) {
			[hub addChildWindow:child ordered:NSWindowAbove];
		}
	});
}

// aiFrame positions child over hub's content area. x,y,w,h are physical px,
// top-left origin, relative to the hub content view (as the frontend reports).
static void aiFrame(void *hubPtr, void *childPtr, int x, int y, int w, int h) {
	if (!hubPtr || !childPtr) return;
	NSWindow *hub = (NSWindow *)hubPtr;
	NSWindow *child = (NSWindow *)childPtr;
	dispatch_async(dispatch_get_main_queue(), ^{
		CGFloat scale = hub.screen ? hub.screen.backingScaleFactor : hub.backingScaleFactor;
		if (scale <= 0) scale = 1.0;
		// Hub content-area rect in screen coords (bottom-left origin).
		NSView *content = hub.contentView;
		NSRect cvWin = [content convertRect:content.bounds toView:nil];
		NSRect cvScreen = [hub convertRectToScreen:cvWin];
		CGFloat px = (CGFloat)x / scale, py = (CGFloat)y / scale;
		CGFloat pw = (CGFloat)w / scale, ph = (CGFloat)h / scale;
		CGFloat originX = cvScreen.origin.x + px;
		// Flip Y: top of content = cvScreen.origin.y + height; window origin is
		// its bottom-left, so subtract py + ph.
		CGFloat originY = cvScreen.origin.y + cvScreen.size.height - py - ph;
		[child setFrame:NSMakeRect(originX, originY, pw, ph) display:YES];
	});
}

static void aiReveal(void *hubPtr, void *childPtr, int x, int y, int w, int h) {
	aiFrame(hubPtr, childPtr, x, y, w, h);
	if (!childPtr) return;
	NSWindow *child = (NSWindow *)childPtr;
	dispatch_async(dispatch_get_main_queue(), ^{
		[child orderFront:nil];
	});
}

static void aiStash(void *childPtr) {
	if (!childPtr) return;
	NSWindow *child = (NSWindow *)childPtr;
	dispatch_async(dispatch_get_main_queue(), ^{
		[child orderOut:nil];
	});
}
*/
import "C"

import (
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// aiEmbedSupported enables the pooled/embedded AI-service path (aiwin.go) on macOS.
const aiEmbedSupported = true

func nsWindowOf(w *application.WebviewWindow) unsafe.Pointer {
	if w == nil {
		return nil
	}
	return w.NativeWindow()
}

func aiEmbedOwn(hub, child *application.WebviewWindow) {
	C.aiOwn(nsWindowOf(hub), nsWindowOf(child))
}

func aiEmbedPosition(hub, child *application.WebviewWindow, x, y, w, h int) {
	C.aiFrame(nsWindowOf(hub), nsWindowOf(child), C.int(x), C.int(y), C.int(w), C.int(h))
}

func aiEmbedReveal(hub, child *application.WebviewWindow, x, y, w, h int) {
	C.aiReveal(nsWindowOf(hub), nsWindowOf(child), C.int(x), C.int(y), C.int(w), C.int(h))
}

func aiEmbedStash(child *application.WebviewWindow, w, h int) {
	C.aiStash(nsWindowOf(child))
}
