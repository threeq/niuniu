package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

func newLocalRunnerSvc(t *testing.T) (*LocalRunnerService, *store.Queries, int64) {
	t.Helper()
	// Keep the mid-reconnect grace tiny so offline-dispatch tests fail fast.
	runnerReconnectGrace = 20 * time.Millisecond
	rawDB := setupTestDB(t)
	q := store.New(rawDB)
	res, err := rawDB.Exec(`INSERT INTO workspaces (path, owner_type, owner_id) VALUES ('/tmp/ws', 'user', 1)`)
	require.NoError(t, err)
	wsID, err := res.LastInsertId()
	require.NoError(t, err)
	svc := NewLocalRunnerService(q, store.Wrap(rawDB), nil)
	return svc, q, wsID
}

func TestLocalRunner_ConfigRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newLocalRunnerSvc(t)

	// Unbound: no config, no error.
	cfg, err := svc.GetConfig(ctx, wsID)
	require.NoError(t, err)
	require.Nil(t, cfg)

	in := LocalRunnerConfig{
		LocalDir:           "/home/me/proj",
		PromptSnippet:      "prefer local_exec",
		AllowedCommands:    []string{"go", "make", "pnpm"},
		AlwaysAllowPersist: true,
	}
	saved, err := svc.SaveConfig(ctx, wsID, in)
	require.NoError(t, err)
	require.Equal(t, in.LocalDir, saved.LocalDir)
	require.Equal(t, in.AllowedCommands, saved.AllowedCommands)
	require.True(t, saved.AlwaysAllowPersist)

	got, err := svc.GetConfig(ctx, wsID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, in.PromptSnippet, got.PromptSnippet)
	require.Equal(t, []string{"go", "make", "pnpm"}, got.AllowedCommands)
	require.True(t, got.AlwaysAllowPersist)

	// Update overwrites (upsert).
	_, err = svc.SaveConfig(ctx, wsID, LocalRunnerConfig{LocalDir: "/other", AllowedCommands: nil})
	require.NoError(t, err)
	got, err = svc.GetConfig(ctx, wsID)
	require.NoError(t, err)
	require.Equal(t, "/other", got.LocalDir)
	require.Equal(t, []string{}, got.AllowedCommands)
	require.False(t, got.AlwaysAllowPersist)

	// Delete -> unbound again.
	require.NoError(t, svc.DeleteConfig(ctx, wsID))
	got, err = svc.GetConfig(ctx, wsID)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestLocalRunner_StatusMachine(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newLocalRunnerSvc(t)

	// No config -> unbound.
	st, _, err := svc.Status(ctx, wsID)
	require.NoError(t, err)
	require.Equal(t, StatusUnbound, st)

	// Config saved, no runner -> connecting.
	_, err = svc.SaveConfig(ctx, wsID, LocalRunnerConfig{LocalDir: "/x"})
	require.NoError(t, err)
	st, _, err = svc.Status(ctx, wsID)
	require.NoError(t, err)
	require.Equal(t, StatusConnecting, st)

	// Runner registers -> active.
	rc := svc.RegisterRunner(ctx, wsID)
	require.True(t, svc.IsOnline(wsID))
	st, _, err = svc.Status(ctx, wsID)
	require.NoError(t, err)
	require.Equal(t, StatusActive, st)

	// Runner drops -> degraded error (#476).
	svc.UnregisterRunner(ctx, rc)
	require.False(t, svc.IsOnline(wsID))
	st, _, err = svc.Status(ctx, wsID)
	require.NoError(t, err)
	require.Equal(t, StatusError, st)

	// Unbinding clears the degradation memory -> unbound.
	require.NoError(t, svc.DeleteConfig(ctx, wsID))
	st, _, err = svc.Status(ctx, wsID)
	require.NoError(t, err)
	require.Equal(t, StatusUnbound, st)
}

// #526: connect/disconnect must regenerate the workspace's scene projection so
// the local-runner tool group in .mcp.json tracks presence for the NEXT launch.
func TestLocalRunner_ReprojectsOnPresenceChange(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newLocalRunnerSvc(t)

	got := make(chan int64, 4)
	svc.SetReproject(func(_ context.Context, id int64) { got <- id })

	rc := svc.RegisterRunner(ctx, wsID)
	select {
	case id := <-got:
		require.Equal(t, wsID, id)
	case <-time.After(2 * time.Second):
		t.Fatal("reproject not called on RegisterRunner")
	}

	svc.UnregisterRunner(ctx, rc)
	select {
	case id := <-got:
		require.Equal(t, wsID, id)
	case <-time.After(2 * time.Second):
		t.Fatal("reproject not called on UnregisterRunner")
	}
}

