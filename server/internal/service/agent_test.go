package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// TestBroadcastWorkspaceStatusChanged_Enrichment is the architect-flagged
// missing emit-site test (M-4): it goes through the same code path the
// review-timer and OnAgentEvent("running") callsites use, captures the wire
// payload, and asserts the v2 architectural contract — that Extra carries
// should_alert_user_ids alongside the existing status field, and OwnerType /
// OwnerID are populated for the hub's owner-fanout filter.
//
// The test wires a real *notify.NotificationHub with a connection registered
// against the workspace's owning org so the broadcast is actually delivered
// (rather than filtered out at the hub). It then reads the JSON frame off
// the connection's channel and asserts the resulting Notification shape.
func TestBroadcastWorkspaceStatusChanged_Enrichment(t *testing.T) {
	db := openWorkspaceTestDB(t)
	q := store.New(db)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}

	// Seed: alice (creator), bob + carol (issue assignees).
	mustExec(`INSERT INTO users (id, username, password_hash, display_name) VALUES
		(1, 'alice', 'x', 'Alice'),
		(2, 'bob',   'x', 'Bob'),
		(3, 'carol', 'x', 'Carol')`)
	mustExec(`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (10, 'p', 'org', 5)`)
	mustExec(`INSERT INTO columns (id, project_id, name, position) VALUES (20, 10, 'todo', 0)`)
	mustExec(`INSERT INTO issues (id, column_id, title, position) VALUES (30, 20, 't', 0)`)
	mustExec(`INSERT INTO issue_assignees (issue_id, user_id) VALUES (30, 2), (30, 3)`)

	// Workspace W101: creator alice (1), linked to issue 30 → assignees bob (2), carol (3).
	// Expected should_alert_user_ids: {1, 2, 3} (deduped).
	mustExec(`INSERT INTO workspaces (id, issue_id, name, path, owner_type, owner_id, created_by) VALUES
		(101, 30, 'w1', '/w1', 'org', 5, 1)`)

	hub := notify.NewNotificationHub()
	t.Cleanup(hub.Stop)

	// Register a connection that has org 5 in its accessible-owner set, so
	// the broadcast is not filtered out by canSeeOwner.
	ch := hub.Register("test-window", /*userID=*/ 1, /*orgIDs=*/ []int64{5})
	defer hub.Unregister("test-window")

	mgr := NewAgentManager(q, nil)
	mgr.SetNotifyHub(hub)

	mgr.broadcastWorkspaceStatusChanged(context.Background(), 101, "needs_review")

	// Read the broadcast frame. The hub's send is non-blocking with buffer
	// of 64; a 1s timeout is plenty under any sane scheduling.
	select {
	case data, ok := <-ch:
		if !ok {
			t.Fatal("notify channel closed before broadcast arrived")
		}
		var got notify.Notification
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal frame: %v\nraw=%s", err, string(data))
		}
		if got.Topic != notify.TopicWorkspace {
			t.Errorf("Topic: want %q, got %q", notify.TopicWorkspace, got.Topic)
		}
		if got.Action != "status_changed" {
			t.Errorf("Action: want status_changed, got %q", got.Action)
		}
		if got.ID != 101 {
			t.Errorf("ID: want 101, got %d", got.ID)
		}
		// Owner metadata is stripped from the wire (json:"-") on purpose; the
		// hub's owner gate already used it to decide delivery, so the absence
		// on the wire is by design (notify/types.go:17-18). What we *can*
		// assert from the wire is Extra.
		extra, ok := got.Extra.(map[string]any)
		if !ok {
			t.Fatalf("Extra: want map[string]any, got %T (%v)", got.Extra, got.Extra)
		}
		if extra["status"] != "needs_review" {
			t.Errorf("Extra.status: want needs_review, got %v", extra["status"])
		}
		raw, ok := extra["should_alert_user_ids"].([]any)
		if !ok {
			t.Fatalf("Extra.should_alert_user_ids: want []any, got %T (%v)", extra["should_alert_user_ids"], extra["should_alert_user_ids"])
		}
		ids := make([]int64, 0, len(raw))
		for _, v := range raw {
			// JSON numbers decode into float64.
			f, ok := v.(float64)
			if !ok {
				t.Errorf("alertable id: want float64, got %T", v)
				continue
			}
			ids = append(ids, int64(f))
		}
		if !setEq(ids, []int64{1, 2, 3}) {
			t.Errorf("should_alert_user_ids: want {1,2,3} (creator + 2 assignees deduped), got %v", ids)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broadcast")
	}
}

// TestBroadcastWorkspaceStatusChanged_NilHubIsNoop confirms the early-return
// when the hub is unset (test-mode wiring, embedded mode without notify) —
// the helper must not panic on a missing hub and must not query the DB.
func TestBroadcastWorkspaceStatusChanged_NilHubIsNoop(t *testing.T) {
	db := openWorkspaceTestDB(t)
	q := store.New(db)

	mgr := NewAgentManager(q, nil)
	// No SetNotifyHub call — m.notifyHub stays nil.

	// Must not panic; must not even touch the DB (workspace 9999 doesn't exist).
	mgr.broadcastWorkspaceStatusChanged(context.Background(), 9999, "needs_review")
}
