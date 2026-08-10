// Package bundle manages the embedded niuniu server subprocess.
// Public API is Spec + Handle; platform-specific details live in
// bundle_windows.go (Job Object) and bundle_unix.go (setpgid + pipe).
package bundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrReadyTimeout    = errors.New("bundle: server did not emit ready within timeout")
	ErrServerExitEarly = errors.New("bundle: server exited before ready handshake")
	ErrExtract         = errors.New("bundle: failed to extract embedded server binary")
)

// Spec describes how the caller wants the server launched.
type Spec struct {
	// DataDir is the niuniu data directory, e.g. ~/.niuniu.
	// It MUST end with the basename ".niuniu" — the server computes its
	// data dir as filepath.Join(os.UserHomeDir(), ".niuniu"), so we
	// export HOME=<DataDir>/.. to redirect the server's home to the
	// parent of DataDir. A DataDir like "/tmp/xyz" would cause the
	// server to use "/tmp/.niuniu" instead of "/tmp/xyz" — do not do that.
	DataDir string

	// LogFile is the **template** path for rotated stderr files. The actual
	// output files are "<dir>/<stem>-YYYY-MM-DD<ext>" inside the same
	// directory; LogFile itself is never written to. If empty, stderr is
	// discarded. Receives the child's stderr plus any extra stdout drained
	// after the ready handshake.
	LogFile string

	// LogRetentionDays is the number of days of rotated stderr log files to
	// keep. Files matching the LogFile basename pattern with mtime older than
	// this are deleted on rotation. Zero applies a default of 30 days;
	// negative disables pruning entirely.
	LogRetentionDays int

	// Timeout for the ready handshake. Defaults to 60s if zero — cold
	// boot including schema init and antivirus scanning can exceed 30s.
	Timeout time.Duration

	// OnCrash is invoked when the child exits before Shutdown was called.
	// Nil is OK.
	OnCrash func(error)

	// ExtraArgs appended after --embedded --addr=127.0.0.1:0.
	// Primarily for tests.
	ExtraArgs []string
}

// Handle represents the running server subprocess. All methods are safe
// for concurrent use; Shutdown is idempotent.
type Handle struct {
	// Addr is the actual bound address reported by the ready handshake,
	// e.g. "127.0.0.1:54321".
	Addr string

	cmd           *exec.Cmd
	stdinPipe     io.WriteCloser
	expectedExit  atomic.Int32
	exitedCh      chan struct{}
	exitErr       error
	onCrash       func(error)
	shutdownOnce  sync.Once
	platformClose func() // set by bundle_{windows,unix}.go
	stopCtxWatch  chan struct{}
	stderrWriter  io.Closer // closed in waitLoop after cmd.Wait
}

// Spawn extracts the embedded server (cached by hash), launches it with
// --embedded --addr=127.0.0.1:0, reads the ready JSON from stdout, and
// returns a Handle. Errors indicate the child is already cleaned up.
func Spawn(ctx context.Context, spec Spec) (*Handle, error) {
	cacheDir, err := userCacheDir()
	if err != nil {
		return nil, fmt.Errorf("%w: user cache: %v", ErrExtract, err)
	}
	binPath, err := extractToCache(cacheDir, serverBinName, serverBinKey, bytesReader(serverBin))
	if err != nil {
		return nil, err
	}
	// Extract niuniu-mcp into the SAME dir as the server, so the server's
	// MCPConfigGenerator.FindMCPBinary finds it via os.Executable() + dirname.
	mcpPath, err := extractToCache(cacheDir, mcpBinName, mcpBinKey, bytesReader(mcpBin))
	if err != nil {
		return nil, err
	}
	h, err := spawnFromBinary(ctx, binPath, spec)
	if err != nil {
		return nil, err
	}
	// Fire-and-forget: GC old cached binaries. Errors ignored.
	go func() {
		_, _ = gcOldCacheEntries(cacheDir, "niuniu-server-", binPath, 7*24*time.Hour)
		_, _ = gcOldCacheEntries(cacheDir, "niuniu-mcp-", mcpPath, 7*24*time.Hour)
	}()
	return h, nil
}

func userCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "niuniu-desktop"), nil
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// hashKey returns the first 16 hex chars of the SHA-256 of b. Used to
// build a content-addressed cache key for the embedded server binary.
func hashKey(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// Wait blocks until the child has exited. Returns the exit error (nil
// if the child exited with status 0).
func (h *Handle) Wait() error {
	<-h.exitedCh
	return h.exitErr
}
