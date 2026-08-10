package service_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGoalSuggester drives the create-path auto-kickoff gate in tests.
type fakeGoalSuggester struct {
	assess *service.GoalAssessment
	err    error
}

func (f fakeGoalSuggester) Suggest(_ context.Context, _ int64, _, _ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.assess != nil {
		return f.assess.GoalCondition, nil
	}
	return "", nil
}

func (f fakeGoalSuggester) Classify(_ context.Context, _ int64, _, _ string) (*service.GoalAssessment, error) {
	return f.assess, f.err
}

// setColumnOp sets a column's op_primitive (+ optional phase_prompt = op_instruction)
// via raw SQL: stage 1a added op_primitive only via migration, so it is not in the
// sqlc Column struct / CreateColumn. SQLite accepts the `?` placeholders directly.
func setColumnOp(t *testing.T, e *epicTestEnv, columnID int64, primitive, instruction string) {
	t.Helper()
	_, err := e.db.ExecContext(e.ctx,
		`UPDATE columns SET op_primitive = ?, phase_prompt = ? WHERE id = ?`,
		primitive, instruction, columnID)
	require.NoError(t, err)
}

// makeColumn adds a column to a project and returns its id.
func makeColumn(t *testing.T, e *epicTestEnv, projectID int64, name string, position int64) int64 {
	t.Helper()
	col, err := e.q.CreateColumn(e.ctx, store.CreateColumnParams{ProjectID: projectID, Name: name, Position: position})
	require.NoError(t, err)
	return col.ID
}

// makeProjectWithColumns creates a project plus a `none` 待办 column and an
// `instruct` 实现 column (with an op_instruction). Returns the ids.
func makeProjectWithColumns(t *testing.T, e *epicTestEnv) (projectID, backlogID, instructID int64) {
	t.Helper()
	p, err := e.q.CreateProject(e.ctx, store.CreateProjectParams{Name: "adv-proj", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	backlogID = makeColumn(t, e, p.ID, "待办", 0)
	setColumnOp(t, e, backlogID, "none", "")
	instructID = makeColumn(t, e, p.ID, "实现", 1)
	setColumnOp(t, e, instructID, "instruct", "实现本 issue 的需求改动")
	return p.ID, backlogID, instructID
}

// makeStandaloneIssue creates a standalone (non-epic, no parent) issue in a column.
func makeStandaloneIssue(t *testing.T, e *epicTestEnv, columnID int64, title, desc string) int64 {
	t.Helper()
	d, err := e.kanban.CreateIssue(e.ctx, columnID, title, desc, 0, 0, "", "", "", 0, nil, "", 0, nil, nil, 0)
	require.NoError(t, err)
	return d.ID
}

func issueColumn(t *testing.T, e *epicTestEnv, issueID int64) int64 {
	t.Helper()
	iss, err := e.q.GetIssue(e.ctx, issueID)
	require.NoError(t, err)
	return iss.ColumnID
}

func TestAdvanceIssue_InstructColumn_CreatesWorkspaceAndInstructs(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	_, backlogID, instructID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, backlogID, "加登录", "用 JWT")

	res, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID:      issueID,
		ToColumn:     "实现", // resolve by name
		Reason:       "开始实现",
		CallerUserID: 1,
	})
	require.NoError(t, err)

	// Card moved to the instruct column.
	assert.Equal(t, instructID, res.ColumnID)
	assert.Equal(t, instructID, issueColumn(t, e, issueID))
	assert.Equal(t, "instruct", res.OpPrimitive)
	assert.Equal(t, "实现", res.ColumnName)

	// A workspace was created (not reused) and instructed.
	assert.False(t, res.Reused)
	assert.NotZero(t, res.WorkspaceID)
	assert.True(t, res.Instructed)
	assert.Equal(t, 1, e.fake.count(), "one workspace created for the issue")

	// Standalone issue entering an instruct lane runs in autohost.
	assert.Equal(t, "autohost", e.fake.env["NIUNIU_PERMISSION_MODE"])

	// The instruct message carries the 【column】op_instruction header + issue context.
	require.Len(t, proxy.kickoffs, 1)
	msg := proxy.kickoffs[0]
	assert.Contains(t, msg, "【实现】")
	assert.Contains(t, msg, "实现本 issue 的需求改动")
	assert.Contains(t, msg, "加登录")
	assert.Contains(t, msg, "用 JWT")
}

