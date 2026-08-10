package service_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func setupKanbanTest(t *testing.T) (*service.KanbanService, *sql.DB, context.Context) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// SQLite ":memory:" databases are per-connection; pin to a single
	// connection so the schema applied below is visible to every later
	// query (including those routed through the service's wrapped *store.DB).
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	store.Migrate(db)
	q := store.New(db)
	activitySvc := service.NewIssueActivityService(q)
	svc := service.NewKanbanService(db, q, activitySvc, nil, nil)
	return svc, db, context.Background()
}

func createTestProject(t *testing.T, db *sql.DB, ctx context.Context) store.Project {
	t.Helper()
	q := store.New(db)
	p, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "test-project", OwnerType: "user"})
	require.NoError(t, err)
	return p
}

func TestUpdateColumnExtension_OpFieldsAndAutoAdvancePointer(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	project := createTestProject(t, db, ctx)
	col, err := svc.CreateColumn(ctx, project.ID, "实现", "implement")
	require.NoError(t, err)

	prim := "instruct"
	wtu := "需要写/改代码时"
	prompt := "实现本 issue 的需求改动"
	at := true
	_, err = svc.UpdateColumnExtension(ctx, col.ID, service.UpdateColumnExtensionInput{
		OpPrimitive: &prim, WhenToUse: &wtu, PhasePrompt: &prompt, AutoAdvance: &at,
	})
	require.NoError(t, err)

	got, err := svc.GetColumnOpFields(ctx, col.ID)
	require.NoError(t, err)
	assert.Equal(t, "instruct", got.OpPrimitive)
	require.NotNil(t, got.WhenToUse)
	assert.Equal(t, wtu, *got.WhenToUse)

	// AutoAdvance=nil on a later update must not clear the previously-set flag.
	wtu2 := "改动复杂时"
	updated, err := svc.UpdateColumnExtension(ctx, col.ID, service.UpdateColumnExtensionInput{WhenToUse: &wtu2})
	require.NoError(t, err)
	assert.Equal(t, int64(1), updated.AutoAdvance)

	// Invalid op_primitive is rejected.
	bad := "bogus"
	_, err = svc.UpdateColumnExtension(ctx, col.ID, service.UpdateColumnExtensionInput{OpPrimitive: &bad})
	assert.Error(t, err)

	// Empty when_to_use clears to NULL.
	empty := ""
	_, err = svc.UpdateColumnExtension(ctx, col.ID, service.UpdateColumnExtensionInput{WhenToUse: &empty})
	require.NoError(t, err)
	cleared, err := svc.GetColumnOpFields(ctx, col.ID)
	require.NoError(t, err)
	assert.Nil(t, cleared.WhenToUse)
}

func getColumnOrder(t *testing.T, db *sql.DB, ctx context.Context, projectID int64) []string {
	t.Helper()
	q := store.New(db)
	cols, err := q.ListColumnsByProject(ctx, projectID)
	require.NoError(t, err)
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
	}
	return names
}

func TestKanbanService_UpdateColumnPosition_InsertSort(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	q := store.New(db)
	project := createTestProject(t, db, ctx)

	// Create columns: A(0), B(1), C(2), D(3), E(4)
	for i, name := range []string{"A", "B", "C", "D", "E"} {
		_, err := q.CreateColumn(ctx, store.CreateColumnParams{
			ProjectID: project.ID, Name: name, Position: int64(i),
		})
		require.NoError(t, err)
	}

	cols, _ := q.ListColumnsByProject(ctx, project.ID)
	colByName := map[string]store.Column{}
	for _, c := range cols {
		colByName[c.Name] = c
	}

	tests := []struct {
		name        string
		columnName  string
		newPosition int64
		wantOrder   []string
	}{
		{
			name:        "move last to first",
			columnName:  "E",
			newPosition: 0,
			wantOrder:   []string{"E", "A", "B", "C", "D"},
		},
		{
			name:        "move first to last",
			columnName:  "E",
			newPosition: 4,
			wantOrder:   []string{"A", "B", "C", "D", "E"},
		},
		{
			name:        "move middle forward",
			columnName:  "D",
			newPosition: 1,
			wantOrder:   []string{"A", "D", "B", "C", "E"},
		},
		{
			name:        "move back to original",
			columnName:  "D",
			newPosition: 3,
			wantOrder:   []string{"A", "B", "C", "D", "E"},
		},
		{
			name:        "no-op same position",
			columnName:  "C",
			newPosition: 2,
			wantOrder:   []string{"A", "B", "C", "D", "E"},
		},
		{
			name:        "clamp to max",
			columnName:  "A",
			newPosition: 100,
			wantOrder:   []string{"B", "C", "D", "E", "A"},
		},
		{
			name:        "restore order",
			columnName:  "A",
			newPosition: 0,
			wantOrder:   []string{"A", "B", "C", "D", "E"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Re-read columns to get current state
			cols, _ := q.ListColumnsByProject(ctx, project.ID)
			var colID int64
			for _, c := range cols {
				if c.Name == tt.columnName {
					colID = c.ID
					break
				}
			}
			err := svc.UpdateColumnPosition(ctx, project.ID, colID, tt.newPosition)
			require.NoError(t, err)
			assert.Equal(t, tt.wantOrder, getColumnOrder(t, db, ctx, project.ID))
		})
	}
}

