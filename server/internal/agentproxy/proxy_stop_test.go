package agentproxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

// TestStop_BroadcastsAgentDoneNotify covers the "X button no UI response" bug:
// killing the claude process via Stop() bypasses SendLoop's natural exit path,
// so SendLoop's `notify.TopicWorkspace, agent_done` broadcast never fires and
// the React Query `['workspace', id]` cache stays stale until manual refresh.
//
// Stop() must mirror that broadcast so the notification dispatcher's
// INVALIDATION_MAP['workspace'] entry invalidates the workspace detail query
// and the chat-input agent_status badge updates immediately.
func TestStop_BroadcastsAgentDoneNotify(t *testing.T) {
	q := setupDispatchDB(t)

	// Real workspace row required — Stop() reads it via GetWorkspace to
	// populate Owner{Type,ID} on the broadcast envelope.
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "stop-broadcast-test",
		Path:      t.TempDir(),
		Status:    "running",
		OwnerType: "user",
		OwnerID:   42,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	// Verify the create return carries the owner; helps localize DB-schema
	// vs notify-broadcast bugs if this assertion ever fires.
	if ws.OwnerType != "user" || ws.OwnerID != 42 {
		t.Fatalf("workspace create-return owner = (%q,%d), want (user,42)", ws.OwnerType, ws.OwnerID)
	}
	if got, err := q.GetWorkspace(context.Background(), ws.ID); err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	} else if got.OwnerType != "user" || got.OwnerID != 42 {
		t.Fatalf("GetWorkspace owner = (%q,%d), want (user,42)", got.OwnerType, got.OwnerID)
	}

	// Mark the workspace running so Stop() actually transitions it.
	if err := q.UpdateAgentStatus(context.Background(), store.UpdateAgentStatusParams{
		AgentPid:    sql.NullInt64{Valid: false},
		AgentStatus: sql.NullString{String: "running", Valid: true},
		ID:          ws.ID,
	}); err != nil {
		t.Fatalf("UpdateAgentStatus: %v", err)
	}

	hub := NewSessionHub()
	t.Cleanup(hub.Stop)
	notifyHub := notify.NewNotificationHub()
	t.Cleanup(notifyHub.Stop)

	// Two listeners verify both the BROADCAST and the OWNER FILTERING:
	//   - matching listener (userID=42) must receive the agent_done event
	//   - non-matching listener (userID=99) must NOT receive it
	// Notification.OwnerType / OwnerID are `json:"-"` (spec §5.9) so they
	// can't be asserted directly off the JSON wire — owner correctness is
	// proven indirectly via the hub's per-connection filtering.
	matching := notifyHub.Register("test-matching", 42, nil)
	other := notifyHub.Register("test-other", 99, nil)

	s := &WorkspaceSession{
		workspaceID:     ws.ID,
		topLevelAgentID: 100,
		q:               q,
		hub:             hub,
		notifyHub:       notifyHub,
		// inflight left nil; killProcess on a nil cmd is a no-op.
	}

	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// The hub fires Broadcast synchronously into the channel buffers, so the
	// envelope is already in `matching` by the time Stop() returns. Drain
	// until we either find the agent_done event or hit deadline.
	deadline := time.After(2 * time.Second)
	var seenAgentDone bool
	for !seenAgentDone {
		select {
		case raw, ok := <-matching:
			if !ok {
				t.Fatal("matching channel closed before agent_done arrived")
			}
			var got notify.Notification
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal notify: %v", err)
			}
			if got.Topic == notify.TopicPing {
				continue
			}
			if got.Topic == notify.TopicWorkspace && got.Action == "agent_done" {
				if got.ID != ws.ID {
					t.Errorf("notification ID = %d, want %d", got.ID, ws.ID)
				}
				seenAgentDone = true
			}
		case <-deadline:
			t.Fatal("Stop() did not broadcast topic=workspace action=agent_done within 2s")
		}
	}

	// Owner filtering: the non-matching listener must NOT have received the
	// agent_done event. Drain its channel non-blockingly and assert nothing
	// matches — pings and other globally-broadcast events are tolerated.
	for {
		select {
		case raw := <-other:
			var got notify.Notification
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal other-side notify: %v", err)
			}
			if got.Topic == notify.TopicWorkspace && got.Action == "agent_done" {
				t.Fatalf("non-owner listener (userID=99) received agent_done — owner filtering broken")
			}
		default:
			return // drained; success
		}
	}
}

// fakeMCPSessions is a stub MCPSessionManager that records calls and lets
// each test choose what token to return.
type fakeMCPSessions struct {
	createCalled  int
	createTokens  []string
	createWorkspaceIDs []int64
	revokeCalled  int
	tokenToReturn string
}

func (f *fakeMCPSessions) Create(_ context.Context, workspaceID int64, _ time.Duration) (string, error) {
	f.createCalled++
	f.createWorkspaceIDs = append(f.createWorkspaceIDs, workspaceID)
	tok := f.tokenToReturn
	if tok == "" {
		tok = "tok-stub"
	}
	f.createTokens = append(f.createTokens, tok)
	return tok, nil
}

