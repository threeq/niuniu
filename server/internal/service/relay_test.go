package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// These tests cover the parts of RelayService that don't need the OS keychain
// or a real relay URL: the state-machine guards, lifecycle idempotency, the
// generation-guard against stale background goroutines, and per-scope
// isolation.  They intentionally do NOT hit credstore (which requires a real
// keychain on the host CI box) or the network.

// redirectScopesStore points scopesStorePath at a temp file for the test.
func redirectScopesStore(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	orig := scopesStorePath
	scopesStorePath = func() (string, error) {
		return filepath.Join(dir, "scopes.json"), nil
	}
	t.Cleanup(func() {
		scopesStorePath = orig
		_ = os.RemoveAll(dir)
	})
}

// userStateForTest forces creation of a scope's userState without going
// through any network path.
func (s *RelayService) userStateForTest(scope string) *userState {
	return s.getOrCreateUser(scope)
}

func TestRelayService_Stop_NoopWhenNoTunnel(t *testing.T) {
	t.Parallel()
	svc := NewRelayService()
	done := make(chan struct{})
	go func() { svc.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop on an empty service blocked")
	}
}

func TestRelayService_StartPairing_RejectsWhenInProgress(t *testing.T) {
	t.Parallel()
	svc := NewRelayService()
	u := svc.userStateForTest("alice")
	u.pairMu.Lock()
	u.pairState = PairingState{Phase: "waiting_claim"}
	u.pairGen = 1
	u.pairMu.Unlock()

	if _, err := svc.StartPairing("alice"); err == nil {
		t.Fatal("expected already-in-progress error")
	}
}

func TestRelayService_StartPairing_AllowedWhenIdleOrFailed(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"idle", "failed", "confirmed"} {
		svc := NewRelayService()
		u := svc.userStateForTest("alice")
		u.pairMu.Lock()
		u.pairState = PairingState{Phase: phase}
		u.pairMu.Unlock()

		st, err := svc.StartPairing("alice")
		if err != nil {
			t.Fatalf("phase=%s: unexpected error: %v", phase, err)
		}
		if st.Phase != "starting" {
			t.Fatalf("phase=%s: expected starting after start, got %q", phase, st.Phase)
		}
	}
}

func TestRelayService_RejectPairing_BumpsGeneration(t *testing.T) {
	t.Parallel()
	svc := NewRelayService()
	u := svc.userStateForTest("alice")
	u.pairMu.Lock()
	u.pairGen = 42
	u.pairSessionID = ""
	u.pairState = PairingState{Phase: "waiting_claim"}
	u.pairMu.Unlock()

	if err := svc.RejectPairing("alice"); err != nil {
		t.Fatalf("RejectPairing: %v", err)
	}

	u.pairMu.Lock()
	gen := u.pairGen
	phase := u.pairState.Phase
	u.pairMu.Unlock()
	if gen != 43 {
		t.Errorf("expected generation bump to 43, got %d", gen)
	}
	if phase != "failed" {
		t.Errorf("expected phase=failed after reject, got %q", phase)
	}
}

func TestRelayService_PairingStatus_Concurrent(t *testing.T) {
	t.Parallel()
	svc := NewRelayService()
	u := svc.userStateForTest("alice")
	u.pairMu.Lock()
	u.pairState = PairingState{Phase: "waiting_claim", SessionID: "s1"}
	u.pairMu.Unlock()

	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = svc.PairingStatus("alice")
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent PairingStatus calls stalled")
	}
}

func TestRelayService_SetLocalBaseURL_TrimsTrailingSlash(t *testing.T) {
	t.Parallel()
	svc := NewRelayService()
	svc.SetLocalBaseURL("http://127.0.0.1:1234/")
	if svc.localBaseURL != "http://127.0.0.1:1234" {
		t.Errorf("trailing slash not trimmed: %q", svc.localBaseURL)
	}
}

func TestRelayService_Start_NoopWithoutScopes(t *testing.T) {
	redirectScopesStore(t)
	svc := NewRelayService()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { svc.Start(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start without persisted scopes blocked")
	}
}

func TestRelayService_ScopeIsolation_PairingSlots(t *testing.T) {
	t.Parallel()
	svc := NewRelayService()

	// Put alice into waiting_claim; bob must still be allowed to start.
	ua := svc.userStateForTest("alice")
	ua.pairMu.Lock()
	ua.pairState = PairingState{Phase: "waiting_claim"}
	ua.pairMu.Unlock()

	if _, err := svc.StartPairing("alice"); err == nil {
		t.Fatal("alice should be blocked: already in waiting_claim")
	}
	// bob has its own slot; starting should succeed.
	st, err := svc.StartPairing("bob")
	if err != nil {
		t.Fatalf("bob StartPairing: %v", err)
	}
	if st.Phase != "starting" {
		t.Fatalf("bob state got %q, want starting", st.Phase)
	}
}

func TestRelayService_PersistedScopes_AddRemove(t *testing.T) {
	redirectScopesStore(t)

	if err := addPersistedScope("alice"); err != nil {
		t.Fatalf("add alice: %v", err)
	}
	if err := addPersistedScope("bob"); err != nil {
		t.Fatalf("add bob: %v", err)
	}
	if err := addPersistedScope("alice"); err != nil {
		t.Fatalf("re-add alice (idempotent): %v", err)
	}

	scopes, err := loadPersistedScopes()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(scopes) != 2 {
		t.Fatalf("expected 2 scopes, got %v", scopes)
	}
	if err := removePersistedScope("alice"); err != nil {
		t.Fatalf("remove alice: %v", err)
	}
	scopes, err = loadPersistedScopes()
	if err != nil {
		t.Fatalf("load after remove: %v", err)
	}
	if len(scopes) != 1 || scopes[0] != "bob" {
		t.Fatalf("expected only bob, got %v", scopes)
	}
}

// retryWithBackoff is the linchpin of the runTunnel resilience fix: the prior
// code returned on the first rc.RefreshAccessToken failure and stranded the
// tunnel until niuniu-server restart. The retry loop must (1) keep calling fn
// until it succeeds, (2) honor ctx cancellation promptly so logout/Stop
// doesn't hang.
func TestRetryWithBackoff_RetriesUntilSuccess(t *testing.T) {
	defer shrinkRetryBackoff(t)()

	calls := 0
	err := retryWithBackoff(context.Background(), func() error {
		calls++
		if calls < 3 {
			return fmt.Errorf("attempt %d transient", calls)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil after success, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestRetryWithBackoff_ReturnsCtxErrOnCancel(t *testing.T) {
	defer shrinkRetryBackoff(t)()

	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- retryWithBackoff(ctx, func() error {
			calls++
			return fmt.Errorf("perma-fail")
		})
	}()

	// Let the loop accumulate a couple of failed attempts, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("retryWithBackoff did not return after cancel")
	}
	if calls < 1 {
		t.Fatalf("expected at least one fn() call before cancel, got %d", calls)
	}
}

// shrinkRetryBackoff overrides the production 1s..30s backoff with sub-ms
// values so the tests run in milliseconds rather than tens of seconds.
// Returns a restore function meant to be `defer`d.
func shrinkRetryBackoff(t *testing.T) func() {
	t.Helper()
	origInitial, origCap := retryWithBackoffInitial, retryWithBackoffCap
	retryWithBackoffInitial = time.Millisecond
	retryWithBackoffCap = 4 * time.Millisecond
	return func() {
		retryWithBackoffInitial = origInitial
		retryWithBackoffCap = origCap
	}
}