// --- OnIssueCreated (create-into-column orchestration, spec §13 stage 8 / §3) ---

func TestOnIssueCreated_InstructColumn_AutoOrchestrates(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	e.svc.SetAsyncRunner(func(f func()) { f() }) // run classify+kickoff inline
	e.svc.SetGoalSuggester(fakeGoalSuggester{assess: &service.GoalAssessment{Actionable: true, Reason: "具体", GoalCondition: "make test exits 0"}})
	_, _, instructID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, instructID, "加登录", "用 JWT")

	res, err := e.svc.OnIssueCreated(e.ctx, issueID, 1)
	require.NoError(t, err)

	assert.Equal(t, instructID, res.ColumnID)
	assert.Equal(t, "instruct", res.OpPrimitive)
	assert.NotZero(t, res.WorkspaceID)
	assert.Equal(t, 1, e.fake.count(), "one workspace auto-created on create-into-instruct")

	// Actionable -> agent kicked off: autohost enabled + instruct message sent.
	assert.Equal(t, "autohost", e.fake.env["NIUNIU_PERMISSION_MODE"])
	require.Len(t, proxy.kickoffs, 1)
	assert.Contains(t, proxy.kickoffs[0], "【实现】")
	assert.Contains(t, proxy.kickoffs[0], "加登录")

	// No routing step is recorded for a creation (creation is not an advance_issue move).
	assert.Zero(t, res.RouteSteps)
}

func TestOnIssueCreated_NoneColumn_NoOp(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	_, backlogID, _ := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, backlogID, "记一笔", "")

	res, err := e.svc.OnIssueCreated(e.ctx, issueID, 1)
	require.NoError(t, err)

	assert.Equal(t, backlogID, res.ColumnID)
	assert.Equal(t, "none", res.OpPrimitive)
	assert.False(t, res.Instructed)
	assert.Zero(t, res.WorkspaceID)
	assert.Equal(t, 0, e.fake.count(), "no workspace for a create into a none column")
	assert.Empty(t, proxy.kickoffs)
}

func TestOnIssueCreated_InstructColumn_ReusesWorkspace(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	e.svc.SetAsyncRunner(func(f func()) { f() }) // run classify+kickoff inline
	e.svc.SetGoalSuggester(fakeGoalSuggester{assess: &service.GoalAssessment{Actionable: true, Reason: "具体", GoalCondition: "make test exits 0"}})
	_, _, instructID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, instructID, "改个 bug", "")
	wsID := e.makeWorkspace(t, issueID)

	res, err := e.svc.OnIssueCreated(e.ctx, issueID, 1)
	require.NoError(t, err)

	assert.True(t, res.Reused)
	assert.Equal(t, wsID, res.WorkspaceID)
	assert.Equal(t, 0, e.fake.count(), "no new workspace created when reusing")
	require.Len(t, proxy.kickoffs, 1)
	assert.Contains(t, proxy.kickoffs[0], "【实现】")
}

func TestAdvanceIssue_ReuseExistingWorkspace(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	_, backlogID, instructID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, backlogID, "改个 bug", "")

	// Pre-existing non-archived workspace for the issue -> ensureWorkspace reuses it.
	wsID := e.makeWorkspace(t, issueID)

	res, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID:      issueID,
		ToColumn:     strconv.FormatInt(instructID, 10), // resolve by numeric id
		CallerUserID: 1,
	})
	require.NoError(t, err)

	assert.True(t, res.Reused)
	assert.Equal(t, wsID, res.WorkspaceID)
	assert.True(t, res.Instructed)
	assert.Equal(t, 0, e.fake.count(), "no new workspace created when reusing")
	require.Len(t, proxy.kickoffs, 1)
	assert.Contains(t, proxy.kickoffs[0], "【实现】")
}

