package agentproxy

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// TestSetSessionUser_RecordsInteractingUser guards the "team-edition external
// data source 401" regression: interactive chat sends reach the proxy through
// Deliver(userID=0), so the workspace's current_session_user_id stayed NULL.
// On an org-owned workspace MCPTokenAuth then can't resolve auth_user_id and
// every credential-scoped MCP tool (external-proxy / data-proxy) 401s.
// SetSessionUser must persist the authenticated caller so the next MCP call
// resolves a real identity.
func TestSetSessionUser_RecordsInteractingUser(t *testing.T) {
	q := setupDispatchDB(t)
	p := NewAgentProxy(q, &config.Config{})
	t.Cleanup(p.Stop)

	// Org-owned workspace with NO created_by — the exact shape (team edition,
	// no recoverable creator) where the created_by fallback also fails, so the
	// session-user identity is the only thing that can resolve auth_user_id.
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "session-user-test",
		Path:      t.TempDir(),
		Status:    "created",
		OwnerType: "org",
		OwnerID:   7,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// Precondition: a freshly chat-started session has no session user.
	if got, _ := q.GetWorkspace(context.Background(), ws.ID); got.CurrentSessionUserID.Valid {
		t.Fatalf("precondition: current_session_user_id should start NULL, got %d", got.CurrentSessionUserID.Int64)
	}

	const userID = int64(42)
	p.SetSessionUser(context.Background(), ws.ID, userID)

	got, err := q.GetWorkspace(context.Background(), ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if !got.CurrentSessionUserID.Valid || got.CurrentSessionUserID.Int64 != userID {
		t.Fatalf("current_session_user_id = (valid=%v, %d), want (true, %d)",
			got.CurrentSessionUserID.Valid, got.CurrentSessionUserID.Int64, userID)
	}
}

// TestSetSessionUser_IgnoresAutonomousCaller ensures autonomous callers
// (scheduler / autohost / gate paths pass userID=0) never clobber a real
// identity back to NULL.
func TestSetSessionUser_IgnoresAutonomousCaller(t *testing.T) {
	q := setupDispatchDB(t)
	p := NewAgentProxy(q, &config.Config{})
	t.Cleanup(p.Stop)

	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "session-user-noop-test",
		Path:      t.TempDir(),
		Status:    "created",
		OwnerType: "org",
		OwnerID:   7,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// Seed a real interacting user, then simulate an autonomous send.
	p.SetSessionUser(context.Background(), ws.ID, 42)
	p.SetSessionUser(context.Background(), ws.ID, 0)

	got, err := q.GetWorkspace(context.Background(), ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if !got.CurrentSessionUserID.Valid || got.CurrentSessionUserID.Int64 != 42 {
		t.Fatalf("current_session_user_id = (valid=%v, %d), want (true, 42) — userID=0 must be a no-op",
			got.CurrentSessionUserID.Valid, got.CurrentSessionUserID.Int64)
	}
}
