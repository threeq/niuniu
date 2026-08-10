package bundle

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeServerBin builds testdata/fake-server/main.go once per test run
// and returns the path. Handles GOEXE suffix for Windows.
func fakeServerBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-server")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/fake-server")
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "build fake-server: %s", out)
	return bin
}

func TestSpawnHandshakeSuccess(t *testing.T) {
	bin := fakeServerBin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h, err := spawnFromBinary(ctx, bin, Spec{Timeout: 3 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { h.Shutdown() })

	require.NotEmpty(t, h.Addr)
	resp, err := http.Get("http://" + h.Addr + "/api/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
}

func TestSpawnEarlyCrash(t *testing.T) {
	bin := fakeServerBin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h, err := spawnFromBinary(ctx, bin, Spec{Timeout: 2 * time.Second, ExtraArgs: []string{"--crash-early"}})
	require.Error(t, err)
	require.Nil(t, h)
}

func TestSpawnHandshakeTimeout(t *testing.T) {
	// Use /bin/sleep (Unix) or skip on Windows.
	if runtime.GOOS == "windows" {
		t.Skip("timeout test uses /bin/sleep")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := spawnFromBinary(ctx, "/bin/sleep", Spec{Timeout: 500 * time.Millisecond, ExtraArgs: []string{"10"}})
	require.ErrorIs(t, err, ErrReadyTimeout)
	require.Nil(t, h)
}

func TestShutdownIdempotent(t *testing.T) {
	bin := fakeServerBin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := spawnFromBinary(ctx, bin, Spec{Timeout: 3 * time.Second})
	require.NoError(t, err)
	require.NoError(t, h.Shutdown())
	require.NoError(t, h.Shutdown())
}

func TestSpawnUsesEmbeddedBinary(t *testing.T) {
	// This test will FAIL until the real server binary is placed under
	// desktop/internal/bundle/server-bin/<goos>-<goarch>/. Skip if the
	// embed is a tiny stub (the <1024-byte placeholder case during dev).
	if len(serverBin) < 1024 {
		t.Skip("embed is a dev stub (<1KB); run `make _personal-prepare-current` first")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	h, err := Spawn(ctx, Spec{Timeout: 10 * time.Second, DataDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { h.Shutdown() })
	require.NotEmpty(t, h.Addr)
}

func TestOnCrashFiresOnExternalKill(t *testing.T) {
	bin := fakeServerBin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	crashed := make(chan error, 1)
	h, err := spawnFromBinary(ctx, bin, Spec{
		Timeout: 3 * time.Second,
		OnCrash: func(e error) {
			select {
			case crashed <- e:
			default:
			}
		},
	})
	require.NoError(t, err)

	// Kill externally, NOT via Shutdown — onCrash should fire
	require.NoError(t, h.cmd.Process.Kill())

	select {
	case <-crashed:
	case <-time.After(3 * time.Second):
		t.Fatal("OnCrash did not fire after external kill")
	}

	t.Cleanup(func() { h.Shutdown() })
}

func TestShutdownSIGKILLEscalation(t *testing.T) {
	if runtime.GOOS == "windows" {
		// On Windows the fake-server's --ignore-sigterm flag uses signal.Notify
		// which doesn't block CTRL_BREAK_EVENT. Skip; Windows tests Shutdown
		// escalation differently.
		t.Skip("--ignore-sigterm is Unix-signal specific")
	}
	bin := fakeServerBin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h, err := spawnFromBinary(ctx, bin, Spec{
		Timeout:   3 * time.Second,
		ExtraArgs: []string{"--ignore-sigterm"},
	})
	require.NoError(t, err)

	start := time.Now()
	require.NoError(t, h.Shutdown())
	elapsed := time.Since(start)

	// Grace is 3s; should have waited at least most of it before SIGKILL
	require.GreaterOrEqual(t, elapsed, 2*time.Second,
		"Shutdown returned too fast — SIGTERM grace skipped")
	require.Less(t, elapsed, 6*time.Second,
		"Shutdown took too long — SIGKILL fallback didn't fire")
}

func TestWaitAfterShutdown(t *testing.T) {
	bin := fakeServerBin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h, err := spawnFromBinary(ctx, bin, Spec{Timeout: 3 * time.Second})
	require.NoError(t, err)
	require.NoError(t, h.Shutdown())

	// Wait() must return promptly after Shutdown (child has already exited)
	done := make(chan error, 1)
	go func() { done <- h.Wait() }()
	select {
	case <-done:
		// exit err may be non-nil (signaled termination); that's acceptable
	case <-time.After(2 * time.Second):
		t.Fatal("Wait() did not return after Shutdown")
	}
}

func TestWaitAfterCrash(t *testing.T) {
	bin := fakeServerBin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h, err := spawnFromBinary(ctx, bin, Spec{Timeout: 3 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { h.Shutdown() })

	// External kill
	require.NoError(t, h.cmd.Process.Kill())

	done := make(chan error, 1)
	go func() { done <- h.Wait() }()
	select {
	case err := <-done:
		// On external kill, Wait returns a non-nil error (exit status)
		require.Error(t, err, "Wait should return exit error after kill")
	case <-time.After(3 * time.Second):
		t.Fatal("Wait() did not return after kill")
	}
}
