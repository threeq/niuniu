package agentproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The team-edition Linux cascade these tests pin: niuniu's SIGKILL lifecycle
// (idle reaper / watchdog / restarts) leaks codex's existence-based
// thread-writer locks, every later thread/resume of that session then fails
// with "thread-store conflict: thread already has an active writer", and the
// intentional kills themselves were logged as ERROR + surfaced to the chat as
// "exited unexpectedly".

func TestIsCodexThreadLockConflict(t *testing.T) {
	yes := []string{
		"app-server thread/resume: thread-store conflict: thread already has an active writer",
		"thread/start failed: error creating thread: Failed to initialize session: thread-store internal error: failed to create thread writer lock directory /h/thread-writer-locks/01a1.lock: File exists (os error 17)",
	}
	for _, msg := range yes {
		if !isCodexThreadLockConflict(errors.New(msg)) {
			t.Fatalf("should match: %s", msg)
		}
	}
	no := []string{
		"app-server thread/resume: process exited",
		"app-server thread/resume: thread not found",
	}
	for _, msg := range no {
		if isCodexThreadLockConflict(errors.New(msg)) {
			t.Fatalf("must not match: %s", msg)
		}
	}
	if isCodexThreadLockConflict(nil) {
		t.Fatal("nil must not match")
	}
}

func TestCodexHomeFromEnv(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	if got := codexHomeFromEnv([]string{"OTHER=x", "CODEX_HOME=" + filepath.Join(tmp, "hx")}); got != filepath.Join(tmp, "hx") {
		t.Fatalf("CODEX_HOME override lost: %q", got)
	}
	if got := codexHomeFromEnv([]string{"CODEX_HOME="}); got != filepath.Join(tmp, ".codex") {
		t.Fatalf("empty CODEX_HOME must fall back to ~/.codex, got %q", got)
	}
	if got := codexHomeFromEnv(nil); got != filepath.Join(tmp, ".codex") {
		t.Fatalf("default home = %q want %q", got, filepath.Join(tmp, ".codex"))
	}
}

func TestRemoveStaleCodexThreadLock(t *testing.T) {
	home := t.TempDir()
	env := []string{"CODEX_HOME=" + home}
	dir := filepath.Join(home, "thread-writer-locks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, "tid.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	removeStaleCodexThreadLock(env, "tid")
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("stale lock still present after removal (stat err=%v)", err)
	}

	// Missing lock and empty thread id are no-ops, not panics.
	removeStaleCodexThreadLock(env, "tid")
	removeStaleCodexThreadLock(env, "")
}

// TestCodexEventLoop_IntentionalKillIsQuiet: when the process death was
// niuniu's own decision (killRequested — Stop / idle reaper / watchdog), the
// event loop must clean up plumbing but NOT surface "exited unexpectedly" as
// the in-flight turn's result. Before this gate, every routine idle reaping
// logged an ERROR and flashed a red error bubble in the chat.
func TestCodexEventLoop_IntentionalKillIsQuiet(t *testing.T) {
	s := newDispatchTestSession(t)
	s.hub = NewSessionHub()
	t.Cleanup(s.hub.Stop)

	events := make(chan codexAppServerNotification, 8)
	app := &codexAppServerClient{
		pending: map[int64]chan codexAppServerResponse{},
		events:  events,
		done:    make(chan struct{}),
	}
	app.killRequested.Store(true)

	// A turn is in flight — the exact state where the unguarded exit path
	// reported the deliberate kill as "Codex app-server exited unexpectedly".
	turnDone := make(chan struct{})
	s.mu.Lock()
	s.running = true
	s.turnMsgId = "m-1"
	s.turnDone = turnDone
	s.mu.Unlock()

	go s.codexAppServerEventLoop(context.Background(), app)
	close(events) // process exit: the events channel closes

	select {
	case <-turnDone:
		t.Fatal("intentional kill surfaced a turn error — must stay quiet")
	case <-time.After(500 * time.Millisecond):
		// Quiet path: loop cleaned up and returned without touching the turn.
	}
}

