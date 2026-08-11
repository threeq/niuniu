package goose

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

// fakeGoosePath is the compiled testdata/fakegoose server, set in TestMain.
var fakeGoosePath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakegoose")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	fakeGoosePath = filepath.Join(dir, "fakegoose.exe")
	cmd := exec.Command("go", "build", "-o", fakeGoosePath,
		"github.com/niuniu-dev/niuniu/internal/agentbackend/goose/testdata/fakegoose")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		panic("build fakegoose: " + err.Error() + "\n" + string(out))
	}
	os.Exit(m.Run())
}

func TestBackendFullCycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	be := New(Options{
		Command: fakeGoosePath,
		ResolvePermission: func(ctx context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
			assert.Equal(t, "confirm", req.Method)
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
	assert.Equal(t, 100, last.InputTokens)

	// Verify text + tool + permission round-trip landed.
	var text strings.Builder
	var sawToolUse, sawToolResult bool
	for _, ev := range events {
		switch ev.Type {
		case agentbackend.EventText:
			text.WriteString(ev.Text)
		case agentbackend.EventToolUse:
			sawToolUse = ev.ToolName == "fake_tool"
		case agentbackend.EventToolResult:
			sawToolResult = strings.Contains(ev.Text, "true") // allow written back
		}
	}
	assert.Contains(t, text.String(), "Hello from fake goose")
	assert.True(t, sawToolUse, "expected tool_use event")
	assert.True(t, sawToolResult, "expected tool_result confirming the permission decision flowed back")
}

func TestBackendPromptFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	be := New(Options{Command: fakeGoosePath, Env: []string{"FAKE_GOOSE_FAIL_PROMPT=1"}})
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

	// No ResolvePermission bridge → the permission request is auto-denied (fail
	// closed), the fake sees allow=false, and the turn still ends done.
	be := New(Options{Command: fakeGoosePath})
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

func TestBackendErrorStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	be := New(Options{Command: fakeGoosePath, Env: []string{"FAKE_GOOSE_FAIL_AFTER_PERMISSION=1"}})
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

func TestBackendAbortAndClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	be := New(Options{Command: fakeGoosePath})
	require.NoError(t, be.Start(ctx))
	require.NoError(t, be.Abort(ctx))
	require.NoError(t, be.Close(ctx))
	require.NoError(t, be.Close(ctx)) // Close is idempotent.
}

func TestBackendHandshakeTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// fakegoose delays its initialize response well past the handshake timeout.
	be := New(Options{Command: fakeGoosePath, Env: []string{"FAKE_GOOSE_DELAY_MS=5000"}, HandshakeTimeout: 200 * time.Millisecond})
	err := be.Start(ctx)
	require.Error(t, err)
}

func TestBackendMissingBinary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	be := New(Options{Command: "cmd-not-a-real-goose-xyz", HandshakeTimeout: time.Second})
	err := be.Start(ctx)
	require.Error(t, err)
}