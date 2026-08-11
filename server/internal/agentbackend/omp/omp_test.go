package omp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentbackend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOMPPath is the compiled testdata/fakeomp server, set in TestMain.
var fakeOMPPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakeomp")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	fakeOMPPath = filepath.Join(dir, "fakeomp.exe")
	cmd := exec.Command("go", "build", "-o", fakeOMPPath,
		"github.com/niuniu-dev/niuniu/internal/agentbackend/omp/testdata/fakeomp")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		panic("build fakeomp: " + err.Error() + "\n" + string(out))
	}
	os.Exit(m.Run())
}

func backend(t *testing.T, extraEnv ...string) *Backend {
	t.Helper()
	env := append([]string{}, extraEnv...)
	return New(Options{Command: fakeOMPPath, Env: env})
}

func TestBackendFullCycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	be := New(Options{
		Command: fakeOMPPath,
		ResolvePermission: func(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
			// The host surface (niuniu permission card) answered "confirm".
			assert.Equal(t, "confirm", req.Method)
			assert.Equal(t, "ui_1", req.ID)
			return agentbackend.PermissionDecision{Confirmed: true}, nil
		},
	})
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, err := be.Prompt(ctx, agentbackend.PromptRequest{Message: "run a task"})
	require.NoError(t, err)

	var events []agentbackend.Event
	for ev := range ch {
		events = append(events, ev)
	}
	require.NotEmpty(t, events)

	// Last event must be EventDone with cost telemetry.
	last := events[len(events)-1]
	assert.Equal(t, agentbackend.EventDone, last.Type)
	assert.InDelta(t, 0.0123, last.CostUSD, 1e-6)
	assert.Equal(t, 1, last.NumTurns)
	assert.Equal(t, 100, last.InputTokens)

	// Verify text + tool + extension round-trip landed.
	var text strings.Builder
	var sawToolUse, sawToolResult bool
	for _, ev := range events {
		switch ev.Type {
		case agentbackend.EventText:
			text.WriteString(ev.Text)
		case agentbackend.EventToolUse:
			sawToolUse = ev.ToolName == "fake_tool"
		case agentbackend.EventToolResult:
			sawToolResult = strings.Contains(ev.Text, "true") // confirmed written back
		}
	}
	assert.Contains(t, text.String(), "Hello from fake omp")
	assert.True(t, sawToolUse, "expected tool_use event")
	assert.True(t, sawToolResult, "expected tool_result confirming the extension UI decision flowed back")
}

func TestBackendPromptFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	be := backend(t, "FAKE_OMP_FAIL_PROMPT=1")
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, err := be.Prompt(ctx, agentbackend.PromptRequest{Message: "x"})
	require.NoError(t, err)
	var last agentbackend.Event
	for ev := range ch {
		last = ev
	}
	assert.Equal(t, agentbackend.EventError, last.Type)
	assert.Contains(t, last.Error, "model unavailable")
}

func TestBackendNoResolverAutoCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// No ResolvePermission bridge → extension_ui_request is auto-cancelled
	// (fail closed), the fake sees confirmed=false, and the turn still ends done.
	be := backend(t)
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, err := be.Prompt(ctx, agentbackend.PromptRequest{Message: "x"})
	require.NoError(t, err)
	var last agentbackend.Event
	for ev := range ch {
		last = ev
	}
	assert.Equal(t, agentbackend.EventDone, last.Type)
}

func TestBackendAbortAndClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	be := backend(t)
	require.NoError(t, be.Start(ctx))
	require.NoError(t, be.Abort(ctx))
	require.NoError(t, be.Close(ctx))
	require.NoError(t, be.Close(ctx)) // Close is idempotent.
}

func TestBackendReadyTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// fakeomp delays its ready frame well past the handshake timeout.
	be := New(Options{Command: fakeOMPPath, Env: []string{"FAKE_OMP_DELAY_MS=5000"}, HandshakeTimeout: 200 * time.Millisecond})
	err := be.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ready frame")
}

func TestBackendMissingBinary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	be := New(Options{Command: "cmd-not-a-real-agent-xyz", HandshakeTimeout: time.Second})
	err := be.Start(ctx)
	require.Error(t, err)
}