func TestAdvanceIssue_NoneColumn_PlainMove(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	_, backlogID, instructID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, instructID, "记一笔", "")

	res, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID:      issueID,
		ToColumn:     "待办",
		CallerUserID: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, backlogID, res.ColumnID)
	assert.Equal(t, "none", res.OpPrimitive)
	assert.False(t, res.Instructed)
	assert.Zero(t, res.WorkspaceID)
	assert.Equal(t, 0, e.fake.count(), "no workspace for a none-column move")
	assert.Empty(t, proxy.kickoffs)
}

func TestAdvanceIssue_ResolveColumnErrors(t *testing.T) {
	e := setupEpicTest(t)
	_, backlogID, _ := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, backlogID, "x", "")

	// Unknown column name.
	_, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{IssueID: issueID, ToColumn: "不存在", CallerUserID: 1})
	require.Error(t, err)

	// Empty to_column.
	_, err = e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{IssueID: issueID, ToColumn: "   ", CallerUserID: 1})
	require.Error(t, err)
}

func TestAdvanceIssue_TotalRouteStepCapBlocks(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	e.svc.SetRoutingLimits(3, 99) // total-step cap 3; re-entry effectively off
	pid, backlogID, implID := makeProjectWithColumns(t, e)
	reviewID := makeColumn(t, e, pid, "审查", 2)
	setColumnOp(t, e, reviewID, "instruct", "严格 review")
	issueID := makeStandaloneIssue(t, e, backlogID, "任务", "")

	seq := []int64{implID, reviewID, implID, reviewID} // 4 routing steps
	var last service.AdvanceIssueResult
	for i, col := range seq {
		res, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
			IssueID: issueID, ToColumn: strconv.FormatInt(col, 10), CallerUserID: 1,
		})
		require.NoError(t, err)
		last = res
		if i < 3 {
			assert.False(t, res.Blocked, "step %d must not block", i+1)
			assert.Equal(t, i+1, res.RouteSteps)
		}
	}
	assert.True(t, last.Blocked, "4th step exceeds the total-step cap")
	assert.Equal(t, 4, last.RouteSteps)
	assert.Contains(t, last.BlockedReason, "total steps")
	assert.Equal(t, "gate_blocked", e.execStatus(t, issueID))
}

func TestAdvanceIssue_ColumnReentryCapBlocks(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	e.svc.SetRoutingLimits(99, 2) // re-entry cap 2; total-step cap effectively off
	pid, backlogID, implID := makeProjectWithColumns(t, e)
	reviewID := makeColumn(t, e, pid, "审查", 2)
	setColumnOp(t, e, reviewID, "instruct", "严格 review")
	issueID := makeStandaloneIssue(t, e, backlogID, "任务", "")

	// Ping-pong impl<->review; impl is entered on steps 1,3,5 -> re-entry hits 3 on step 5.
	seq := []int64{implID, reviewID, implID, reviewID, implID}
	var last service.AdvanceIssueResult
	for i, col := range seq {
		res, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
			IssueID: issueID, ToColumn: strconv.FormatInt(col, 10), CallerUserID: 1,
		})
		require.NoError(t, err)
		last = res
		if i < 4 {
			assert.False(t, res.Blocked, "step %d must not block", i+1)
		}
	}
	assert.True(t, last.Blocked, "3rd entry into 实现 exceeds the re-entry cap")
	assert.Contains(t, last.BlockedReason, "re-entry")
	assert.False(t, last.Instructed, "no instruction sent on the blocking move")
	assert.Equal(t, "gate_blocked", e.execStatus(t, issueID))
}

func TestAdvanceIssue_RouteStepsReportedUnderCap(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	_, backlogID, implID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, backlogID, "任务", "")

	res, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID: issueID, ToColumn: strconv.FormatInt(implID, 10), CallerUserID: 1,
	})
	require.NoError(t, err)
	assert.False(t, res.Blocked)
	assert.Equal(t, 1, res.RouteSteps)
	assert.True(t, res.Instructed)
}