// TestCodexAppServerClient_CloseStopsGracefullyOnPOSIX: Close() asks the
// process to exit with SIGTERM first. `sleep` dies on TERM immediately, so
// Close must return well inside the grace window — waiting the full period
// would mean the graceful hand is broken and every kill burns the grace time.
func TestCodexAppServerClient_CloseStopsGracefullyOnPOSIX(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal shutdown path")
	}
	orig := codexAppServerGracePeriod
	codexAppServerGracePeriod = 2 * time.Second
	t.Cleanup(func() { codexAppServerGracePeriod = orig })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sleep", "30")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &codexAppServerClient{
		cmd:     cmd,
		stdin:   stdin,
		cancel:  cancel,
		pending: map[int64]chan codexAppServerResponse{},
		events:  make(chan codexAppServerNotification, 4),
		done:    make(chan struct{}),
	}
	go c.readLoop(stdout)

	start := time.Now()
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("Close took %s — SIGTERM should end the process immediately, not wait out the %s grace window", elapsed, codexAppServerGracePeriod)
	}
}

// TestReadLineCapped_OversizeLineIsDrainedAndDropped: a line over the cap must
// be reported (and fully drained through its newline) WITHOUT losing the next
// line — the dropped item is shed-able traffic, the stream is not.
func TestReadLineCapped_OversizeLineIsDrainedAndDropped(t *testing.T) {
	big := strings.Repeat("x", 3<<20) // 3MB, cap 1MB
	input := `{"junk":"` + big + `"}` + "\n" + `{"method":"thread/started"}` + "\n"
	r := bufio.NewReaderSize(strings.NewReader(input), 64*1024)

	if _, err := readLineCapped(r, 1<<20); !errors.Is(err, errCodexLineOversize) {
		t.Fatalf("first read err=%v want errCodexLineOversize", err)
	}
	line, err := readLineCapped(r, 1<<20)
	if err != nil {
		t.Fatalf("second read err=%v", err)
	}
	if string(line) != `{"method":"thread/started"}`+"\n" {
		t.Fatalf("next line lost after oversize drop: %q", line)
	}
	if _, err := readLineCapped(r, 1<<20); !errors.Is(err, io.EOF) {
		t.Fatalf("final err=%v want io.EOF", err)
	}
}

// TestCodexAppServerClient_ReadLoopSurvivesOversizedLine is the regression for
// the team-edition crash loop: codex's base64 image tool outputs arrive as
// single stdout lines larger than the old 1MB scanner cap. The scanner aborted
// ("token too long"), readLoop returned SILENTLY, and the event loop declared
// the still-alive process dead — leaving the live process holding its
// thread-writer flock, so every later thread/resume failed with "already has
// an active writer" and the workspace looped spawn→conflict→fresh-thread
// forever. The stream must survive an oversized line: only the line is shed.
func TestCodexAppServerClient_ReadLoopSurvivesOversizedLine(t *testing.T) {
	c := &codexAppServerClient{
		pending: map[int64]chan codexAppServerResponse{},
		events:  make(chan codexAppServerNotification, 8),
		done:    make(chan struct{}),
	}
	oversize := `{"method":"item/completed","params":{"junk":"` + strings.Repeat("A", 3<<20) + `"}}`
	input := oversize + "\n" + `{"method":"thread/started","params":{"thread":{"id":"t-9"}}}` + "\n"

	go c.readLoop(strings.NewReader(input))

	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-c.events:
			if !ok {
				t.Fatal("events closed before thread/started — oversized line killed the stream")
			}
			if ev.Method == "thread/started" {
				return // stream survived the oversized line — contract holds
			}
		case <-deadline:
			t.Fatal("thread/started never delivered after an oversized line")
		}
	}
}