func TestKanbanService_CreateColumn_AutoPosition(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	project := createTestProject(t, db, ctx)

	col1, err := svc.CreateColumn(ctx, project.ID, "First", "")
	require.NoError(t, err)
	assert.Equal(t, int64(0), col1.Position)

	col2, err := svc.CreateColumn(ctx, project.ID, "Second", "")
	require.NoError(t, err)
	assert.Equal(t, int64(1), col2.Position)

	col3, err := svc.CreateColumn(ctx, project.ID, "Third", "")
	require.NoError(t, err)
	assert.Equal(t, int64(2), col3.Position)

	order := getColumnOrder(t, db, ctx, project.ID)
	assert.Equal(t, []string{"First", "Second", "Third"}, order)
}

func TestKanbanService_UpdateColumnPosition_NotFound(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	project := createTestProject(t, db, ctx)

	err := svc.UpdateColumnPosition(ctx, project.ID, 99999, 0)
	assert.Error(t, err)
}

// ─── Executable Epic (E1): hierarchy + exec fields + same-project gate ─────────

func TestKanbanService_ExecutableEpic(t *testing.T) {
	s, db, ctx := setupKanbanTest(t)
	p1 := createTestProject(t, db, ctx)
	c1, err := s.CreateColumn(ctx, p1.ID, "todo", "")
	require.NoError(t, err)

	// Create an Epic (issue_type='epic').
	epic, err := s.CreateIssue(ctx, c1.ID, "epic", "", 0, 0, "", "", "", 0,
		nil, "epic", 0, nil, nil, 0)
	require.NoError(t, err)
	assert.Equal(t, "epic", epic.IssueType)
	assert.False(t, epic.ParentIssueID.Valid)

	// Create a child in the same project with a wave -> attaches to the Epic.
	child, err := s.CreateIssue(ctx, c1.ID, "child", "", 0, 0, "", "", "", 0,
		&epic.ID, "task", 2, nil, nil, 0)
	require.NoError(t, err)
	require.True(t, child.ParentIssueID.Valid)
	assert.Equal(t, epic.ID, child.ParentIssueID.Int64)
	assert.Equal(t, int64(2), child.ExecWave)
	assert.Equal(t, "idle", child.ExecStatus)

	// Children list is wave-ordered.
	kids, err := s.ListChildIssues(ctx, epic.ID)
	require.NoError(t, err)
	require.Len(t, kids, 1)
	assert.Equal(t, child.ID, kids[0].ID)

	// Cross-project attach is rejected: parent in p1, child column in p2.
	p2, err := store.New(db).CreateProject(ctx, store.CreateProjectParams{Name: "test-project-2", OwnerType: "user"})
	require.NoError(t, err)
	c2, err := s.CreateColumn(ctx, p2.ID, "todo", "")
	require.NoError(t, err)
	_, err = s.CreateIssue(ctx, c2.ID, "foreign", "", 0, 0, "", "", "", 0,
		&epic.ID, "task", 0, nil, nil, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "same project")

	// SetIssueExecFields transitions exec_status and self-parent is rejected.
	updated, err := s.SetIssueExecFields(ctx, epic.ID, nil, "epic", 0, "running")
	require.NoError(t, err)
	assert.Equal(t, "running", updated.ExecStatus)

	_, err = s.SetIssueExecFields(ctx, epic.ID, &epic.ID, "epic", 0, "running")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "own parent")
}

