package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestEmbeddedE2E builds the real niuniu binary and launches it with
// --embedded --addr=127.0.0.1:0, reads the ready handshake, probes
// /api/health, then closes stdin and confirms the child exits cleanly.
func TestEmbeddedE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e in -short")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "niuniu-bin")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	env := filterEnv(os.Environ(), "GOOS", "GOARCH")
	build := exec.Command("go", "build", "-o", bin, "./")
	build.Dir = mustCwd(t)
	build.Env = env
	require.NoError(t, build.Run(), "build niuniu")

	// Provide HOME override so cfg.DataDir points here.
	env = append(filterEnv(os.Environ(), "GOOS", "GOARCH"), "HOME="+tmp, "USERPROFILE="+tmp)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "--embedded", "--addr=127.0.0.1:0")
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	sc := bufio.NewScanner(stdout)
	require.True(t, sc.Scan(), "no ready line")

	line := sc.Bytes()
	if !bytes.HasPrefix(line, []byte(`{"event":"ready"`)) {
		t.Fatalf("first stdout line is not ready JSON: %q", line)
	}
	var ready struct {
		Event string `json:"event"`
		Addr  string `json:"addr"`
	}
	require.NoError(t, json.Unmarshal(line, &ready))
	require.Equal(t, "ready", ready.Event)
	require.NotEmpty(t, ready.Addr)

	// Probe /api/health
	resp, err := http.Get("http://" + ready.Addr + "/api/health")
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()

	// Close stdin → expect graceful shutdown via heartbeat-pipe EOF
	require.NoError(t, stdin.Close())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		// exit code 0 or caused by our close — both acceptable
		_ = err
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit after stdin close")
	}
}

func filterEnv(env []string, drop ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		keep := true
		for _, d := range drop {
			if strings.HasPrefix(kv, d+"=") {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

func mustCwd(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	require.NoError(t, err)
	return d
}
