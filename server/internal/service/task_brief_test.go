package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// briefFixture seeds one project/column/issue and returns the store handles.
func briefFixture(t *testing.T) (*store.Queries, *store.DB, int64, int64) {
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
	return q, store.Wrap(db), issue.ID, proj.ID
}

func TestTaskBriefWriter_AppendVersionMaterializesAcceptance(t *testing.T) {
	q, db, issueID, _ := briefFixture(t)
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
	}, 0)
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
	}, 0)
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
	q, db, issueID, _ := briefFixture(t)
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
	q, db, issueID, _ := briefFixture(t)
	ctx := context.Background()

	_, err := NewTaskBriefWriter(db).Append(ctx, 0, intake.TaskBrief{Goal: "x"}, 0)
	require.Error(t, err)

	_, err = (&TaskBriefWriter{}).Append(ctx, issueID, intake.TaskBrief{Goal: "x"}, 0)
	require.Error(t, err)

	// Nothing was written by the rejected calls.
	_, err = q.GetLatestTaskBrief(ctx, issueID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// The provenance projection reads whichever casing the adapter's raw payload
// uses (IM marshals Go field names, GitHub the REST JSON field names).
func TestProvenancePayloadProjection(t *testing.T) {
	require.Equal(t, "登录接口 500", provenancePayloadText(json.RawMessage(`{"Text":"登录接口 500"}`)))
	require.Equal(t, "please fix", provenancePayloadText(json.RawMessage(`{"body":"please fix"}`)))
	require.Empty(t, provenancePayloadText(json.RawMessage(`{}`)))
	require.Empty(t, provenancePayloadText(nil))

	require.Equal(t, "https://github.com/acme/foo/pull/1#c",
		provenancePayloadURL(json.RawMessage(`{"html_url":"https://github.com/acme/foo/pull/1#c"}`)))
	require.Equal(t, "https://example.test/x",
		provenancePayloadURL(json.RawMessage(`{"SourceURL":"https://example.test/x"}`)))
	require.Empty(t, provenancePayloadURL(nil))
}

func TestTaskBriefRead_ProvenanceEventAndCreatedBy(t *testing.T) {
	q, db, issueID, projectID := briefFixture(t)
	ctx := context.Background()

	rule, err := q.CreateIntakeRule(ctx, store.CreateIntakeRuleParams{
		ProjectID: projectID, SourceKind: "im_bot", Name: "线上缺陷自动接单",
		Instruction: "accept production bugs", ConditionsJson: `{}`, Priority: 1, Enabled: 1,
	})
	require.NoError(t, err)

	event, err := q.CreateIntakeEvent(ctx, store.CreateIntakeEventParams{
		ProjectID: projectID, SourceKind: "im_bot", SourceRef: "im_group",
		EventExtID: "msg-1", EventKind: "message", ActorExtID: "ou_1",
		ActorName: "Alice", ThreadExtID: "thread-1",
		PayloadJson: `{"Text":"登录接口又 500 了，麻烦看下","ActorName":"Alice"}`,
	})
	require.NoError(t, err)
	decision, err := q.CreateIntakeDecision(ctx, store.CreateIntakeDecisionParams{
		EventID: event.ID, ProjectID: projectID, ModeAtDecision: "auto",
		Strength: "explicit", Decision: "accept", Executed: 1,
		ReasonCode: "matched", MatchedRuleIdsJson: `[` + itoa64(rule.ID) + `]`,
		PolicyVersion: 3, IssueID: sql.NullInt64{Int64: issueID, Valid: true},
		WouldDoJson: `{}`,
	})
	require.NoError(t, err)

	_, err = NewTaskBriefWriter(db).Append(ctx, issueID, intake.TaskBrief{
		Goal:       "修复登录接口 500",
		Acceptance: []string{"复现并定位", "修复并补测试"},
		Delivery:   intake.DeliverySpec{SourceKind: "im_bot", ThreadExtID: "thread-1", ReplyMode: "thread"},
		Provenance: intake.Provenance{
			DecisionID: decision.ID, PolicyVersion: 3, Strength: intake.StrengthExplicit,
			ActorExtID: "ou_1", RuleIDs: []int64{rule.ID},
		},
	}, 7)
	require.NoError(t, err)

	view, err := NewTaskBriefService(q, db).GetIssueBrief(ctx, issueID)
	require.NoError(t, err)
	require.NotNil(t, view.Brief)
	require.NotNil(t, view.Brief.CreatedBy)
	require.EqualValues(t, 7, *view.Brief.CreatedBy)

	require.NotNil(t, view.ProvenanceEvent)
	require.Equal(t, "im_bot", view.ProvenanceEvent.SourceKind)
	require.Equal(t, "Alice", view.ProvenanceEvent.ActorName)
	require.Equal(t, "thread-1", view.ProvenanceEvent.ThreadExtID)
	require.Equal(t, []string{"线上缺陷自动接单"}, view.ProvenanceEvent.RuleNames)
	require.Contains(t, string(view.ProvenanceEvent.Payload), "登录接口又 500 了")
	// Source-neutral projections so the UI never has to know adapter shapes.
	require.Equal(t, "登录接口又 500 了，麻烦看下", view.ProvenanceEvent.Text)
	require.Empty(t, view.ProvenanceEvent.URL)

	// A brief with no decision has no event rather than an empty one.
	_, db2, issue2, _ := briefFixture(t)
	_, err = NewTaskBriefWriter(db2).Append(ctx, issue2, intake.TaskBrief{Goal: "manual"}, 7)
	require.NoError(t, err)
	view2, err := NewTaskBriefService(store.New(db2), db2).GetIssueBrief(ctx, issue2)
	require.NoError(t, err)
	require.Nil(t, view2.ProvenanceEvent)
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}
