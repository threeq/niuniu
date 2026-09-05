package cursor

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

// fakeCursorPath is the compiled testdata/fakecursor ACP server, built in
// TestMain. Driving the real JSON-RPC/stdio protocol (rather than mocking the
// backend's own helpers) is the point: the wire shapes are what this integration
// can get wrong.
var fakeCursorPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakecursor")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	fakeCursorPath = filepath.Join(dir, "fakecursor.exe")
	cmd := exec.Command("go", "build", "-o", fakeCursorPath,
		"github.com/niuniu-dev/niuniu/internal/agentbackend/cursor/testdata/fakecursor")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		panic("build fakecursor: " + err.Error() + "\n" + string(out))
	}
	os.Exit(m.Run())
}

func newBackend(t *testing.T, mode string, resolve func(context.Context, agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error)) *Backend {
	t.Helper()
	return New(Options{
		Command:           fakeCursorPath,
		WorkDir:           t.TempDir(),
		Env:               []string{"FAKECURSOR_MODE=" + mode},
		ResolvePermission: resolve,
	})
}

func drain(t *testing.T, ch <-chan agentbackend.Event) []agentbackend.Event {
	t.Helper()
	var evs []agentbackend.Event
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

func TestStartHandshakeAndSessionID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "text-only", nil)
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	// session/new's sessionId must be captured: the host persists it so the SPA
	// shows a live session (ACP has no system/init stream line to key off).
	assert.Equal(t, "sess-fake-1", be.SessionID())

	// Start is idempotent.
	require.NoError(t, be.Start(ctx))
}

func TestPromptStreamsTextAndDone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "text-only", nil)
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, err := be.Prompt(ctx, agentbackend.PromptRequest{Message: "hi"})
	require.NoError(t, err)
	evs := drain(t, ch)

	var text string
	for _, ev := range evs {
		if ev.Type == agentbackend.EventText {
			text += ev.Text
		}
	}
	assert.Equal(t, "hello world", text)
	require.NotEmpty(t, evs)
	// The turn must settle with a terminal Done so the host's drain loop ends.
	assert.Equal(t, agentbackend.EventDone, evs[len(evs)-1].Type)
}

func TestPromptThinkingChunk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "thinking", nil)
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, _ := be.Prompt(ctx, agentbackend.PromptRequest{Message: "think"})
	evs := drain(t, ch)

	found := false
	for _, ev := range evs {
		if ev.Type == agentbackend.EventThinking && ev.Thinking == "pondering" {
			found = true
		}
	}
	assert.True(t, found, "agent_thought_chunk should map to EventThinking, got %+v", evs)
}

// TestToolCallCycle pins the tool mapping, including the rule that a
// NON-terminal tool_call_update must not emit a tool_result (doing so paints a
// premature result in the SPA).
func TestToolCallCycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "tools", nil)
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, _ := be.Prompt(ctx, agentbackend.PromptRequest{Message: "read"})
	evs := drain(t, ch)

	var uses, results []agentbackend.Event
	for _, ev := range evs {
		switch ev.Type {
		case agentbackend.EventToolUse:
			uses = append(uses, ev)
		case agentbackend.EventToolResult:
			results = append(results, ev)
		}
	}
	require.Len(t, uses, 1, "expected exactly one tool_use, got %+v", evs)
	assert.Equal(t, "Read", uses[0].ToolName)
	assert.Equal(t, "tc-1", uses[0].ToolUseID)
	assert.Contains(t, uses[0].ToolInput, "README.md")

	require.Len(t, results, 1, "in_progress must NOT produce a tool_result; got %d", len(results))
	assert.Equal(t, "tc-1", results[0].ToolUseID)
	// The name is carried over from the opening tool_call so the SPA can label it.
	assert.Equal(t, "Read", results[0].ToolName)
	assert.False(t, results[0].IsError)
	assert.Contains(t, results[0].Text, "file body")
}

// TestPermissionRequestAllow is the capability that motivated moving to ACP: a
// real approval gate. It also verifies request_permission is treated as a
// REQUEST (answered with a JSON-RPC response), not a notification — the fake
// server only proceeds once it receives that response.
func TestPermissionRequestAllow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var gotReq agentbackend.PermissionRequest
	be := newBackend(t, "permission", func(_ context.Context, req agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
		gotReq = req
		return agentbackend.PermissionDecision{Confirmed: true}, nil
	})
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, _ := be.Prompt(ctx, agentbackend.PromptRequest{Message: "rm"})
	evs := drain(t, ch)

	// The host saw a describable request.
	assert.Equal(t, "rm -rf /tmp/x", gotReq.Title)
	assert.Contains(t, gotReq.Message, "rm -rf /tmp/x")
	assert.Equal(t, "tc-1", gotReq.ID)
	assert.NotEmpty(t, gotReq.Options)

	// And the agent received "allow-once".
	var text string
	for _, ev := range evs {
		if ev.Type == agentbackend.EventText {
			text += ev.Text
		}
	}
	assert.Contains(t, text, "decision=selected:allow-once")
}