func TestKanbanService_BatchCreateIssues_EpicFields(t *testing.T) {
	s, db, ctx := setupKanbanTest(t)
	p := createTestProject(t, db, ctx)
	_, err := s.CreateColumn(ctx, p.ID, "todo", "")
	require.NoError(t, err)

	// Seed the caller user so issues.created_by (FK to users) is satisfiable.
	_, err = db.ExecContext(ctx, `INSERT INTO users (id, username, password_hash) VALUES (7, 'u7', 'x')`)
	require.NoError(t, err)

	// Batch-create an Epic.
	res, err := s.BatchCreateIssues(ctx, p.ID, []service.BatchCreateIssuesTask{
		{Title: "epic", IssueType: "epic"},
	}, 7)
	require.NoError(t, err)
	require.Len(t, res.Issues, 1)
	epicID := res.Issues[0].ID
	epic, err := s.GetIssue(ctx, epicID)
	require.NoError(t, err)
	assert.Equal(t, "epic", epic.IssueType)
	// I1: batch path stamps the caller as created_by, and loadIssueDetail
	// surfaces it (regression guard for the lock-step field mapping).
	require.True(t, epic.CreatedBy.Valid)
	assert.Equal(t, int64(7), epic.CreatedBy.Int64)

	// Batch-create a child attached to the Epic with a wave.
	_, err = s.BatchCreateIssues(ctx, p.ID, []service.BatchCreateIssuesTask{
		{Title: "child", ParentIssueID: &epicID, ExecWave: 1},
	}, 0)
	require.NoError(t, err)
	kids, err := s.ListChildIssues(ctx, epicID)
	require.NoError(t, err)
	require.Len(t, kids, 1)
	assert.Equal(t, int64(1), kids[0].ExecWave)
	require.True(t, kids[0].ParentIssueID.Valid)
	assert.Equal(t, epicID, kids[0].ParentIssueID.Int64)

	// A cross-project parent fails the whole batch.
	p2, err := store.New(db).CreateProject(ctx, store.CreateProjectParams{Name: "p2", OwnerType: "user"})
	require.NoError(t, err)
	_, err = s.CreateColumn(ctx, p2.ID, "todo", "")
	require.NoError(t, err)
	_, err = s.BatchCreateIssues(ctx, p2.ID, []service.BatchCreateIssuesTask{
		{Title: "foreign", ParentIssueID: &epicID},
	}, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "same project")

	// Invalid issue_type is rejected.
	_, err = s.BatchCreateIssues(ctx, p.ID, []service.BatchCreateIssuesTask{
		{Title: "bad", IssueType: "bogus"},
	}, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "issue_type")
}

func TestKanbanService_IssueTypeAutoDerivedFromChildren(t *testing.T) {
	s, db, ctx := setupKanbanTest(t)
	p := createTestProject(t, db, ctx)
	c, err := s.CreateColumn(ctx, p.ID, "todo", "")
	require.NoError(t, err)

	// A plain issue (no type chosen) starts as a task.
	a, err := s.CreateIssue(ctx, c.ID, "A", "", 0, 0, "", "", "", 0, nil, "", 0, nil, nil, 0)
	require.NoError(t, err)
	assert.Equal(t, "task", a.IssueType)

	// Adding a child auto-promotes A to an Epic.
	b, err := s.CreateIssue(ctx, c.ID, "B", "", 0, 0, "", "", "", 0, &a.ID, "", 0, nil, nil, 0)
	require.NoError(t, err)
	aDetail, err := s.GetIssue(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, "epic", aDetail.IssueType, "parent auto-promoted to epic on child add")

	// Deleting the last child auto-demotes A back to a task.
	require.NoError(t, s.DeleteIssue(ctx, b.ID))
	aDetail, err = s.GetIssue(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, "task", aDetail.IssueType, "parent auto-demoted to task when childless")

	// Re-parenting also keeps both ends in sync: attach C under A again via exec-fields.
	cc, err := s.CreateIssue(ctx, c.ID, "C", "", 0, 0, "", "", "", 0, nil, "", 0, nil, nil, 0)
	require.NoError(t, err)
	_, err = s.SetIssueExecFields(ctx, cc.ID, &a.ID, "", 0, "idle")
	require.NoError(t, err)
	aDetail, err = s.GetIssue(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, "epic", aDetail.IssueType, "parent re-promoted on reparent via exec-fields")
}

