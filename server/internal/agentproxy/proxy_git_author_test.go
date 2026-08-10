package agentproxy

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// TestEffectiveGitUserID_PrefersInteractingUser is the core regression guard for
// the "团队版 git 作者署名串了" bug: GIT_AUTHOR_* was resolved from the session's
// frozen s.userID (locked to whoever *started* the session), so in a shared
// workspace every member's commits were misattributed to the starter — and the
// interactive chat path delivers with userID=0, dropping injection entirely.
//
// The fix resolves the git author from workspaces.current_session_user_id (the
// user who actually sent this turn, written by SetSessionUser before every
// Deliver) at spawn time. This test pins that: when the frozen start-user (A)
// differs from the current interacting user (B), the effective author is B.
func TestEffectiveGitUserID_PrefersInteractingUser(t *testing.T) {
	q := setupDispatchDB(t)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "git-author-test",
		Path:      t.TempDir(),
		Status:    "created",
		OwnerType: "org",
		OwnerID:   7,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	const userA, userB = int64(11), int64(22)
	if err := q.UpdateWorkspaceSessionUser(context.Background(), store.UpdateWorkspaceSessionUserParams{
		CurrentSessionUserID: sql.NullInt64{Int64: userB, Valid: true},
		ID:                   ws.ID,
	}); err != nil {
		t.Fatalf("UpdateWorkspaceSessionUser: %v", err)
	}

	// Session frozen to start-user A; current sender is B.
	s := &WorkspaceSession{q: q, workspaceID: ws.ID, userID: userA, ownerType: "org", ownerID: 7}
	if got := s.effectiveGitUserID(context.Background()); got != userB {
		t.Fatalf("effectiveGitUserID = %d, want %d (the interacting user, not frozen start-user %d)", got, userB, userA)
	}
}

// TestEffectiveGitUserID_FallbackChain covers the resolution order when no
// interacting user is recorded (autonomous / pre-first-send): frozen s.userID,
// then the workspace owner when owner_type=user, then 0 (no injection → git
// uses OS-global config).
func TestEffectiveGitUserID_FallbackChain(t *testing.T) {
	q := setupDispatchDB(t)

	// (a) current_session_user_id unset, s.userID>0 → s.userID.
	wsA, _ := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "fb-a", Path: t.TempDir(), Status: "created", OwnerType: "org", OwnerID: 7,
	})
	s := &WorkspaceSession{q: q, workspaceID: wsA.ID, userID: 33, ownerType: "org", ownerID: 7}
	if got := s.effectiveGitUserID(context.Background()); got != 33 {
		t.Fatalf("fallback to s.userID: got %d, want 33", got)
	}

	// (b) nothing set, user-owned workspace → owner id.
	wsB, _ := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "fb-b", Path: t.TempDir(), Status: "created", OwnerType: "user", OwnerID: 44,
	})
	s = &WorkspaceSession{q: q, workspaceID: wsB.ID, userID: 0, ownerType: "user", ownerID: 44}
	if got := s.effectiveGitUserID(context.Background()); got != 44 {
		t.Fatalf("fallback to user owner: got %d, want 44", got)
	}

	// (c) nothing set, org-owned (no single user owner) → 0 (skip injection).
	wsC, _ := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "fb-c", Path: t.TempDir(), Status: "created", OwnerType: "org", OwnerID: 7,
	})
	s = &WorkspaceSession{q: q, workspaceID: wsC.ID, userID: 0, ownerType: "org", ownerID: 7}
	if got := s.effectiveGitUserID(context.Background()); got != 0 {
		t.Fatalf("org-owned with no interacting user: got %d, want 0", got)
	}
}
