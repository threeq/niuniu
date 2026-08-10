//go:build darwin || linux

package bundle

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// spawnFromBinary launches the given binary in embedded mode. This is
// the test seam: real callers go through Spawn (which extracts first).
func spawnFromBinary(ctx context.Context, bin string, spec Spec) (*Handle, error) {
	timeout := spec.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second // cold boot incl. schema + AV scan may exceed 30s
	}

	args := []string{"--embedded", "--addr=127.0.0.1:0"}
	args = append(args, spec.ExtraArgs...)

	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	env := os.Environ()
	if spec.DataDir != "" {
		// Redirect server's home so os.UserHomeDir() + "/.niuniu" == spec.DataDir
		env = setEnv(env, "HOME", spec.DataDir+"/..")
	}
	// macOS Finder/Dock (and Linux .desktop) launches give this GUI process a
	// minimal PATH that omits /usr/local/bin, /opt/homebrew/bin, nvm, etc. The
	// embedded server would inherit it and report node/git/claude as missing
	// even when installed (and fail to spawn agents). Recover the user's real
	// PATH before handing it to the child. See resolveChildPath in path.go.
	env = setEnv(env, "PATH", resolveChildPath(ctx))
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	var stderrW *rotatingWriter
	if spec.LogFile != "" {
		retention := spec.LogRetentionDays
		if retention == 0 {
			retention = 30
		}
		stderrW = newRotatingWriter(spec.LogFile, retention)
		cmd.Stderr = stderrW
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("start child: %w", err)
	}

	h := &Handle{
		cmd:          cmd,
		stdinPipe:    stdin,
		exitedCh:     make(chan struct{}),
		onCrash:      spec.OnCrash,
		stopCtxWatch: make(chan struct{}),
	}
	if stderrW != nil {
		h.stderrWriter = stderrW
	}
	h.platformClose = func() {
		// Send SIGTERM to the whole process group so any descendants die too.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
	}

	// Background: wait for exit, fire OnCrash if unexpected
	go h.waitLoop()

	// Foreground: read ready handshake
	addr, err := readReady(stdout, timeout)
	if err != nil {
		_ = h.Shutdown()
		return nil, err
	}
	h.Addr = addr

	// Stdout after ready: drain and tee to log file to prevent child from
	// blocking on a full stdout pipe.
	var sink io.Writer = io.Discard
	if cmd.Stderr != nil {
		sink = cmd.Stderr
	}
	go func() { _, _ = io.Copy(sink, stdout) }()

	// ctx-watcher: graceful Shutdown on context cancellation
	go func() {
		select {
		case <-ctx.Done():
			_ = h.Shutdown()
		case <-h.stopCtxWatch:
		}
	}()

	return h, nil
}

func (h *Handle) waitLoop() {
	h.exitErr = h.cmd.Wait()
	if h.stderrWriter != nil {
		_ = h.stderrWriter.Close()
	}
	close(h.exitedCh)
	if h.expectedExit.Load() == 0 && h.onCrash != nil {
		h.onCrash(h.exitErr)
	}
}

func (h *Handle) Shutdown() error {
	h.shutdownOnce.Do(func() {
		close(h.stopCtxWatch)
		h.expectedExit.Store(1)
		if h.platformClose != nil {
			h.platformClose()
		}
		// Close stdin so heartbeat-pipe path also fires.
		if h.stdinPipe != nil {
			_ = h.stdinPipe.Close()
		}

		select {
		case <-h.exitedCh:
		case <-time.After(3 * time.Second):
			if h.cmd.Process != nil {
				_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGKILL)
			}
			<-h.exitedCh
		}
	})
	return nil
}