func TestAdvanceIssue_CrossProjectColumnRejected(t *testing.T) {
	e := setupEpicTest(t)
	_, backlogID, _ := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, backlogID, "x", "")

	// A column in a *different* project must be rejected (numeric id form).
	p2, err := e.q.CreateProject(e.ctx, store.CreateProjectParams{Name: "other", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	otherCol := makeColumn(t, e, p2.ID, "别处", 0)

	_, err = e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID:      issueID,
		ToColumn:     strconv.FormatInt(otherCol, 10),
		CallerUserID: 1,
	})
	require.Error(t, err)
	assert.Equal(t, backlogID, issueColumn(t, e, issueID), "issue stays put when target column is cross-project")
}

func TestAbandonIssue_MovesToBacklogAndSetsTerminalState(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	_, backlogID, implID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, implID, "重构鉴权", "需要产品先定方案")

	res, err := e.svc.AbandonIssue(e.ctx, service.AbandonIssueInput{
		IssueID:      issueID,
		Reason:       "需要产品决策,不是工程任务",
		CallerUserID: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, backlogID, res.ColumnID, "abandoned issue parks in the none/backlog column")
	assert.Equal(t, backlogID, issueColumn(t, e, issueID))

	iss, err := e.q.GetIssue(e.ctx, issueID)
	require.NoError(t, err)
	assert.Equal(t, "abandoned", iss.ExecStatus)
	assert.True(t, iss.ExecStatusReason.Valid)
	assert.Equal(t, "需要产品决策,不是工程任务", iss.ExecStatusReason.String)
}

func TestAbandonIssue_RequiresReason(t *testing.T) {
	e := setupEpicTest(t)
	_, _, implID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, implID, "x", "")
	_, err := e.svc.AbandonIssue(e.ctx, service.AbandonIssueInput{IssueID: issueID, Reason: "   ", CallerUserID: 1})
	require.Error(t, err)
}

func TestAdvanceIssue_WritesOrgAuditOnOrgBoard(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})

	// Minimal org-owned board: user -> org -> org-owned project + 2 columns + issue.
	_, err := e.db.ExecContext(e.ctx, `INSERT INTO users (id, username, password_hash) VALUES (1, 'u1', 'x')`)
	require.NoError(t, err)
	_, err = e.db.ExecContext(e.ctx, `INSERT INTO organizations (id, slug, name, created_by) VALUES (7, 'acme', 'Acme', 1)`)
	require.NoError(t, err)
	p, err := e.q.CreateProject(e.ctx, store.CreateProjectParams{Name: "org-proj", OwnerType: "org", OwnerID: 7})
	require.NoError(t, err)
	backlogID := makeColumn(t, e, p.ID, "待办", 0)
	setColumnOp(t, e, backlogID, "none", "")
	doneID := makeColumn(t, e, p.ID, "完成", 1)
	setColumnOp(t, e, doneID, "complete", "")
	issueID := makeStandaloneIssue(t, e, backlogID, "上线功能", "")

	_, err = e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID: issueID, ToColumn: "完成", Reason: "收尾", CallerUserID: 1,
	})
	require.NoError(t, err)

	var n int
	require.NoError(t, e.db.QueryRowContext(e.ctx,
		`SELECT COUNT(*) FROM org_audit_log WHERE org_id = 7 AND action = 'issue.advanced' AND target_id = ?`,
		issueID).Scan(&n))
	assert.Equal(t, 1, n, "advance on an org board writes an org_audit_log row")
}

func TestAdvanceIssue_RecordsExecEvent(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	execEvents := service.NewExecEventService(e.db)
	e.svc.SetExecEventService(execEvents)
	_, backlogID, implID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, backlogID, "任务", "")

	_, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID: issueID, ToColumn: strconv.FormatInt(implID, 10), Reason: "开始实现", CallerUserID: 1,
	})
	require.NoError(t, err)

	events, err := execEvents.List(e.ctx, issueID)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, "advance", events[0].Kind)
	assert.Contains(t, events[0].Summary, "实现")
}

