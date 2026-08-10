package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// fakePump faithfully models Wails v3's main-thread dispatch contract
// (application.dispatchOnMainThread + InvokeSync + runMainLoop): a call that
// dispatches onto the main thread and waits BLOCKS until the message loop is
// running and drains it. This is exactly the primitive that mainWindow.Show()/
// SetURL()/tray.SetMenu() funnel through — and exactly what deadlocked when the
// old code drove the boot sequence from ServiceStartup before the loop started.
type fakePump struct {
	mu      sync.Mutex
	running bool
	queue   []func()
	wake    chan struct{}
}

func newFakePump() *fakePump {
	return &fakePump{wake: make(chan struct{}, 1)}
}

// invokeSync mirrors application.InvokeSync: enqueue work for the main thread and
// block until the (running) loop executes it. If the loop never starts, this
// blocks forever — the production deadlock.
func (p *fakePump) invokeSync(fn func()) {
	done := make(chan struct{})
	p.mu.Lock()
	p.queue = append(p.queue, func() { fn(); close(done) })
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
	<-done
}

// run is the message loop (windowsApp.runMainLoop). Until this runs, invokeSync
// callers are parked.
func (p *fakePump) run(ctx context.Context) {
	p.mu.Lock()
	p.running = true
	p.mu.Unlock()
	for {
		p.mu.Lock()
		q := p.queue
		p.queue = nil
		p.mu.Unlock()
		for _, fn := range q {
			fn()
		}
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		}
	}
}

// TestStartupLifecycle_BootDeferredUntilPumpRunning is the startup integration
// test. It drives the App through the exact Wails Run() ordering and asserts the
// boot sequence's main-thread (window/tray) work only happens once the message
// loop is up — i.e. the "process alive but no window/no tray" deadlock cannot
// recur. Running the boot work in ServiceStartup (the old bug) would park on the
// dead pump and trip the 2s timeout below.
func TestStartupLifecycle_BootDeferredUntilPumpRunning(t *testing.T) {
	pump := newFakePump()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := &App{dataDir: t.TempDir()}

	bootStarted := make(chan struct{})
	bootDone := make(chan struct{})
	a.bootFn = func() {
		close(bootStarted)
		// boot touches the window/tray — modeled as a main-thread dispatch.
		pump.invokeSync(func() {})
		close(bootDone)
	}

	// --- Wails Run() phase 1: services start synchronously, BEFORE the loop. ---
	if err := a.ServiceStartup(ctx, application.ServiceOptions{}); err != nil {
		t.Fatalf("ServiceStartup error: %v", err)
	}
	// boot must NOT have been kicked off here; doing so would dispatch onto a
	// pump that is not running yet and hang the whole launch.
	select {
	case <-bootStarted:
		t.Fatal("boot ran during ServiceStartup — would deadlock against the not-yet-running message loop")
	case <-time.After(100 * time.Millisecond):
	}

	// --- Wails Run() phase 2: the platform message loop starts, then fires
	// ApplicationStarted, which is wired to StartBoot. ---
	go pump.run(ctx)
	a.StartBoot()

	select {
	case <-bootDone:
		// boot's main-thread dispatch completed: window/tray work was serviced.
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: boot's main-thread dispatch never completed after the loop started")
	}
}

// TestStartupLifecycle_SingleInstance is the end-to-end single-instance gate: the
// first launch acquires the boot-lock; a concurrent second launch against the
// same data dir is refused (so it exits in main() before building any window or
// tray), and the lock frees once the first releases it.
func TestStartupLifecycle_SingleInstance(t *testing.T) {
	dataDir := t.TempDir()

	first := &App{dataDir: dataDir}
	if !first.AcquireSingleInstance() {
		t.Fatal("first AcquireSingleInstance must succeed")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "personal.boot.lock")); err != nil {
		t.Fatalf("boot-lock file not created: %v", err)
	}

	second := &App{dataDir: dataDir}
	if second.AcquireSingleInstance() {
		t.Fatal("second AcquireSingleInstance must be refused while the first holds the lock")
	}

	// Release the first; a fresh launch must now be able to acquire it.
	if first.bootLock != nil {
		if err := first.bootLock.Release(); err != nil {
			t.Fatalf("release boot-lock: %v", err)
		}
	}
	third := &App{dataDir: dataDir}
	if !third.AcquireSingleInstance() {
		t.Fatal("AcquireSingleInstance must succeed after the prior holder released")
	}
	if third.bootLock != nil {
		_ = third.bootLock.Release()
	}
}
