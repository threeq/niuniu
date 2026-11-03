package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// briefFixture seeds one project/column/issue and returns the store handles.
func briefFixture(t *testing.T) (*store.Queries, *store.DB, int64) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)
	q := store.New(db)

	ctx := context.Background()
	proj, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name: "项目", OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{
		ProjectID: proj.ID, Name: "待办", Position: 0,
	})
	require.NoError(t, err)
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{
		ColumnID: col.ID, Title: "做一个营销方案", Position: 0,
	})
	require.NoError(t, err)
	return q, store.Wrap(db), issue.ID
}

func TestTaskBriefWriter_AppendVersionMaterializesAcceptance(t *testing.T) {
	q, db, issueID := briefFixture(t)
	ctx := context.Background()

	// A manual execution item that must survive every Brief change.
	_, err := q.CreateChecklist(ctx, store.CreateChecklistParams{
		IssueID: issueID, Title: "manual task", IssueID_2: issueID,
	})
	require.NoError(t, err)

	writer := NewTaskBriefWriter(db)
	v1, err := writer.Append(ctx, issueID, intake.TaskBrief{
		Goal:       "ship it",
		Acceptance: []string{" tests pass ", "", "docs updated"},
		ScopeIn:    []string{" server "},
		ScopeOut:   []string{" mobile "},
		Delivery:   intake.DeliverySpec{SourceKind: "im_bot", ThreadExtID: "thread-1", ReplyMode: "thread"},
		Provenance: intake.Provenance{
			DecisionID: 42, PolicyVersion: 7, Strength: intake.StrengthExplicit,
			ActorExtID: "ou_1", RuleIDs: []int64{3, 5},
		},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, v1.Version)
	require.JSONEq(t, `["tests pass","docs updated"]`, v1.AcceptanceJson)
	require.JSONEq(t, `["server"]`, v1.ScopeInJson)
	require.Equal(t, sql.NullInt64{Int64: 42, Valid: true}, v1.IntakeDecisionID)

	wantGoal := "ship it\n\n验收标准（全部满足才算完成）：\n- tests pass\n- docs updated"
	issue, err := q.GetIssue(ctx, issueID)
	require.NoError(t, err)
	require.Equal(t, wantGoal, issue.GoalCondition)

	// Acceptance is materialized with kind='acceptance' and this version's id.
	acc, err := q.ListChecklistsByIssueAndKind(ctx, store.ListChecklistsByIssueAndKindParams{
		IssueID: issueID, Kind: "acceptance",
	})
	require.NoError(t, err)
	require.Len(t, acc, 2)
	for _, row := range acc {
		require.Equal(t, sql.NullInt64{Int64: v1.ID, Valid: true}, row.BriefID)
	}

	// Execution stats ignore acceptance rows.
	stats, err := q.GetChecklistStats(ctx, issueID)
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.Total)
	detail, err := q.GetIssueDetail(ctx, issueID)
	require.NoError(t, err)
	require.EqualValues(t, 1, detail.ChecklistTotal)

	// The read surface decodes the stored JSON into the stable contract.
	briefSvc := NewTaskBriefService(q, db)
	view, err := briefSvc.GetIssueBrief(ctx, issueID)
	require.NoError(t, err)
	require.NotNil(t, view.Brief)
	require.Equal(t, "ship it", view.Brief.Goal)
	require.Equal(t, []string{"tests pass", "docs updated"}, view.Brief.Acceptance)
	require.Equal(t, "im_bot", view.Brief.Delivery.SourceKind)
	require.Equal(t, "thread-1", view.Brief.Delivery.ThreadExtID)
	require.Equal(t, "thread", view.Brief.Delivery.ReplyMode)
	require.Equal(t, int64(42), view.Brief.Provenance.DecisionID)
	require.Equal(t, int64(7), view.Brief.Provenance.PolicyVersion)
	require.Equal(t, "explicit", view.Brief.Provenance.Strength)
	require.Equal(t, "ou_1", view.Brief.Provenance.ActorExtID)
	require.Equal(t, []int64{3, 5}, view.Brief.Provenance.RuleIDs)
	require.Equal(t, wantGoal, view.GoalCondition)
	require.Len(t, view.Versions, 1)

	// A scope expansion appends v2: history stays, acceptance is refreshed,
	// manual execution items are untouched.
	v2, err := writer.Append(ctx, issueID, intake.TaskBrief{
		Goal:       "ship it wider",
		Acceptance: []string{"all green", "load test"},
		ScopeIn:    []string{"server", "mobile"},
		Provenance: intake.Provenance{DecisionID: 43, PolicyVersion: 8, Strength: intake.StrengthInferred},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, v2.Version)

	latest, err := q.GetLatestTaskBrief(ctx, issueID)
	require.NoError(t, err)
	require.EqualValues(t, 2, latest.Version)
	require.Equal(t, "ship it wider", latest.Goal)

	acc, err = q.ListChecklistsByIssueAndKind(ctx, store.ListChecklistsByIssueAndKindParams{
		IssueID: issueID, Kind: "acceptance",
	})
	require.NoError(t, err)
	require.Len(t, acc, 2, "old acceptance rows are replaced")
	for _, row := range acc {
		require.Equal(t, sql.NullInt64{Int64: v2.ID, Valid: true}, row.BriefID)
	}
	tasks, err := q.ListChecklistsByIssueAndKind(ctx, store.ListChecklistsByIssueAndKindParams{
		IssueID: issueID, Kind: "task",
	})
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "manual task", tasks[0].Title)

	versions, err := q.ListTaskBriefs(ctx, issueID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	require.EqualValues(t, 2, versions[0].Version)

	// v1 remains queryable after the append.
	old, err := q.GetTaskBriefByVersion(ctx, store.GetTaskBriefByVersionParams{
		IssueID: issueID, Version: 1,
	})
	require.NoError(t, err)
	require.Equal(t, "ship it", old.Goal)
}

func TestIssueBrief_NoBriefFallsBackToGoalCondition(t *testing.T) {
	q, db, issueID := briefFixture(t)
	ctx := context.Background()

	require.NoError(t, q.UpdateIssueGoalCondition(ctx, store.UpdateIssueGoalConditionParams{
		GoalCondition: "legacy goal", ID: issueID,
	}))

	view, err := NewTaskBriefService(q, db).GetIssueBrief(ctx, issueID)
	require.NoError(t, err)
	require.Nil(t, view.Brief)
	require.Equal(t, "legacy goal", view.GoalCondition)
	require.Empty(t, view.Versions)
}

func TestTaskBriefWriter_RejectsInvalidInput(t *testing.T) {
	q, db, issueID := briefFixture(t)
	ctx := context.Background()

	_, err := NewTaskBriefWriter(db).Append(ctx, 0, intake.TaskBrief{Goal: "x"})
	require.Error(t, err)

	_, err = (&TaskBriefWriter{}).Append(ctx, issueID, intake.TaskBrief{Goal: "x"})
	require.Error(t, err)

	// Nothing was written by the rejected calls.
	_, err = q.GetLatestTaskBrief(ctx, issueID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}