func TestKanbanService_TwoLevelHierarchyEnforced(t *testing.T) {
	s, db, ctx := setupKanbanTest(t)
	p := createTestProject(t, db, ctx)
	c, err := s.CreateColumn(ctx, p.ID, "todo", "")
	require.NoError(t, err)

	// A has child B -> A is a top-level Epic (level 1), B is level 2.
	a, err := s.CreateIssue(ctx, c.ID, "A", "", 0, 0, "", "", "", 0, nil, "", 0, nil, nil, 0)
	require.NoError(t, err)
	b, err := s.CreateIssue(ctx, c.ID, "B", "", 0, 0, "", "", "", 0, &a.ID, "", 0, nil, nil, 0)
	require.NoError(t, err)
	x, err := s.CreateIssue(ctx, c.ID, "X", "", 0, 0, "", "", "", 0, nil, "", 0, nil, nil, 0)
	require.NoError(t, err)

	// Cannot make A (which has children) a child of X.
	_, err = s.SetIssueExecFields(ctx, a.ID, &x.ID, "", 0, "idle")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "two levels")

	// Cannot make X a child of B (B is itself a child -> not top-level).
	_, err = s.SetIssueExecFields(ctx, x.ID, &b.ID, "", 0, "idle")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "two levels")

	// Cannot CREATE a child under B (B not top-level).
	_, err = s.CreateIssue(ctx, c.ID, "Y", "", 0, 0, "", "", "", 0, &b.ID, "", 0, nil, nil, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "two levels")

	// X under A is allowed (A top-level, X has no children).
	_, err = s.SetIssueExecFields(ctx, x.ID, &a.ID, "", 0, "idle")
	require.NoError(t, err)
}

func TestKanbanService_SetIssueExecFields_RejectsParentCycle(t *testing.T) {
	s, db, ctx := setupKanbanTest(t)
	p := createTestProject(t, db, ctx)
	c, err := s.CreateColumn(ctx, p.ID, "todo", "")
	require.NoError(t, err)

	a, err := s.CreateIssue(ctx, c.ID, "A", "", 0, 0, "", "", "", 0, nil, "epic", 0, nil, nil, 0)
	require.NoError(t, err)
	// B is a child of A.
	b, err := s.CreateIssue(ctx, c.ID, "B", "", 0, 0, "", "", "", 0, &a.ID, "task", 0, nil, nil, 0)
	require.NoError(t, err)

	// Making A a child of B would form a cycle A->B->A — rejected.
	_, err = s.SetIssueExecFields(ctx, a.ID, &b.ID, "epic", 0, "idle")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
}

// ─── Task 8: CreateIssue with arrays + SetAssignees / SetLabels ────────────────

func TestKanbanService_CreateIssue_WithArrays(t *testing.T) {
	s, db, ctx := setupKanbanTest(t)
	owner := kanbanCreateUser(t, db, "owner-cii")
	org := kanbanCreateOrg(t, db, "o-cii", owner)
	proj := kanbanCreateProject(t, db, "org", org, "p-cii")
	col := kanbanCreateColumn(t, db, proj, "todo")
	lbl := kanbanCreateLabel(t, db, proj, owner, "bug", "#d73a4a")

	d, err := s.CreateIssue(ctx, col, "title", "", 0, 0, "", "", "", 0,
		nil, "", 0,
		[]int64{owner}, []int64{lbl}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Assignees) != 1 || d.Assignees[0].ID != owner {
		t.Errorf("assignees: %+v", d.Assignees)
	}
	if len(d.Labels) != 1 || d.Labels[0].ID != lbl {
		t.Errorf("labels: %+v", d.Labels)
	}
}

