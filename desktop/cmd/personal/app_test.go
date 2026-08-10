package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// TestServiceStartup_DoesNotBootSynchronously guards the Windows "process alive
// but no window/no tray" deadlock: Wails v3 runs ServiceStartup synchronously
// before the message pump and before the window/tray are created, so it must NOT
// perform the heavy boot work (probe/spawn/openWebview/RebuildTray) there. The
// boot sequence belongs to StartBoot, wired to events.Common.ApplicationStarted.
func TestServiceStartup_DoesNotBootSynchronously(t *testing.T) {
	var bootCalls int32
	a := &App{dataDir: t.TempDir()}
	a.bootFn = func() { atomic.AddInt32(&bootCalls, 1) }

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "v")

	done := make(chan error, 1)
	go func() { done <- a.ServiceStartup(ctx, application.ServiceOptions{}) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServiceStartup returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServiceStartup did not return promptly — it must not block on boot work")
	}

	if a.ctx == nil {
		t.Error("ServiceStartup must capture the context")
	}
	// Give any (incorrectly) spawned goroutine a chance to run before asserting.
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&bootCalls); n != 0 {
		t.Errorf("boot must not run during ServiceStartup; ran %d time(s)", n)
	}
}

// TestStartBoot_RunsBootExactlyOnce verifies the deferred boot fires once even
// if ApplicationStarted is delivered more than once.
func TestStartBoot_RunsBootExactlyOnce(t *testing.T) {
	var bootCalls int32
	ran := make(chan struct{}, 4)
	a := &App{dataDir: t.TempDir()}
	a.bootFn = func() {
		atomic.AddInt32(&bootCalls, 1)
		ran <- struct{}{}
	}

	for i := 0; i < 3; i++ {
		a.StartBoot()
	}

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("StartBoot never ran the boot function")
	}
	// Allow time for any extra (incorrect) invocations to surface.
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&bootCalls); n != 1 {
		t.Errorf("boot must run exactly once across repeated StartBoot calls; ran %d time(s)", n)
	}
}

func TestHardResetMain_MutualExclusionWithRestartServer(t *testing.T) {
	a := &App{
		serverAddr: "127.0.0.1:3000",
	}
	a.restarting = true

	// HardResetMain should be a no-op when restarting.
	a.HardResetMain()

	if a.rebuilding {
		t.Error("rebuilding should not be set when restarting")
	}
}

func TestRestartServer_MutualExclusionWithHardReset(t *testing.T) {
	a := &App{
		serverAddr: "127.0.0.1:3000",
	}
	a.rebuilding = true

	// RestartServer should be a no-op when rebuilding — returns nil
	// without clearing the rebuilding flag.
	err := a.RestartServer()
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if !a.rebuilding {
		t.Error("rebuilding should still be true after RestartServer no-op")
	}
}

func TestHardResetMain_IdempotentUnderConcurrency(t *testing.T) {
	a := &App{
		serverAddr: "127.0.0.1:3000",
	}
	a.rebuilding = true

	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			a.HardResetMain()
			done <- struct{}{}
		}()
	}

	timeout := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-timeout:
			t.Fatal("deadlock detected: concurrent HardResetMain calls")
		}
	}
	// rebuilding stays true because the first caller holds it and the
	// second is a no-op. The important thing is no deadlock.
}