func TestPermissionRequestDeny(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "permission", func(_ context.Context, _ agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
		return agentbackend.PermissionDecision{Cancelled: true}, nil
	})
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, _ := be.Prompt(ctx, agentbackend.PromptRequest{Message: "rm"})
	var text string
	for _, ev := range drain(t, ch) {
		if ev.Type == agentbackend.EventText {
			text += ev.Text
		}
	}
	assert.Contains(t, text, "decision=selected:reject-once")
}

// TestPermissionNoBridgeFailsClosed: an unwired gate must reject, never
// auto-approve.
func TestPermissionNoBridgeFailsClosed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "permission", nil)
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, _ := be.Prompt(ctx, agentbackend.PromptRequest{Message: "rm"})
	var text string
	for _, ev := range drain(t, ch) {
		if ev.Type == agentbackend.EventText {
			text += ev.Text
		}
	}
	assert.Contains(t, text, "reject-once", "no permission bridge must fail closed")
}

// TestPermissionRenamedOptionIDs proves the fallback matching works: if Cursor
// renames its option ids, selection degrades to kind/prefix matching instead of
// hanging the turn.
func TestPermissionRenamedOptionIDs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "renamed-options", func(_ context.Context, _ agentbackend.PermissionRequest) (agentbackend.PermissionDecision, error) {
		return agentbackend.PermissionDecision{Confirmed: true}, nil
	})
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, _ := be.Prompt(ctx, agentbackend.PromptRequest{Message: "write"})
	var text string
	for _, ev := range drain(t, ch) {
		if ev.Type == agentbackend.EventText {
			text += ev.Text
		}
	}
	assert.Contains(t, text, "decision=selected:allow_v2")
}

// TestBlockingExtensionAnswered: cursor/ask_question blocks the agent until the
// client responds. Leaving it unanswered would hang the turn, so the backend
// replies "skipped".
func TestBlockingExtensionAnswered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "blocking-extension", nil)
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, _ := be.Prompt(ctx, agentbackend.PromptRequest{Message: "ask"})
	evs := drain(t, ch)
	require.NotEmpty(t, evs)
	assert.Equal(t, agentbackend.EventDone, evs[len(evs)-1].Type)
}

func TestRefusalSurfacesAsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "refusal", nil)
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, _ := be.Prompt(ctx, agentbackend.PromptRequest{Message: "bad"})
	evs := drain(t, ch)
	require.NotEmpty(t, evs)
	last := evs[len(evs)-1]
	assert.Equal(t, agentbackend.EventError, last.Type)
	assert.Contains(t, last.Error, "refused")
}

// TestAbortCancelsTurn covers cooperative cancellation — the capability the
// one-shot path could only approximate by killing the process.
func TestAbortCancelsTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "cancel", nil)
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, err := be.Prompt(ctx, agentbackend.PromptRequest{Message: "long task"})
	require.NoError(t, err)
	require.NoError(t, be.Abort(ctx))

	evs := drain(t, ch)
	require.NotEmpty(t, evs, "cancel must still settle the turn")
	assert.Equal(t, agentbackend.EventDone, evs[len(evs)-1].Type)
}

// TestSecondPromptRejectedWhileInFlight pins the one-turn-at-a-time contract the
// agentbackend.Backend interface documents.
func TestSecondPromptRejectedWhileInFlight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "cancel", nil) // blocks until cancelled
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	ch, err := be.Prompt(ctx, agentbackend.PromptRequest{Message: "first"})
	require.NoError(t, err)

	_, err2 := be.Prompt(ctx, agentbackend.PromptRequest{Message: "second"})
	require.Error(t, err2)
	assert.Contains(t, err2.Error(), "in-flight")

	_ = be.Abort(ctx)
	drain(t, ch)
}

// TestMultiTurnReusesSession is the other headline ACP capability: several turns
// on ONE process and ONE session, instead of re-establishing context per message.
func TestMultiTurnReusesSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	be := newBackend(t, "text-only", nil)
	require.NoError(t, be.Start(ctx))
	defer be.Close(ctx)

	sid := be.SessionID()
	for i := 0; i < 3; i++ {
		ch, err := be.Prompt(ctx, agentbackend.PromptRequest{Message: "turn"})
		require.NoError(t, err, "turn %d", i)
		evs := drain(t, ch)
		require.NotEmpty(t, evs, "turn %d produced no events", i)
		assert.Equal(t, agentbackend.EventDone, evs[len(evs)-1].Type, "turn %d", i)
	}
	assert.Equal(t, sid, be.SessionID(), "session id must be stable across turns")
}