func TestOnIssueCreated_NotActionable_BuildsShellNoKickoff(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	execEvents := service.NewExecEventService(e.db)
	e.svc.SetExecEventService(execEvents)
	e.svc.SetAsyncRunner(func(f func()) { f() })
	e.svc.SetGoalSuggester(fakeGoalSuggester{assess: &service.GoalAssessment{Actionable: false, Reason: "信息不足"}})
	_, _, instructID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, instructID, "随便记一下", "")

	res, err := e.svc.OnIssueCreated(e.ctx, issueID, 1)
	require.NoError(t, err)

	// Shell built, but NOT kicked off.
	assert.NotZero(t, res.WorkspaceID)
	assert.Equal(t, 1, e.fake.count())
	assert.Empty(t, proxy.kickoffs, "non-actionable issue is not auto-instructed")
	assert.NotEqual(t, "autohost", e.fake.env["NIUNIU_PERMISSION_MODE"])

	// A withheld-kickoff intervention event was recorded.
	evs, err := execEvents.List(e.ctx, issueID)
	require.NoError(t, err)
	require.NotEmpty(t, evs)
	assert.Equal(t, "intervention", evs[len(evs)-1].Kind)
	assert.Contains(t, evs[len(evs)-1].Summary, "未自动开工")
}

func TestOnIssueCreated_SuggesterNil_Conservative_NoKickoff(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	e.svc.SetAsyncRunner(func(f func()) { f() })
	// no SetGoalSuggester -> nil -> conservative
	_, _, instructID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, instructID, "加登录", "用 JWT")

	res, err := e.svc.OnIssueCreated(e.ctx, issueID, 1)
	require.NoError(t, err)
	assert.NotZero(t, res.WorkspaceID)
	assert.Equal(t, 1, e.fake.count())
	assert.Empty(t, proxy.kickoffs, "no suggester -> do not auto-kickoff")
}

func TestOnIssueCreated_SuggesterError_Conservative_NoKickoff(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	e.svc.SetAsyncRunner(func(f func()) { f() })
	e.svc.SetGoalSuggester(fakeGoalSuggester{err: assert.AnError})
	_, _, instructID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, instructID, "加登录", "用 JWT")

	res, err := e.svc.OnIssueCreated(e.ctx, issueID, 1)
	require.NoError(t, err)
	assert.NotZero(t, res.WorkspaceID)
	assert.Empty(t, proxy.kickoffs, "classify error -> conservative, no auto-kickoff")
}

// --- complete-column uncommitted-code gate (AI 移动 column 规则, ws-558) ---

// makeCompleteColumn adds a `complete` 完成 column to the project.
func makeCompleteColumn(t *testing.T, e *epicTestEnv, projectID int64) int64 {
	t.Helper()
	doneID := makeColumn(t, e, projectID, "完成", 9)
	setColumnOp(t, e, doneID, "complete", "")
	return doneID
}

func TestAdvanceIssue_CompleteColumn_DirtyWorkspaceRefused(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	execEvents := service.NewExecEventService(e.db)
	e.svc.SetExecEventService(execEvents)
	pid, _, implID := makeProjectWithColumns(t, e)
	doneID := makeCompleteColumn(t, e, pid)
	issueID := makeStandaloneIssue(t, e, implID, "改需求", "")
	e.makeWorkspace(t, issueID)

	e.svc.SetCompletionDirtyChecker(func(_ context.Context, _ store.Workspace) (bool, string) {
		return true, "repo-a 有 3 个未提交变更"
	})

	_, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID: issueID, ToColumn: strconv.FormatInt(doneID, 10), CallerUserID: 1,
	})
	require.Error(t, err, "dirty workspace must refuse the move to the complete column")
	assert.Contains(t, err.Error(), "未提交")
	assert.Contains(t, err.Error(), "完成")
	assert.Equal(t, implID, issueColumn(t, e, issueID), "card stays put on a refused complete move")

	// The refusal lands on the execution timeline; no advance event was recorded.
	evs, lerr := execEvents.List(e.ctx, issueID)
	require.NoError(t, lerr)
	require.Len(t, evs, 1)
	assert.Equal(t, "intervention", evs[0].Kind)
	assert.Contains(t, evs[0].Summary, "未提交")
}

