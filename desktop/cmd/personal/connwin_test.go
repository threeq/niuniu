package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu-desktop/internal/config"
)

// TestCombinedBridgeJS_SignalsRuntimeReady guards the fix for "Mac 本地执行器选择
// 文件夹路径不回填": a remote-connection window is runtime-less (data:URL splash +
// remote SPA), so WebviewWindow.runtimeLoaded never flips and every Go->SPA
// win.ExecJS (the folder-picker reply) is silently queued and dropped. The
// injected bridge must post "wails:runtime:ready" over the raw native bridge to
// flip runtimeLoaded, and must do so AFTER the bridge helpers are defined but
// BEFORE the harvesters/loaded/ping posts that follow.
func TestCombinedBridgeJS_SignalsRuntimeReady(t *testing.T) {
	js := combinedBridgeJS("self.example.com:443")

	if !strings.Contains(js, "wails:runtime:ready") {
		t.Fatal("combinedBridgeJS must post wails:runtime:ready so Go->SPA ExecJS is delivered on runtime-less remote windows")
	}

	defIdx := strings.Index(js, "window.__nnRunnerPost=function")
	readyIdx := strings.Index(js, "wails:runtime:ready")
	loadedIdx := strings.Index(js, "niuniu-runner-loaded")
	if defIdx < 0 || readyIdx < 0 || loadedIdx < 0 {
		t.Fatalf("missing expected snippets: def=%d ready=%d loaded=%d", defIdx, readyIdx, loadedIdx)
	}
	if !(defIdx < readyIdx && readyIdx < loadedIdx) {
		t.Errorf("runtime-ready must come after the bridge def and before other posts: def=%d ready=%d loaded=%d", defIdx, readyIdx, loadedIdx)
	}
}

// TestRunnerRuntimeReadyJS_GuardedAndBridgeGated verifies the ready signal is
// idempotent (posts once per document) and no-ops until the native bridge exists.
func TestRunnerRuntimeReadyJS_GuardedAndBridgeGated(t *testing.T) {
	js := runnerRuntimeReadyJS()
	for _, want := range []string{"__nnRuntimeReadySignaled", "__nnRunnerBridge", "wails:runtime:ready"} {
		if !strings.Contains(js, want) {
			t.Errorf("runnerRuntimeReadyJS missing %q", want)
		}
	}
}

// These cover the remote-connection machinery absorbed from cmd/connect. The
// LOCAL window's HardResetMain/RestartServer mutual exclusion lives in
// app_test.go; here we exercise the per-key remote rebuild guard.

func newConnTestApp() *App {
	return &App{
		cfg:            &config.DesktopConfig{},
		connWindows:    make(map[string]*ConnWindow),
		connRebuilding: make(map[string]bool),
	}
}

func TestHardResetConnection_MutualExclusion(t *testing.T) {
	a := newConnTestApp()
	key := "127.0.0.1:3000"
	a.connRebuilding[key] = true

	// Should be a no-op when a rebuild is already in progress for this key.
	a.HardResetConnection(key)

	if _, ok := a.connRebuilding[key]; !ok {
		t.Error("connRebuilding[key] should still be set")
	}
}

func TestHardResetConnection_UnknownKey_NoPanic(t *testing.T) {
	a := newConnTestApp()
	// Should not panic on an unknown key.
	a.HardResetConnection("nonexistent:9999")
}

func TestConnect_BlockedDuringRebuild(t *testing.T) {
	a := newConnTestApp()
	key := "127.0.0.1:3000"
	a.connRebuilding[key] = true

	// The rebuild guard returns before any window creation (no wailsApp needed).
	conn := &config.Connection{Host: "127.0.0.1", Port: 3000}
	if a.Connect(conn, true) {
		t.Error("Connect should return false when a rebuild is in progress for the key")
	}
}

func TestHardResetConnection_IdempotentUnderConcurrency(t *testing.T) {
	a := newConnTestApp()
	key := "127.0.0.1:3000"

	done := make(chan struct{}, 3)
	for i := 0; i < 3; i++ {
		go func() {
			a.HardResetConnection(key)
			done <- struct{}{}
		}()
	}

	timeout := time.After(2 * time.Second)
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-timeout:
			t.Fatal("deadlock detected: concurrent HardResetConnection calls")
		}
	}
}

// TestConfigAccess_ConcurrentSnapshotAndMutate hammers AddConnection (mutate +
// SaveTo under cfgMu) against GetConnections (snapshot under cfgMu) from many
// goroutines. With nil tray/wailsApp, RebuildTray is a no-op, so this isolates
// the cfg slice. Under `go test -race` (CI) it guards against the append-vs-range
// data race that the dedicated cfgMu was added to close.
func TestConfigAccess_ConcurrentSnapshotAndMutate(t *testing.T) {
	a := newConnTestApp()
	a.cfgPath = filepath.Join(t.TempDir(), "config.json")

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(k int) {
			defer wg.Done()
			a.AddConnection(fmt.Sprintf("c%d", k), "127.0.0.1", 3000+k)
		}(i)
		go func() {
			defer wg.Done()
			_ = a.GetConnections()
		}()
	}
	wg.Wait()

	if got := len(a.GetConnections()); got != n {
		t.Errorf("expected %d connections after concurrent adds, got %d", n, got)
	}
}

// TestConnect_NotWiredReturnsFalse guards Connect's defensive nil-guard: with no
// Wails app wired (a bare test App), a fresh Connect must fail gracefully rather
// than panic on window creation. (Connect no longer pre-flights a health check;
// the window opens immediately and the Monitor reports connectivity.)
func TestConnect_NotWiredReturnsFalse(t *testing.T) {
	a := newConnTestApp() // wailsApp is nil
	conn := &config.Connection{Host: "127.0.0.1", Port: 3999}
	if a.Connect(conn, true) {
		t.Error("Connect should return false when the app is not wired (wailsApp nil)")
	}
}
