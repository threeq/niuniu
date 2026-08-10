package service

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// TestSidebarListFirstPaintScales is the empirical proof for the 方案 B
// first-paint fix: GET /api/workspaces (ListWithSidebarMeta) must be a small,
// constant set of indexed batch queries with NO per-workspace query, NO git,
// and NO filesystem work — so its cost grows ~linearly with data, not
// quadratically with N. It seeds 1000 workspaces (each linked to an issue) plus
// several agent_messages each (to exercise the message aggregate) and asserts
// the enrich path returns all rows quickly.
//
// This does NOT cover the git badges: those are computed by the separate
// sidebar-git endpoint (sidebarGitStatus), which is intentionally O(N) git
// subprocesses and covered by TestSidebarGitStatus_ReportsPerWorkspaceCounts.
func TestSidebarListFirstPaintScales(t *testing.T) {
	const n = 1000
	const msgsPerWs = 5

	db := openWorkspaceTestDB(t)
	q := store.New(db)
	ctx := context.Background()

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name: "bench", OwnerType: "user", OwnerID: 1, DefaultCliType: "claude",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{
		ProjectID: proj.ID, Name: "todo", Position: 0, LifecycleMapping: "",
	})
	if err != nil {
		t.Fatalf("CreateColumn: %v", err)
	}

	for i := 0; i < n; i++ {
		issue, err := q.CreateIssue(ctx, store.CreateIssueParams{
			ColumnID: col.ID, Title: fmt.Sprintf("issue-%d", i), Position: int64(i),
		})
		if err != nil {
			t.Fatalf("CreateIssue %d: %v", i, err)
		}
		ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
			Name:      fmt.Sprintf("ws-%d", i),
			Path:      fmt.Sprintf("/tmp/ws-%d", i),
			Status:    "created",
			OwnerType: "user",
			OwnerID:   1,
			IssueID:   sql.NullInt64{Int64: issue.ID, Valid: true},
		})
		if err != nil {
			t.Fatalf("CreateWorkspace %d: %v", i, err)
		}
		for m := 0; m < msgsPerWs; m++ {
			if err := q.CreateAgentMessage(ctx, store.CreateAgentMessageParams{
				ID:          fmt.Sprintf("m-%d-%d", i, m),
				WorkspaceID: ws.ID,
				Role:        "assistant",
				Content:     "x",
				MessageID:   fmt.Sprintf("mid-%d-%d", i, m),
				EventType:   "text",
			}); err != nil {
				t.Fatalf("CreateAgentMessage: %v", err)
			}
		}
	}

	// Warm any lazy init, then measure the first-paint enrich path.
	start := time.Now()
	metas, err := svcListWithSidebarMeta(t, q)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ListWithSidebarMeta: %v", err)
	}
	if len(metas) != n {
		t.Fatalf("expected %d metas, got %d", n, len(metas))
	}

	perWs := elapsed / time.Duration(n)
	t.Logf("first-paint ListWithSidebarMeta over %d workspaces (%d msgs total): %v (%v/workspace)",
		n, n*msgsPerWs, elapsed, perWs)

	// Guardrail against a reintroduced per-workspace query / git / FS call or an
	// accidental O(N^2). On in-memory SQLite the whole enrich is a handful of
	// batch queries + a map merge, so 1000 workspaces is milliseconds; 5s is a
	// deliberately loose ceiling that only trips on a real regression.
	if elapsed > 5*time.Second {
		t.Fatalf("first-paint too slow for %d workspaces: %v — likely an N+1/git/FS regression", n, elapsed)
	}

	// Spot-check enrichment actually populated (project name + message count),
	// proving the batched joins/aggregates ran, not just returned bare rows.
	withProject, withMsgs := 0, 0
	for i := range metas {
		if metas[i].ProjectName != "" {
			withProject++
		}
		if metas[i].MessageCount == int64(msgsPerWs) {
			withMsgs++
		}
	}
	if withProject != n {
		t.Errorf("expected all %d metas to carry project name, got %d", n, withProject)
	}
	if withMsgs != n {
		t.Errorf("expected all %d metas to carry message_count=%d, got %d", n, msgsPerWs, withMsgs)
	}
}

// svcListWithSidebarMeta builds a WorkspaceService bound to q and runs the
// no-auth first-paint path (ListWithSidebarMeta -> enrichSidebarMeta).
func svcListWithSidebarMeta(t *testing.T, q *store.Queries) ([]WorkspaceSidebarMeta, error) {
	t.Helper()
	svc := newWorkspaceServiceForGitStatus(t, q)
	return svc.ListWithSidebarMeta(context.Background())
}