func TestKanbanService_SetAssignees_OutOfOrgRejected(t *testing.T) {
	s, db, ctx := setupKanbanTest(t)
	owner := kanbanCreateUser(t, db, "owner-saoor")
	outsider := kanbanCreateUser(t, db, "outsider-saoor")
	org := kanbanCreateOrg(t, db, "o-saoor", owner)
	proj := kanbanCreateProject(t, db, "org", org, "p-saoor")
	col := kanbanCreateColumn(t, db, proj, "todo")
	issue := kanbanCreateIssue(t, db, col, "x")

	_, err := s.SetAssignees(ctx, issue, []int64{outsider}, owner)
	if !errors.Is(err, service.ErrInvalidAssignee) {
		t.Errorf("got %v, want ErrInvalidAssignee", err)
	}
}

func TestKanbanService_SetLabels_CrossProjectRejected(t *testing.T) {
	s, db, ctx := setupKanbanTest(t)
	owner := kanbanCreateUser(t, db, "owner-slcp")
	projA := kanbanCreateProject(t, db, "user", owner, "a-slcp")
	projB := kanbanCreateProject(t, db, "user", owner, "b-slcp")
	colA := kanbanCreateColumn(t, db, projA, "todo")
	issueA := kanbanCreateIssue(t, db, colA, "x")
	labelB := kanbanCreateLabel(t, db, projB, owner, "x", "#000000")

	_, err := s.SetLabels(ctx, issueA, []int64{labelB})
	if !errors.Is(err, service.ErrInvalidLabel) {
		t.Errorf("got %v, want ErrInvalidLabel", err)
	}
}

func TestKanbanService_SetAssignees_TooMany(t *testing.T) {
	s, db, ctx := setupKanbanTest(t)
	owner := kanbanCreateUser(t, db, "o-sat")
	proj := kanbanCreateProject(t, db, "user", owner, "p-sat")
	col := kanbanCreateColumn(t, db, proj, "todo")
	issue := kanbanCreateIssue(t, db, col, "x")

	ids := make([]int64, 101)
	for i := range ids {
		ids[i] = owner
	}
	_, err := s.SetAssignees(ctx, issue, ids, owner)
	if !errors.Is(err, service.ErrTooMany) {
		t.Errorf("got %v, want ErrTooMany", err)
	}
}

func TestKanbanService_UpdateColumnExtension(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	uid := kanbanCreateUser(t, db, "tester-uce")
	pid := kanbanCreateProject(t, db, "user", uid, "p-uce")
	cid := kanbanCreateColumn(t, db, pid, "implement")

	rev := "claude-reviewer"
	prompt := "Implement spec; tests must pass."
	at := true
	col, err := svc.UpdateColumnExtension(ctx, cid, service.UpdateColumnExtensionInput{
		ReviewerAgent: &rev,
		PhasePrompt:   &prompt,
		AutoAdvance:   &at,
	})
	require.NoError(t, err)
	assert.Equal(t, "claude-reviewer", col.ReviewerAgent.String)
	assert.Equal(t, "Implement spec; tests must pass.", col.PhasePrompt.String)
	assert.Equal(t, int64(1), col.AutoAdvance)
}

func kanbanCreateSpec(t *testing.T, db *sql.DB, ownerType string, ownerID int64, name string) int64 {
	t.Helper()
	_, _ = ownerType, ownerID // harness_specs is a global library (no owner)
	res, err := db.Exec(
		`INSERT INTO harness_specs (name, category, severity, config) `+
			`VALUES (?, 'quality', 'warning', '{}')`,
		name,
	)
	if err != nil {
		t.Fatalf("insert spec: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last id: %v", err)
	}
	return id
}

func TestKanbanService_ReplaceColumnGateSpecs(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	uid := kanbanCreateUser(t, db, "tester-rcgs")
	pid := kanbanCreateProject(t, db, "user", uid, "p-rcgs")
	cid := kanbanCreateColumn(t, db, pid, "review")
	sid1 := kanbanCreateSpec(t, db, "user", uid, "lint-rcgs")
	sid2 := kanbanCreateSpec(t, db, "user", uid, "test-rcgs")

	got, err := svc.ReplaceColumnGateSpecs(ctx, cid, []service.GateSpecBinding{{SpecID: sid1}, {SpecID: sid2}})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, sid1, got[0].SpecID)
	assert.Equal(t, int64(0), got[0].Position)
	assert.Equal(t, "if_routed", got[0].Applicability, "blank applicability defaults to if_routed")
	assert.Equal(t, sid2, got[1].SpecID)
	assert.Equal(t, int64(1), got[1].Position)

	// Replace with reversed order + sid2 bound as an 'always' floor.
	got2, err := svc.ReplaceColumnGateSpecs(ctx, cid, []service.GateSpecBinding{{SpecID: sid2, Applicability: "always"}, {SpecID: sid1}})
	require.NoError(t, err)
	require.Len(t, got2, 2)
	assert.Equal(t, sid2, got2[0].SpecID)
	assert.Equal(t, "always", got2[0].Applicability, "floor applicability persists across re-bind")
	assert.Equal(t, sid1, got2[1].SpecID)
}