func TestLocalRunner_LogFanOutAndReplay(t *testing.T) {
	svc, _, wsID := newLocalRunnerSvc(t)

	// A log before anyone subscribes still lands in the replay buffer.
	svc.AppendLog(wsID, "system", "boot")

	subID, ch, replay := svc.SubscribeLogs(wsID)
	require.Len(t, replay, 1)
	require.Equal(t, "boot", replay[0].Text)

	svc.AppendLog(wsID, "stdout", "building...")
	select {
	case data := <-ch:
		var entry LocalRunnerLog
		require.NoError(t, json.Unmarshal(data, &entry))
		require.Equal(t, "stdout", entry.Level)
		require.Equal(t, "building...", entry.Text)
	default:
		t.Fatal("expected a fanned-out log line")
	}

	svc.UnsubscribeLogs(wsID, subID)
	// After unsubscribe, further appends do not panic and are not delivered.
	svc.AppendLog(wsID, "stdout", "after")
	select {
	case <-ch:
		t.Fatal("no delivery expected after unsubscribe")
	default:
	}
}

func TestLocalRunner_LogRingCap(t *testing.T) {
	svc, _, wsID := newLocalRunnerSvc(t)
	for i := 0; i < logRingCap+50; i++ {
		svc.AppendLog(wsID, "stdout", "line")
	}
	_, _, replay := svc.SubscribeLogs(wsID)
	require.Len(t, replay, logRingCap)
}

func TestLocalRunner_Dispatch(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newLocalRunnerSvc(t)

	// Offline -> dispatch fails (surfaced as error to the AI, not silent).
	require.False(t, svc.Dispatch(wsID, []byte(`{"cmd":"go build"}`)))

	rc := svc.RegisterRunner(ctx, wsID)
	require.True(t, svc.Dispatch(wsID, []byte(`{"cmd":"go build"}`)))
	select {
	case frame := <-rc.Send:
		require.JSONEq(t, `{"cmd":"go build"}`, string(frame))
	default:
		t.Fatal("expected dispatched frame on runner Send channel")
	}
}

func TestLocalRunner_DisableToolGroupsFor(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newLocalRunnerSvc(t)

	// Offline -> local-runner group hidden.
	require.Equal(t, []string{LocalRunnerToolGroup}, svc.DisableToolGroupsFor(wsID))

	// Online -> group not hidden (tools appear).
	svc.RegisterRunner(ctx, wsID)
	require.Nil(t, svc.DisableToolGroupsFor(wsID))
}

func TestLocalRunner_RequestResponseRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newLocalRunnerSvc(t)

	// Offline: exec fails loudly (#476).
	_, err := svc.ExecCommand(ctx, wsID, "go build")
	require.ErrorIs(t, err, ErrRunnerOffline)

	rc := svc.RegisterRunner(ctx, wsID)
	// Fake desktop: read the dispatched request, reply with a result frame.
	go func() {
		frame := <-rc.Send
		var req struct {
			Type, ID, Kind, Command string
		}
		_ = json.Unmarshal(frame, &req)
		if req.Type != "request" || req.Kind != "exec" {
			return
		}
		reply, _ := json.Marshal(map[string]any{
			"type": "response", "id": req.ID, "ok": true,
			"stdout": "built ok", "exit": 0,
		})
		svc.HandleRunnerFrame(wsID, rc, reply)
	}()

	rep, err := svc.ExecCommand(ctx, wsID, "go build")
	require.NoError(t, err)
	require.True(t, rep.OK)
	require.Equal(t, "built ok", rep.Stdout)
	require.Equal(t, 0, rep.Exit)
}

func TestLocalRunner_RequestFailsWhenRunnerDropsMidFlight(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newLocalRunnerSvc(t)
	rc := svc.RegisterRunner(ctx, wsID)

	// Drain the request then drop the runner without replying.
	go func() {
		<-rc.Send
		svc.UnregisterRunner(ctx, rc)
	}()

	_, err := svc.ExecCommand(ctx, wsID, "sleep 999")
	require.ErrorIs(t, err, ErrRunnerOffline)
}

func TestLocalRunner_MCPInjectionGate(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newLocalRunnerSvc(t)

	// Offline: no injection.
	_, ok := svc.MCPInjection(ctx, wsID)
	require.False(t, ok)

	_, err := svc.SaveConfig(ctx, wsID, LocalRunnerConfig{LocalDir: "/x", PromptSnippet: "use local tools"})
	require.NoError(t, err)
	// Config bound but still offline: no injection.
	_, ok = svc.MCPInjection(ctx, wsID)
	require.False(t, ok)

	// Online + config: inject with the configured prompt fragment.
	svc.RegisterRunner(ctx, wsID)
	frag, ok := svc.MCPInjection(ctx, wsID)
	require.True(t, ok)
	require.Equal(t, "use local tools", frag)
}