func (f *fakeMCPSessions) Revoke(_ context.Context, _ int64) error {
	f.revokeCalled++
	return nil
}

// TestGetOrStartSession_IssuesTokenWhenUserIDIsZero is the regression test
// for the harness/scheduler MCP-token-missing bug:
//
// harness/pipeline.go:138/355 and scheduler/scheduler.go:353 both call
// `proxy.GetOrStartSession(ctx, workspaceID, 0)` — no logged-in user
// initiates the run, so userID is 0. The previous implementation gated
// `mcpSessions.Create` behind `if userID > 0`, so harness- and
// scheduler-started agents NEVER got an MCP token. .mcp.json was written
// without env.NIUNIU_MCP_TOKEN, every /mcp/* call returned 401, every
// blackboard / phase / inbox tool failed silently, and the harness run
// could not progress.
//
// Fix: token issuance is now decoupled from userID — the session manager,
// when wired, always issues a token. current_session_user_id update still
// gates on userID > 0 (because that field is per-user identity, not
// per-session auth).
func TestGetOrStartSession_IssuesTokenWhenUserIDIsZero(t *testing.T) {
	q := setupDispatchDB(t)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "harness-start-test",
		Path:      t.TempDir(),
		Status:    "created",
		OwnerType: "user",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	fake := &fakeMCPSessions{tokenToReturn: "tok-issued-for-userID-0"}
	p := NewAgentProxy(q, nil)
	t.Cleanup(p.Stop)
	p.SetMCPSessionService(fake)

	s, err := p.GetOrStartSession(context.Background(), ws.ID, 0)
	if err != nil {
		t.Fatalf("GetOrStartSession: %v", err)
	}

	if fake.createCalled != 1 {
		t.Fatalf("MCPSessionManager.Create called %d times, want 1 (token MUST be issued for harness-started sessions)", fake.createCalled)
	}
	if len(fake.createWorkspaceIDs) != 1 || fake.createWorkspaceIDs[0] != ws.ID {
		t.Errorf("Create workspaceID = %v, want [%d]", fake.createWorkspaceIDs, ws.ID)
	}
	if s.sessionToken != "tok-issued-for-userID-0" {
		t.Errorf("session.sessionToken = %q, want %q (token must reach the session struct so .mcp.json gets env.NIUNIU_MCP_TOKEN)", s.sessionToken, "tok-issued-for-userID-0")
	}

	// Sanity: current_session_user_id must remain unset for userID=0
	// (autonomous start — there's no real user to attribute the session to).
	got, err := q.GetWorkspace(context.Background(), ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if got.CurrentSessionUserID.Valid {
		t.Errorf("current_session_user_id should be NULL for userID=0 start, got Valid=true Int64=%d", got.CurrentSessionUserID.Int64)
	}
}

// TestGetOrStartSession_IssuesTokenAndSetsUserWhenUserIDProvided guards the
// chat-path regression (the working case): when a real user initiates a
// session, both the token AND the current_session_user_id must update.
func TestGetOrStartSession_IssuesTokenAndSetsUserWhenUserIDProvided(t *testing.T) {
	q := setupDispatchDB(t)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "chat-start-test",
		Path:      t.TempDir(),
		Status:    "created",
		OwnerType: "user",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	fake := &fakeMCPSessions{tokenToReturn: "tok-issued-for-user-7"}
	p := NewAgentProxy(q, nil)
	t.Cleanup(p.Stop)
	p.SetMCPSessionService(fake)

	s, err := p.GetOrStartSession(context.Background(), ws.ID, 7)
	if err != nil {
		t.Fatalf("GetOrStartSession: %v", err)
	}

	if fake.createCalled != 1 {
		t.Errorf("Create called %d, want 1", fake.createCalled)
	}
	if s.sessionToken != "tok-issued-for-user-7" {
		t.Errorf("sessionToken = %q, want issued token", s.sessionToken)
	}

	got, err := q.GetWorkspace(context.Background(), ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if !got.CurrentSessionUserID.Valid || got.CurrentSessionUserID.Int64 != 7 {
		t.Errorf("current_session_user_id = (Valid=%v, Int64=%d), want (true, 7)", got.CurrentSessionUserID.Valid, got.CurrentSessionUserID.Int64)
	}
}

// TestStop_TolerantOfNilNotifyHub guards against a regression where Stop()
// would panic on nil notifyHub (e.g. tests that don't wire the hub, or
// temporary workspaces). The previous implementation had no nil check on
// the new broadcast block; without `if s.notifyHub != nil` guarding it,
// every non-hub Stop() would crash.
func TestStop_TolerantOfNilNotifyHub(t *testing.T) {
	q := setupDispatchDB(t)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "stop-nil-hub-test",
		Path:      t.TempDir(),
		Status:    "running",
		OwnerType: "user",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	hub := NewSessionHub()
	t.Cleanup(hub.Stop)
	s := &WorkspaceSession{
		workspaceID: ws.ID,
		q:           q,
		hub:         hub,
		// notifyHub explicitly nil
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop with nil notifyHub: %v", err)
	}
}