// TestKanbanService_GetIssue_RetainsExternalFields guards every
// external_* column propagating from `SELECT i.*` (GetIssueDetail) through
// loadIssueDetail's IssueDetail{Issue: store.Issue{...}} literal. The
// literal-omission failure mode is silent: response.go reads
// `d.ExternalSource`, the Go struct field exists (embedded store.Issue),
// the zero value compiles cleanly, and the SPA quietly never sees the
// external linkage or v1.1 comments-snapshot until someone notices the
// missing UI. v1.1 #r1 hit it; this test pins down all 7 fields so the
// next added column has to follow suit.
func TestKanbanService_GetIssue_RetainsExternalFields(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	uid := kanbanCreateUser(t, db, "tester-extfields")
	pid := kanbanCreateProject(t, db, "user", uid, "p-extfields")
	cid := kanbanCreateColumn(t, db, pid, "todo-extfields")
	iid := kanbanCreateIssue(t, db, cid, "task-extfields")

	// Seed every external_* column directly — bypasses the import path
	// so the test stays focused on the literal-propagation contract
	// rather than the upstream HTTP roundtrip.
	snapshotJSON := `[{"id":"c1","author":"alice","body":"upstream reply","created_at":"2026-05-13T01:00:00Z","url":"https://github.com/x/y/issues/1#issuecomment-c1"}]`
	_, err := db.ExecContext(ctx, `
		UPDATE issues
		SET external_source = ?,
			external_id = ?,
			external_url = ?,
			external_snapshot_at = CURRENT_TIMESTAMP,
			external_comments_snapshot = ?,
			external_comments_snapshot_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		"github", "acme/foo#1", "https://github.com/acme/foo/issues/1", snapshotJSON, iid)
	require.NoError(t, err)

	detail, err := svc.GetIssue(ctx, iid)
	require.NoError(t, err)
	assert.True(t, detail.ExternalSource.Valid && detail.ExternalSource.String == "github", "ExternalSource lost in literal")
	assert.True(t, detail.ExternalID.Valid && detail.ExternalID.String == "acme/foo#1", "ExternalID lost in literal")
	assert.True(t, detail.ExternalUrl.Valid && detail.ExternalUrl.String == "https://github.com/acme/foo/issues/1", "ExternalUrl lost in literal")
	assert.True(t, detail.ExternalSnapshotAt.Valid, "ExternalSnapshotAt lost in literal")
	assert.True(t, detail.ExternalCommentsSnapshot.Valid && detail.ExternalCommentsSnapshot.String == snapshotJSON, "ExternalCommentsSnapshot lost in literal")
	assert.True(t, detail.ExternalCommentsSnapshotAt.Valid, "ExternalCommentsSnapshotAt lost in literal")
}

// TestKanbanService_GetIssue_RetainsGoalCondition guards the same literal-
// omission failure mode as _RetainsExternalFields, scoped to the
// goal_condition column. The bug: when goal_condition was added to issues,
// loadIssueDetail's IssueDetail{Issue: store.Issue{...}} literal was not
// updated to copy `row.GoalCondition` from GetIssueDetailRow, so GET
// /issues/:id always returned `"goal_condition":""` regardless of the DB
// value. The SPA's IssueGoalConditionPanel then rendered "未设置" on
// every page open. Caught 2026-05-15 via curl + sqlite check (DB had the
// value, response was empty string). Fix: one-line addition. This test
// pins it.
func TestKanbanService_GetIssue_RetainsGoalCondition(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	uid := kanbanCreateUser(t, db, "tester-goalcond")
	pid := kanbanCreateProject(t, db, "user", uid, "p-goalcond")
	cid := kanbanCreateColumn(t, db, pid, "todo-goalcond")
	iid := kanbanCreateIssue(t, db, cid, "task-goalcond")

	// Set goal_condition directly in DB — bypasses UpdateIssue so the
	// test stays focused on the read-path literal propagation rather
	// than the write path.
	const want = "所有单元测试通过且 make test 退出 0"
	_, err := db.ExecContext(ctx,
		`UPDATE issues SET goal_condition = ? WHERE id = ?`, want, iid)
	require.NoError(t, err)

	detail, err := svc.GetIssue(ctx, iid)
	require.NoError(t, err)
	assert.Equal(t, want, detail.GoalCondition,
		"GoalCondition must survive loadIssueDetail's struct-literal copy; if you just added a new issues column, make sure it's also in the IssueDetail{Issue: ...} literal at loadIssueDetail in kanban.go")
}

func TestKanbanService_ReplaceColumnGateSpecs_AcceptsGlobalSpec(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)
	uid := kanbanCreateUser(t, db, "tester-rcgs-global")
	pid := kanbanCreateProject(t, db, "user", uid, "p-rcgs-global")
	cid := kanbanCreateColumn(t, db, pid, "review-global")

	// harness_specs is a single global library usable by any project.
	_ = kanbanCreateUser(t, db, "other-rcgs-global")
	res, err := db.ExecContext(ctx,
		`INSERT INTO harness_specs (name, category, severity, config) `+
			`VALUES (?, 'quality', 'warning', '{}')`,
		"global-conv-commits",
	)
	require.NoError(t, err)
	globalSid, err := res.LastInsertId()
	require.NoError(t, err)

	got, err := svc.ReplaceColumnGateSpecs(ctx, cid, []service.GateSpecBinding{{SpecID: globalSid}})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, globalSid, got[0].SpecID)
}

// TestKanbanService_CreateProjectWithDefaults_SeedsFiveColumns guards the §9
// seed (AI-native board execution design, phase 1b; extended 2026-06-07 with a
// human-review lane): CreateProjectWithDefaults must seed exactly the five
// op-primitive columns — 待办(none) / 实现(instruct) / AI 审查(instruct) /
// 人工审查(none) / 完成(complete) — with their op_instruction (phase_prompt) and
// when_to_use routing hints. op_primitive / when_to_use live only as migrate.go
// ALTERs (outside the schema CREATE block), so they are read back via raw SQL.
func TestKanbanService_CreateProjectWithDefaults_SeedsFiveColumns(t *testing.T) {
	svc, db, ctx := setupKanbanTest(t)

	proj, err := svc.CreateProjectWithDefaults(ctx, "ai-native-seed", "desc", "user", 0)
	require.NoError(t, err)

	type colRow struct {
		name        string
		position    int64
		lifecycle   string
		primitive   string
		whenToUse   sql.NullString
		instruction sql.NullString
	}
	rows, err := db.QueryContext(ctx,
		`SELECT name, position, lifecycle_mapping, op_primitive, when_to_use, phase_prompt
		   FROM columns WHERE project_id = ? ORDER BY position`, proj.ID)
	require.NoError(t, err)
	defer rows.Close()
	var got []colRow
	for rows.Next() {
		var c colRow
		require.NoError(t, rows.Scan(&c.name, &c.position, &c.lifecycle, &c.primitive, &c.whenToUse, &c.instruction))
		got = append(got, c)
	}
	require.NoError(t, rows.Err())

	require.Len(t, got, 5, "§9 seed must create exactly 5 columns")

	// 待办 — none, no instruction / routing hint.
	assert.Equal(t, "待办", got[0].name)
	assert.EqualValues(t, 0, got[0].position)
	assert.Equal(t, "none", got[0].primitive)
	assert.False(t, got[0].instruction.Valid, "待办 has no op_instruction")
	assert.False(t, got[0].whenToUse.Valid, "待办 has no when_to_use")

	// 实现 — instruct.
	assert.Equal(t, "实现", got[1].name)
	assert.EqualValues(t, 1, got[1].position)
	assert.Equal(t, "instruct", got[1].primitive)
	assert.Equal(t, "实现本 issue 的任务分析、需求改动或问题修复", got[1].instruction.String)
	assert.Equal(t, "需要开始、实现、解决任务时", got[1].whenToUse.String)

	// AI 审查 — instruct (renamed from 审查 on 2026-06-07 to disambiguate from 人工审查).
	assert.Equal(t, "AI 审查", got[2].name)
	assert.EqualValues(t, 2, got[2].position)
	assert.Equal(t, "instruct", got[2].primitive)
	assert.Equal(t, "对本 issue 的产出做严格审查，列出问题并就地修复", got[2].instruction.String)
	assert.Equal(t, "产出有复杂度、值得严格审查时", got[2].whenToUse.String)

	// 人工审查 — none parking lane (无指令): no op_instruction, but a when_to_use hint.
	// Because it has a when_to_use it appears in the AI board menu (so the agent can
	// escalate into it via advance_issue); a human parks issues needing
	// judgment/approval/trade-off here.
	assert.Equal(t, "人工审查", got[3].name)
	assert.EqualValues(t, 3, got[3].position)
	assert.Equal(t, "none", got[3].primitive)
	assert.False(t, got[3].instruction.Valid, "人工审查 has no op_instruction")
	assert.Equal(t, "需要人工判断、利益相关方批准或需要权衡多个方案的问题", got[3].whenToUse.String)

	// 完成 — complete, no instruction / routing hint.
	assert.Equal(t, "完成", got[4].name)
	assert.EqualValues(t, 4, got[4].position)
	assert.Equal(t, "complete", got[4].primitive)
	assert.False(t, got[4].instruction.Valid, "完成 has no op_instruction")
	assert.False(t, got[4].whenToUse.Valid, "完成 has no when_to_use")

	// lifecycle_mapping stays consistent with op_primitive (per the §17
	// lifecycle→primitive backfill) so the legacy lifecycle-group sidebar and
	// any residual lifecycle consumers keep working for newly seeded projects.
	// 人工审查 maps to "" deliberately — a new op-primitive lane, not a legacy
	// lifecycle stage (no auto-move, no lifecycle sidebar group).
	assert.Equal(t, "created", got[0].lifecycle)
	assert.Equal(t, "implement", got[1].lifecycle)
	assert.Equal(t, "implement-review", got[2].lifecycle)
	assert.Equal(t, "", got[3].lifecycle)
	assert.Equal(t, "completed", got[4].lifecycle)
}

// TestKanbanService_CreateProject_NameUniquePerOwner guards the multi-tenant
// contract: project names are unique per owner (idx_projects_owner_name_unique),
// NOT globally. One owner having a "牛牛助手" project must not block a different
// owner from creating their own — the regression behind the team-edition
// assistant failing with "项目名字已经存在" for a project the user can't see
// (it lived under another owner). Same owner + same name still conflicts.
func TestKanbanService_CreateProject_NameUniquePerOwner(t *testing.T) {
	svc, _, ctx := setupKanbanTest(t)

	// Owner A creates the project.
	_, err := svc.CreateProjectWithDefaults(ctx, "牛牛助手", "desc", "user", 1)
	require.NoError(t, err)

	// A different owner (here an org) reuses the same name — must succeed.
	_, err = svc.CreateProjectWithDefaults(ctx, "牛牛助手", "desc", "org", 7)
	require.NoError(t, err, "a different owner must be able to reuse the name")

	// Same owner + same name still conflicts.
	_, err = svc.CreateProjectWithDefaults(ctx, "牛牛助手", "desc", "user", 1)
	require.ErrorIs(t, err, service.ErrProjectNameExists, "same owner duplicate must be rejected")

	// CreateProjectWithColumns (blueprint path) shares the same owner-scoped
	// contract: a different owner reuses the name; same owner conflicts.
	seeds := []service.ColumnSeed{{Name: "待办", Position: 0, OpPrimitive: "none"}}
	_, err = svc.CreateProjectWithColumns(ctx, "牛牛助手", "desc", "user", 2, seeds)
	require.NoError(t, err, "blueprint path: a different owner must be able to reuse the name")
	_, err = svc.CreateProjectWithColumns(ctx, "牛牛助手", "desc", "user", 2, seeds)
	require.ErrorIs(t, err, service.ErrProjectNameExists, "blueprint path: same owner duplicate must be rejected")
}

// Fixture helpers (kanbanCreate* series) live in kanban_fixtures_test.go
// and are shared across kanban_test.go, run_test.go, and future service tests.
