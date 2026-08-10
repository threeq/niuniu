package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// ---- fixtures -------------------------------------------------------------

func newCleanupSvc(t *testing.T) (*WorkspaceCleanupService, *store.Queries, *sql.DB, context.Context) {
	t.Helper()
	db := setupMemoryTestDB(t)
	q := store.New(db)
	svc := &WorkspaceCleanupService{q: q, db: db, now: time.Now}
	return svc, q, db, context.Background()
}

func mkProject(t *testing.T, q *store.Queries, ctx context.Context, name string) int64 {
	t.Helper()
	p, err := q.CreateProject(ctx, store.CreateProjectParams{Name: name, OwnerType: "user"})
	require.NoError(t, err)
	return p.ID
}

func mkColumn(t *testing.T, db *sql.DB, projectID int64) int64 {
	t.Helper()
	res, err := db.Exec("INSERT INTO columns (project_id, name, position) VALUES (?, 'todo', 0)", projectID)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

func mkIssue(t *testing.T, db *sql.DB, columnID int64, lifecycle, exec string) int64 {
	t.Helper()
	res, err := db.Exec(
		"INSERT INTO issues (column_id, title, lifecycle_status, exec_status) VALUES (?, 't', ?, ?)",
		columnID, lifecycle, exec)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// mkWorkspace inserts a workspace bound to issueID with an explicit updated_at
// (the coarse activity fallback) and agent status.
func mkWorkspace(t *testing.T, db *sql.DB, issueID int64, agentStatus string, updatedAt time.Time) int64 {
	t.Helper()
	res, err := db.Exec(
		"INSERT INTO workspaces (issue_id, path, status, agent_status, session_status, updated_at) VALUES (?, '/tmp/ws', 'created', ?, 'idle', ?)",
		issueID, agentStatus, updatedAt.UTC())
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

func setLastActivity(t *testing.T, db *sql.DB, wsID int64, at time.Time) {
	t.Helper()
	_, err := db.Exec("INSERT INTO workspace_stats (workspace_id, last_activity_at) VALUES (?, ?)", wsID, at.UTC())
	require.NoError(t, err)
}

// stubDeletes wires the destructive callbacks to real DB row deletes so tests can
// assert the workspace/issue rows are gone, plus a controllable change gate.
func stubDeletes(svc *WorkspaceCleanupService, q *store.Queries, changed map[int64]bool) {
	svc.deleteWorkspace = func(ctx context.Context, id int64) error { return q.DeleteWorkspace(ctx, id) }
	svc.deleteIssue = func(ctx context.Context, id int64) error { return q.DeleteIssue(ctx, id) }
	svc.hasChanges = func(ctx context.Context, id int64) (bool, error) { return changed[id], nil }
}

// ---- policy get/set -------------------------------------------------------

func TestCleanupPolicy_DefaultOffAndToggle(t *testing.T) {
	svc, q, _, ctx := newCleanupSvc(t)
	pid := mkProject(t, q, ctx, "p")

	pol, err := svc.GetPolicy(ctx, pid)
	require.NoError(t, err)
	require.False(t, pol.Enabled, "new project defaults OFF")
	require.Equal(t, 0, pol.InactiveDays)
	require.False(t, pol.Active(), "inert by default")
	// schema default targets both categories
	require.ElementsMatch(t, []string{CleanupCategoryCompleted, CleanupCategoryNotStarted}, pol.Statuses)

	require.NoError(t, svc.SetPolicy(ctx, pid, CleanupPolicy{
		Enabled: true, InactiveDays: 7, Statuses: []string{CleanupCategoryCompleted},
	}))
	pol, err = svc.GetPolicy(ctx, pid)
	require.NoError(t, err)
	require.True(t, pol.Enabled)
	require.Equal(t, 7, pol.InactiveDays)
	require.Equal(t, []string{CleanupCategoryCompleted}, pol.Statuses)
	require.True(t, pol.Active())
}

func TestCleanupPolicy_SetNormalizesStatusesAndClampsDays(t *testing.T) {
	svc, q, _, ctx := newCleanupSvc(t)
	pid := mkProject(t, q, ctx, "p")

	require.NoError(t, svc.SetPolicy(ctx, pid, CleanupPolicy{
		Enabled:      true,
		InactiveDays: -5, // clamped to 0
		Statuses:     []string{"completed", "bogus", " not_started ", "completed"},
	}))
	pol, err := svc.GetPolicy(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, 0, pol.InactiveDays, "negative days clamped")
	require.Equal(t, []string{CleanupCategoryCompleted, CleanupCategoryNotStarted}, pol.Statuses,
		"unknown dropped, whitespace trimmed, duplicates removed")
	require.False(t, pol.Active(), "0 days is inert even when enabled")
}

// ---- pure classification / evaluation -------------------------------------

func TestClassifyIssue(t *testing.T) {
	require.Equal(t, CleanupCategoryCompleted, classifyIssue("completed", "idle"))
	require.Equal(t, CleanupCategoryCompleted, classifyIssue("implement", "done"))
	require.Equal(t, CleanupCategoryCompleted, classifyIssue("implement", "abandoned"))
	require.Equal(t, CleanupCategoryNotStarted, classifyIssue("created", "idle"))
	// in-progress states must be kept
	require.Equal(t, "", classifyIssue("implement", "running"))
	require.Equal(t, "", classifyIssue("created", "running"))
	require.Equal(t, "", classifyIssue("spec", "idle"))
	require.Equal(t, "", classifyIssue("implement", "failed"))
}

func row(ws, iss int64, lifecycle, exec, agent string, updated time.Time, last *time.Time) store.ListProjectWorkspacesForCleanupRow {
	r := store.ListProjectWorkspacesForCleanupRow{
		WorkspaceID:     ws,
		IssueID:         iss,
		AgentStatus:     sql.NullString{String: agent, Valid: true},
		SessionStatus:   sql.NullString{String: "idle", Valid: true},
		UpdatedAt:       updated,
		LifecycleStatus: lifecycle,
		ExecStatus:      exec,
	}
	if last != nil {
		r.LastActivityAt = sql.NullTime{Time: *last, Valid: true}
	}
	return r
}

func TestEvaluateCandidate(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	pol := CleanupPolicy{Enabled: true, InactiveDays: 7, Statuses: DefaultCleanupStatuses}
	old := now.Add(-10 * 24 * time.Hour)
	recent := now.Add(-2 * 24 * time.Hour)

	// completed + idle for 10 days -> candidate
	c, ok := evaluateCandidate(row(1, 11, "completed", "idle", "idle", old, nil), pol, now)
	require.True(t, ok)
	require.Equal(t, CleanupCategoryCompleted, c.Category)
	require.Equal(t, int64(11), c.IssueID)

	// not-started for 10 days -> candidate
	_, ok = evaluateCandidate(row(2, 12, "created", "idle", "idle", old, nil), pol, now)
	require.True(t, ok)

	// completed but active within window -> kept
	_, ok = evaluateCandidate(row(3, 13, "completed", "idle", "idle", recent, nil), pol, now)
	require.False(t, ok, "recent activity keeps it")

	// in-progress issue old -> kept
	_, ok = evaluateCandidate(row(4, 14, "implement", "running", "idle", old, nil), pol, now)
	require.False(t, ok)

	// running agent, otherwise a candidate -> kept
	_, ok = evaluateCandidate(row(5, 15, "completed", "idle", "running", old, nil), pol, now)
	require.False(t, ok, "never delete a running workspace")

	// last_activity_at overrides updated_at: updated_at old but stats recent -> kept
	_, ok = evaluateCandidate(row(6, 16, "completed", "idle", "idle", old, &recent), pol, now)
	require.False(t, ok)

	// category not targeted by policy -> kept
	polCompletedOnly := CleanupPolicy{Enabled: true, InactiveDays: 7, Statuses: []string{CleanupCategoryCompleted}}
	_, ok = evaluateCandidate(row(7, 17, "created", "idle", "idle", old, nil), polCompletedOnly, now)
	require.False(t, ok, "not_started excluded when policy targets completed only")
}

// ---- end-to-end sweep -----------------------------------------------------

func TestSweepProject_DeletesQualifyingWorkspacesAndIssues(t *testing.T) {
	svc, q, db, ctx := newCleanupSvc(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	stubDeletes(svc, q, nil)

	pid := mkProject(t, q, ctx, "p")
	col := mkColumn(t, db, pid)
	old := now.Add(-30 * 24 * time.Hour)
	recent := now.Add(-1 * 24 * time.Hour)

	// qualifying: completed + idle 30d
	doneIssue := mkIssue(t, db, col, "completed", "idle")
	doneWs := mkWorkspace(t, db, doneIssue, "idle", old)
	// qualifying: not-started 30d
	newIssue := mkIssue(t, db, col, "created", "idle")
	newWs := mkWorkspace(t, db, newIssue, "idle", old)
	// kept: in-progress
	wipIssue := mkIssue(t, db, col, "implement", "running")
	wipWs := mkWorkspace(t, db, wipIssue, "running", old)
	// kept: completed but recent activity
	freshIssue := mkIssue(t, db, col, "completed", "idle")
	freshWs := mkWorkspace(t, db, freshIssue, "idle", old)
	setLastActivity(t, db, freshWs, recent)

	require.NoError(t, svc.SetPolicy(ctx, pid, CleanupPolicy{
		Enabled: true, InactiveDays: 7, Statuses: DefaultCleanupStatuses,
	}))

	res, err := svc.SweepProject(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, 4, res.Scanned)
	require.ElementsMatch(t, []int64{doneWs, newWs}, res.Deleted)
	require.Equal(t, 0, res.Errors)

	// Deleted workspaces and their issues are gone.
	requireWorkspaceGone(t, q, ctx, doneWs)
	requireWorkspaceGone(t, q, ctx, newWs)
	requireIssueGone(t, q, ctx, doneIssue)
	requireIssueGone(t, q, ctx, newIssue)
	// Kept ones remain.
	requireWorkspaceExists(t, q, ctx, wipWs)
	requireWorkspaceExists(t, q, ctx, freshWs)
}

func TestSweepProject_SkipsWorkspacesWithUncommittedChanges(t *testing.T) {
	svc, q, db, ctx := newCleanupSvc(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	pid := mkProject(t, q, ctx, "p")
	col := mkColumn(t, db, pid)
	old := now.Add(-30 * 24 * time.Hour)
	iss := mkIssue(t, db, col, "completed", "idle")
	ws := mkWorkspace(t, db, iss, "idle", old)
	stubDeletes(svc, q, map[int64]bool{ws: true}) // has uncommitted changes

	require.NoError(t, svc.SetPolicy(ctx, pid, CleanupPolicy{
		Enabled: true, InactiveDays: 7, Statuses: DefaultCleanupStatuses,
	}))

	res, err := svc.SweepProject(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, 1, res.Scanned)
	require.Empty(t, res.Deleted)
	require.Equal(t, 1, res.SkippedChanges)
	requireWorkspaceExists(t, q, ctx, ws)
}

func TestSweepProject_InertPolicyIsNoop(t *testing.T) {
	svc, q, db, ctx := newCleanupSvc(t)
	stubDeletes(svc, q, nil)
	pid := mkProject(t, q, ctx, "p")
	col := mkColumn(t, db, pid)
	iss := mkIssue(t, db, col, "completed", "idle")
	ws := mkWorkspace(t, db, iss, "idle", time.Now().Add(-100*24*time.Hour))

	// disabled (default) -> nothing scanned, nothing deleted
	res, err := svc.SweepProject(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, 0, res.Scanned)
	require.Empty(t, res.Deleted)
	requireWorkspaceExists(t, q, ctx, ws)
}

func TestRunDueCleanup_OnlyEnabledProjects(t *testing.T) {
	svc, q, db, ctx := newCleanupSvc(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	stubDeletes(svc, q, nil)
	old := now.Add(-30 * 24 * time.Hour)

	// enabled project with a qualifying workspace
	on := mkProject(t, q, ctx, "on")
	onCol := mkColumn(t, db, on)
	onIssue := mkIssue(t, db, onCol, "completed", "idle")
	onWs := mkWorkspace(t, db, onIssue, "idle", old)
	require.NoError(t, svc.SetPolicy(ctx, on, CleanupPolicy{Enabled: true, InactiveDays: 7, Statuses: DefaultCleanupStatuses}))

	// disabled project with an equally-qualifying workspace
	off := mkProject(t, q, ctx, "off")
	offCol := mkColumn(t, db, off)
	offIssue := mkIssue(t, db, offCol, "completed", "idle")
	offWs := mkWorkspace(t, db, offIssue, "idle", old)

	svc.runDueCleanup(ctx)

	requireWorkspaceGone(t, q, ctx, onWs)
	requireWorkspaceExists(t, q, ctx, offWs)
}

// ---- assertion helpers ----------------------------------------------------

func requireWorkspaceGone(t *testing.T, q *store.Queries, ctx context.Context, id int64) {
	t.Helper()
	_, err := q.GetWorkspace(ctx, id)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func requireWorkspaceExists(t *testing.T, q *store.Queries, ctx context.Context, id int64) {
	t.Helper()
	_, err := q.GetWorkspace(ctx, id)
	require.NoError(t, err)
}

func requireIssueGone(t *testing.T, q *store.Queries, ctx context.Context, id int64) {
	t.Helper()
	_, err := q.GetIssue(ctx, id)
	require.ErrorIs(t, err, sql.ErrNoRows)
}