func TestAdvanceIssue_CompleteColumn_CleanWorkspaceMoves(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	pid, _, implID := makeProjectWithColumns(t, e)
	doneID := makeCompleteColumn(t, e, pid)
	issueID := makeStandaloneIssue(t, e, implID, "改需求", "")
	e.makeWorkspace(t, issueID)

	e.svc.SetCompletionDirtyChecker(func(_ context.Context, _ store.Workspace) (bool, string) {
		return false, ""
	})

	res, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID: issueID, ToColumn: strconv.FormatInt(doneID, 10), CallerUserID: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, doneID, res.ColumnID)
	assert.Equal(t, "complete", res.OpPrimitive)
	assert.Equal(t, doneID, issueColumn(t, e, issueID))
}

// --- complete-column epic-child auto-merge (epic 自动执行逻辑优化, ws-568) ---

// An epic CHILD advanced into the done lane has its worktree committed + merged
// into the epic feature branch BEFORE the card moves — advance_issue is the
// child's canonical last step, so the integration must not wait for a later
// agent_done that may never come.
func TestAdvanceIssue_CompleteColumn_EpicChildMergesIntoEpicBranch(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	pid, backlogID, _ := makeProjectWithColumns(t, e)
	doneID := makeCompleteColumn(t, e, pid)
	epicID := e.makeEpic(t, backlogID, "Epic AdvMerge")
	childID := e.makeChild(t, backlogID, epicID, 0, "子任务A")
	wsID := e.makeWorkspace(t, childID)
	e.makeWorktree(t, wsID, "repo-a")

	merger := &fakeMerger{}
	e.svc.SetMerger(merger)
	e.svc.SetCompletionDirtyChecker(func(_ context.Context, _ store.Workspace) (bool, string) { return false, "" })

	res, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID: childID, ToColumn: strconv.FormatInt(doneID, 10), Reason: "开发完成", CallerUserID: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, doneID, res.ColumnID)
	assert.Equal(t, []string{"repo-a"}, merger.commits, "child worktree committed on advance to 完成")
	assert.Equal(t, []string{"repo-a->" + epicBranch(epicID)}, merger.merges, "child merged into the epic branch on advance to 完成")
	assert.Equal(t, doneID, issueColumn(t, e, childID))
}

// A merge conflict refuses the move: the card stays put and the child agent gets
// an actionable error naming the epic branch.
func TestAdvanceIssue_CompleteColumn_EpicChildMergeConflictRefused(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	pid, backlogID, _ := makeProjectWithColumns(t, e)
	doneID := makeCompleteColumn(t, e, pid)
	epicID := e.makeEpic(t, backlogID, "Epic AdvConflict")
	childID := e.makeChild(t, backlogID, epicID, 0, "子任务B")
	wsID := e.makeWorkspace(t, childID)
	e.makeWorktree(t, wsID, "repo-a")

	merger := &fakeMerger{mergeFail: map[string]error{"repo-a": errMergeConflict}}
	e.svc.SetMerger(merger)
	e.svc.SetCompletionDirtyChecker(func(_ context.Context, _ store.Workspace) (bool, string) { return false, "" })

	_, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID: childID, ToColumn: strconv.FormatInt(doneID, 10), CallerUserID: 1,
	})
	require.Error(t, err, "merge conflict must refuse the move to the complete column")
	assert.Contains(t, err.Error(), epicBranch(epicID))
	assert.Equal(t, backlogID, issueColumn(t, e, childID), "card stays put on a refused merge")
	assert.Equal(t, "failed", e.execStatus(t, childID), "integration failure recorded on the child")
}

// A STANDALONE issue advanced into the done lane never touches the merger (the
// epic integration applies to epic children only).
func TestAdvanceIssue_CompleteColumn_StandaloneSkipsEpicMerge(t *testing.T) {
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	pid, _, implID := makeProjectWithColumns(t, e)
	doneID := makeCompleteColumn(t, e, pid)
	issueID := makeStandaloneIssue(t, e, implID, "独立任务", "")
	e.makeWorkspace(t, issueID)

	merger := &fakeMerger{}
	e.svc.SetMerger(merger)
	e.svc.SetCompletionDirtyChecker(func(_ context.Context, _ store.Workspace) (bool, string) { return false, "" })

	_, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID: issueID, ToColumn: strconv.FormatInt(doneID, 10), CallerUserID: 1,
	})
	require.NoError(t, err)
	assert.Empty(t, merger.commits)
	assert.Empty(t, merger.merges)
	assert.Equal(t, doneID, issueColumn(t, e, issueID))
}