// TestAuthenticateErrorTolerated: some builds reject a redundant `authenticate`
// when already logged in. That must not make an otherwise-usable session fail.
func TestAuthenticateErrorTolerated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "auth-error", nil)
	require.NoError(t, be.Start(ctx), "an authenticate error must not abort Start")
	defer be.Close(ctx)
	assert.Equal(t, "sess-fake-1", be.SessionID())
}

// TestSessionNewErrorFailsStart: a genuinely unauthenticated CLI fails at
// session/new, and that MUST surface (unlike the tolerated authenticate error).
func TestSessionNewErrorFailsStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "session-error", nil)
	err := be.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "not logged in")
}

// TestCloseSettlesInFlightTurn: tearing down mid-turn must close the channel so
// the host's `for ev := range ch` loop terminates instead of hanging.
func TestCloseSettlesInFlightTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	be := newBackend(t, "cancel", nil)
	require.NoError(t, be.Start(ctx))

	ch, err := be.Prompt(ctx, agentbackend.PromptRequest{Message: "long"})
	require.NoError(t, err)
	require.NoError(t, be.Close(ctx))

	evs := drain(t, ch) // must not block
	require.NotEmpty(t, evs)
	assert.Equal(t, agentbackend.EventError, evs[len(evs)-1].Type)

	// Close is idempotent.
	require.NoError(t, be.Close(ctx))
}

// TestMcpServersParamShape pins the ACP `mcpServers` encoding, including env as a
// name/value ARRAY rather than an object. Getting this wrong is how niuniu-mcp
// would silently fail to load — the exact failure mode that made the headless
// path unusable.
func TestMcpServersParamShape(t *testing.T) {
	out := mcpServersParam([]McpServer{{
		Name:    "niuniu",
		Command: "niuniu-mcp",
		Args:    []string{"--workspace", "7"},
		Env:     map[string]string{"NIUNIU_MCP_TOKEN": "tok"},
	}})
	require.Len(t, out, 1)
	entry, ok := out[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "niuniu", entry["name"])
	assert.Equal(t, "niuniu-mcp", entry["command"])
	assert.Equal(t, []string{"--workspace", "7"}, entry["args"])

	envArr, ok := entry["env"].([]any)
	require.True(t, ok, "env must be a name/value array, got %T", entry["env"])
	require.Len(t, envArr, 1)
	kv := envArr[0].(map[string]any)
	assert.Equal(t, "NIUNIU_MCP_TOKEN", kv["name"])
	assert.Equal(t, "tok", kv["value"])
}

func TestChooseOption(t *testing.T) {
	documented := []permissionOption{
		{OptionID: "allow-once", Kind: "allow_once"},
		{OptionID: "reject-once", Kind: "reject_once"},
	}
	assert.Equal(t, "allow-once", chooseOption(documented, true))
	assert.Equal(t, "reject-once", chooseOption(documented, false))

	byKind := []permissionOption{
		{OptionID: "x1", Kind: "allow_once"},
		{OptionID: "x2", Kind: "reject_once"},
	}
	assert.Equal(t, "x1", chooseOption(byKind, true))
	assert.Equal(t, "x2", chooseOption(byKind, false))

	byPrefix := []permissionOption{
		{OptionID: "allow_something"},
		{OptionID: "reject_something"},
	}
	assert.Equal(t, "allow_something", chooseOption(byPrefix, true))
	assert.Equal(t, "reject_something", chooseOption(byPrefix, false))

	// Nothing matches → empty, and the caller cancels rather than inventing an id
	// the agent never offered.
	assert.Equal(t, "", chooseOption([]permissionOption{{OptionID: "weird"}}, true))
	assert.Equal(t, "", chooseOption(nil, false))
}

func TestIsTerminalToolStatus(t *testing.T) {
	for _, s := range []string{"completed", "failed", "error", "cancelled"} {
		assert.True(t, isTerminalToolStatus(s), s)
	}
	for _, s := range []string{"", "pending", "in_progress", "running"} {
		assert.False(t, isTerminalToolStatus(s), s)
	}
}

func TestCompactJSONFallsBackToEmptyObject(t *testing.T) {
	assert.Equal(t, "{}", string(compactJSON(nil)))
	assert.Equal(t, "{}", string(compactJSON([]byte("not json"))))
	assert.Equal(t, `{"a":1}`, string(compactJSON([]byte(`{"a":1}`))))
}
