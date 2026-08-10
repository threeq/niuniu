package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestRouteInProject_Generalization proves the W2 core: routing creates the
// issue+workspace in the *given* project (not a hard-wired assistant project),
// and an explicit active-issue hint continues that same task instead of forking
// a new one. This is the isolation + continue guarantee behind the IM inbound
// loop, exercised end to end against real Kanban + Workspace services.
func TestRouteInProject_Generalization(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)
	q := store.New(db)

	dataDir := t.TempDir()
	cfg := &config.WorkspaceConfig{BaseDir: filepath.Join(dataDir, "workspaces")}
	require.NoError(t, os.MkdirAll(cfg.BaseDir, 0o755))
	wsSvc := NewWorkspaceService(q, db, cfg, dataDir, nil, nil)
	kanban := NewKanbanService(db, q, NewIssueActivityService(q), nil, nil)
	disp := NewAssistantDispatchService(kanban, wsSvc, q, nil) // nil classifier

	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: 1}

	// An arbitrary (non-assistant) project with a single lane.
	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "营销项目", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	_, err = q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "待办", Position: 0})
	require.NoError(t, err)

	// New task lands in THIS project's first column with a backing workspace.
	target, err := disp.RouteInProject(ctx, owner, proj.ID, "做一个营销方案", RouteHint{})
	require.NoError(t, err)
	require.True(t, target.IsNew)
	require.Equal(t, proj.ID, target.ProjectID)
	require.NotZero(t, target.WorkspaceID)

	issue, err := q.GetIssue(ctx, target.IssueID)
	require.NoError(t, err)
	col, err := q.GetColumn(ctx, issue.ColumnID)
	require.NoError(t, err)
	require.Equal(t, proj.ID, col.ProjectID, "issue must be parked in the routed project")

	// A follow-up naming the active issue continues it — no new task.
	cont, err := disp.RouteInProject(ctx, owner, proj.ID, "再补一段预算", RouteHint{ActiveIssueID: target.IssueID})
	require.NoError(t, err)
	require.False(t, cont.IsNew)
	require.Equal(t, target.IssueID, cont.IssueID)
	require.Equal(t, target.WorkspaceID, cont.WorkspaceID)

	// ForceNew always forks a fresh task even with an active hint.
	forced, err := disp.RouteInProject(ctx, owner, proj.ID, "另起一个任务", RouteHint{ActiveIssueID: target.IssueID, ForceNew: true})
	require.NoError(t, err)
	require.True(t, forced.IsNew)
	require.NotEqual(t, target.IssueID, forced.IssueID)
}