// End-to-end check of the DEFAULT git-status probe (no checker injected): a real
// repo with an uncommitted file blocks the move; after committing it passes.
func TestAdvanceIssue_CompleteColumn_DefaultGitProbe(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	e := setupEpicTest(t)
	e.svc.SetAgentProxy(&fakeAgentProxy{})
	pid, _, implID := makeProjectWithColumns(t, e)
	doneID := makeCompleteColumn(t, e, pid)
	issueID := makeStandaloneIssue(t, e, implID, "改需求", "")

	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		out, rerr := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, rerr, "git %v: %s", args, out)
	}
	run("init")
	run("config", "user.email", "test@test.local")
	run("config", "user.name", "test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x"), 0o644))

	// Workspace whose path IS the dirty repo (no worktree rows -> ws.Path fallback).
	_, err := e.q.CreateWorkspace(e.ctx, store.CreateWorkspaceParams{
		IssueID: sql.NullInt64{Int64: issueID, Valid: true}, Name: "ws", Path: repo,
		Status: "running", OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)

	_, err = e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID: issueID, ToColumn: strconv.FormatInt(doneID, 10), CallerUserID: 1,
	})
	require.Error(t, err, "uncommitted file must block the move")
	assert.Contains(t, err.Error(), "未提交")
	assert.Equal(t, implID, issueColumn(t, e, issueID))

	run("add", "-A")
	run("commit", "-m", "all committed")

	res, err := e.svc.AdvanceIssue(e.ctx, service.AdvanceIssueInput{
		IssueID: issueID, ToColumn: strconv.FormatInt(doneID, 10), CallerUserID: 1,
	})
	require.NoError(t, err, "clean repo allows the move")
	assert.Equal(t, doneID, res.ColumnID)
	assert.Equal(t, doneID, issueColumn(t, e, issueID))
}

// When the workspace's agent is already running, DispatchInstructColumn must
// queue the instruction message rather than send a competing kickoff.
func TestDispatchInstructColumn_WorkspaceRunning_Queues(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	_, backlogID, instructID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, backlogID, "修 bug", "")

	// Create a workspace for the issue so DispatchInstructColumn reuses it.
	wsID := e.makeWorkspace(t, issueID)

	// Mark that workspace's session as running so Enqueue returns true.
	proxy.runningWS = wsID

	e.svc.DispatchInstructColumn(e.ctx, issueID, instructID, 1)

	// Message must be queued, not sent as a kickoff.
	assert.Empty(t, proxy.kickoffs, "SendKickoff must not be called when workspace is running")
	require.Len(t, proxy.enqueued, 1)
	assert.Contains(t, proxy.enqueued[0], "【实现】")
	assert.Contains(t, proxy.enqueued[0], "修 bug")
}

// When the workspace's agent is idle, DispatchInstructColumn sends a kickoff directly.
func TestDispatchInstructColumn_WorkspaceIdle_SendsKickoff(t *testing.T) {
	e := setupEpicTest(t)
	proxy := &fakeAgentProxy{}
	e.svc.SetAgentProxy(proxy)
	_, backlogID, instructID := makeProjectWithColumns(t, e)
	issueID := makeStandaloneIssue(t, e, backlogID, "加功能", "")

	// Create workspace but leave proxy.runningWS = 0 (idle).
	e.makeWorkspace(t, issueID)

	e.svc.DispatchInstructColumn(e.ctx, issueID, instructID, 1)

	assert.Empty(t, proxy.enqueued, "Enqueue must not be used when workspace is idle")
	require.Len(t, proxy.kickoffs, 1)
	assert.Contains(t, proxy.kickoffs[0], "【实现】")
	assert.Contains(t, proxy.kickoffs[0], "加功能")
}
