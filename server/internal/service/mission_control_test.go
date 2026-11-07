package service

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func missionExec(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	res, err := db.ExecContext(context.Background(), query, args...)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// seedMissionControl builds a user-owned project with a todo / done board and
// one issue in each, plus a running workspace tied to the completed issue.
func seedMissionControl(t *testing.T, db *sql.DB) (userID, projectID int64, doneColID, todoColID int64) {
	t.Helper()
	ctx := context.Background()
	userID = newOrgTestUser(t, db, "mission-user", "member")
	projectID = missionExec(t, db,
		`INSERT INTO projects (name, owner_type, owner_id) VALUES ('mission-p','user',?)`, userID)
	todoColID = missionExec(t, db,
		`INSERT INTO columns (project_id, name, position) VALUES (?,'todo',0)`, projectID)
	doneColID = missionExec(t, db,
		`INSERT INTO columns (project_id, name, position) VALUES (?,'done',1)`, projectID)
	_, err := db.ExecContext(ctx, `UPDATE columns SET op_primitive='complete' WHERE id = ?`, doneColID)
	require.NoError(t, err)
	return userID, projectID, doneColID, todoColID
}

func newMissionControlService(db *sql.DB) *MissionControlService {
	q := store.NewQueries(db)
	authz := NewAuthz(q, db)
	return NewMissionControlService(store.Wrap(db), q, authz, NewIntakeService(db, q))
}

func metricByKey(t *testing.T, metrics []MissionControlMetric, key string) MissionControlMetric {
	t.Helper()
	for _, m := range metrics {
		if m.Key == key {
			return m
		}
	}
	t.Fatalf("metric %q not found", key)
	return MissionControlMetric{}
}

// TestMissionControl_TodayBoundaryUsesUTCStorage pins the timezone fix: stored
// timestamps are UTC, so the "today" boundary must be the local-midnight
// instant converted to UTC. A row created at 00:15 local (16:15Z the previous
// UTC day) counts as today on both panels, while 23:59 local the previous day
// (15:59Z) does not.
func TestMissionControl_TodayBoundaryUsesUTCStorage(t *testing.T) {
	db := openOrgTestDB(t)
	ctx := context.Background()
	userID, projectID, _, todoColID := seedMissionControl(t, db)

	// Fixed wall clock: 2026-11-04 00:30 at UTC+8. Today starts at
	// 2026-11-03 16:00Z; the previous local day ends at 15:59:59Z.
	now := time.Date(2026, 11, 4, 0, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))
	today := time.Date(2026, 11, 3, 16, 15, 0, 0, time.UTC)
	yesterday := time.Date(2026, 11, 3, 15, 59, 0, 0, time.UTC)

	wsID := missionExec(t, db,
		`INSERT INTO workspaces (name, path, status, owner_type, owner_id) VALUES ('ws','/tmp/ws','running','user',?)`, userID)
	issueID := missionExec(t, db,
		`INSERT INTO issues (column_id, title, position) VALUES (?,'probe',0)`, todoColID)
	eventID := missionExec(t, db,
		`INSERT INTO intake_events (project_id, source_kind, source_ref, event_ext_id, event_kind) `+
			`VALUES (?,'im_bot','chat-1','evt-1','message')`, projectID)
	for _, at := range []time.Time{today, yesterday} {
		missionExec(t, db,
			`INSERT INTO intake_decisions (event_id, project_id, mode_at_decision, strength, decision, executed, issue_id, workspace_id, created_at) `+
				`VALUES (?,?,'auto','explicit','accept',1,?,?,?)`,
			eventID, projectID, issueID, wsID, at)
		missionExec(t, db,
			`INSERT INTO issue_exec_events (issue_id, kind, summary, created_at) VALUES (?,'intervention','probe',?)`,
			issueID, at)
		missionExec(t, db,
			`INSERT INTO workspace_costs (workspace_id, cost_usd, created_at) VALUES (?,1.5,?)`, wsID, at)
	}
	// Distinct token amounts make "today only" unambiguous.
	missionExec(t, db,
		`INSERT INTO workspace_token_hourly (workspace_id, bucket_hour, input_tokens) VALUES (?,?,100)`, wsID, today)
	missionExec(t, db,
		`INSERT INTO workspace_token_hourly (workspace_id, bucket_hour, input_tokens) VALUES (?,?,999)`, wsID, yesterday)

	svc := newMissionControlService(db)
	svc.now = func() time.Time { return now }
	view, err := svc.View(ctx, userID, nil)
	require.NoError(t, err)

	todayAccepted := metricByKey(t, view.Intake.Metrics, "today_accepted")
	require.NotNil(t, todayAccepted.Value)
	assert.Equal(t, float64(1), *todayAccepted.Value, "00:15 local must count as today")

	interventions := metricByKey(t, view.Completion.Metrics, "interventions")
	require.NotNil(t, interventions.Value)
	assert.Equal(t, float64(1), *interventions.Value)

	tokens := metricByKey(t, view.Completion.Metrics, "today_tokens")
	require.NotNil(t, tokens.Value)
	assert.Equal(t, float64(100), *tokens.Value)

	cost := metricByKey(t, view.Completion.Metrics, "today_cost")
	require.NotNil(t, cost.Value)
	assert.InDelta(t, 1.5, *cost.Value, 1e-9)

	// Per-day buckets use the same window; the bucket label follows the host's
	// local zone, so derive the expected label instead of hard-coding it.
	wantDay := today.In(time.Local).Format("2006-01-02")
	found := false
	for _, d := range view.Intake.PerSource {
		if d.Day == wantDay && d.Count == 1 {
			found = true
		}
	}
	assert.True(t, found, "today bucket %q missing from %+v", wantDay, view.Intake.PerSource)
}

func TestMissionControl_NoProjectsHasNoData(t *testing.T) {
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "no-projects", "member")
	svc := newMissionControlService(db)

	view, err := svc.View(context.Background(), userID, nil)
	require.NoError(t, err)
	require.Len(t, view.Intake.Metrics, 6)
	require.Len(t, view.Completion.Metrics, 8)
	for _, m := range view.Intake.Metrics {
		assert.Equal(t, "no_data", m.Status, m.Key)
		assert.Nil(t, m.Value, m.Key)
	}
	for _, m := range view.Completion.Metrics {
		assert.Equal(t, "no_data", m.Status, m.Key)
		assert.Nil(t, m.Value, m.Key)
	}
}

func TestMissionControl_IntakeAndCompletionPanels(t *testing.T) {
	db := openOrgTestDB(t)
	ctx := context.Background()
	userID, projectID, doneColID, _ := seedMissionControl(t, db)

	now := time.Now()
	received := now.Add(-10 * time.Minute)

	// Workspace for the completed issue (owner = the caller, running).
	wsID := missionExec(t, db,
		`INSERT INTO workspaces (name, path, status, owner_type, owner_id, created_at, updated_at) `+
			`VALUES ('ws','/tmp/ws','running','user',?,?,?)`, userID, now, now)
	issueID := missionExec(t, db,
		`INSERT INTO issues (column_id, title, position, created_at, updated_at) VALUES (?,'done task',0,?,?)`,
		doneColID, now, now)

	eventID := missionExec(t, db,
		`INSERT INTO intake_events (project_id, source_kind, source_ref, event_ext_id, event_kind, thread_ext_id, received_at) `+
			`VALUES (?,'im_bot','chat-1','evt-1','message','thread-1',?)`, projectID, received)
	// One auto-accepted decision (today), one pending, one ignored.
	missionExec(t, db,
		`INSERT INTO intake_decisions (event_id, project_id, mode_at_decision, strength, decision, executed, issue_id, workspace_id, created_at) `+
			`VALUES (?,?,'auto','explicit','accept',1,?,?,?)`, eventID, projectID, issueID, wsID, now)
	missionExec(t, db,
		`INSERT INTO intake_decisions (event_id, project_id, mode_at_decision, strength, decision, executed, created_at) `+
			`VALUES (?,?,'observe','explicit','accept',0,?)`, eventID, projectID, now)
	missionExec(t, db,
		`INSERT INTO intake_decisions (event_id, project_id, mode_at_decision, strength, decision, executed, reason_text, created_at) `+
			`VALUES (?,?,'observe','explicit','ignore',0,'noise',?)`, eventID, projectID, now)

	// Two advance rounds for the completed issue.
	missionExec(t, db, `INSERT INTO issue_exec_events (issue_id, kind, summary) VALUES (?,'advance','moved')`, issueID)
	missionExec(t, db, `INSERT INTO issue_exec_events (issue_id, kind, summary) VALUES (?,'advance','moved again')`, issueID)

	svc := newMissionControlService(db)
	view, err := svc.View(ctx, userID, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, view.Intake.ProjectCount)
	today := metricByKey(t, view.Intake.Metrics, "today_accepted")
	require.NotNil(t, today.Value)
	assert.Equal(t, float64(1), *today.Value)

	pending := metricByKey(t, view.Intake.Metrics, "pending")
	require.NotNil(t, pending.Value)
	assert.Equal(t, float64(1), *pending.Value)

	approval := metricByKey(t, view.Intake.Metrics, "approval_rate")
	require.NotNil(t, approval.Value)
	assert.Equal(t, float64(50), *approval.Value)
	assert.Equal(t, int64(1), approval.Num)
	assert.Equal(t, int64(2), approval.Denom)

	latency := metricByKey(t, view.Intake.Metrics, "event_to_start")
	require.NotNil(t, latency.Value)
	assert.InDelta(t, 10, *latency.Value, 1)

	// Auto decision, no abandoned event → misfire 0% with a real denominator.
	misfire := metricByKey(t, view.Intake.Metrics, "misfire_rate")
	require.NotNil(t, misfire.Value)
	assert.Equal(t, float64(0), *misfire.Value)
	assert.Equal(t, int64(1), misfire.Denom)

	assert.Equal(t, "ok", today.Status)
	inProgress := metricByKey(t, view.Completion.Metrics, "in_progress")
	require.NotNil(t, inProgress.Value)
	assert.Equal(t, float64(1), *inProgress.Value)

	firstPass := metricByKey(t, view.Completion.Metrics, "first_pass_rate")
	require.NotNil(t, firstPass.Value)
	assert.Equal(t, float64(100), *firstPass.Value)

	rounds := metricByKey(t, view.Completion.Metrics, "avg_rounds")
	require.NotNil(t, rounds.Value)
	assert.Equal(t, float64(2), *rounds.Value)

	tokens := metricByKey(t, view.Completion.Metrics, "today_tokens")
	assert.Equal(t, "ok", tokens.Status)
	require.NotNil(t, tokens.Value)
	assert.Equal(t, float64(0), *tokens.Value)
}

func TestMissionControl_WarningAndMisfireAndReopen(t *testing.T) {
	db := openOrgTestDB(t)
	ctx := context.Background()
	userID, projectID, doneColID, _ := seedMissionControl(t, db)
	now := time.Now()

	eventID := missionExec(t, db,
		`INSERT INTO intake_events (project_id, source_kind, source_ref, event_ext_id, event_kind, thread_ext_id) `+
			`VALUES (?,'im_bot','chat-1','evt-2','message','thread-1')`, projectID)
	issueID := missionExec(t, db,
		`INSERT INTO issues (column_id, title, position, updated_at) VALUES (?,'reopened',0,?)`, doneColID, now)
	// Auto pick, then abandoned 1h later → misfire.
	missionExec(t, db,
		`INSERT INTO intake_decisions (event_id, project_id, mode_at_decision, strength, decision, executed, issue_id, created_at) `+
			`VALUES (?,?,'auto','inferred','accept',1,?,?)`, eventID, projectID, issueID, now.Add(-3*time.Hour))
	missionExec(t, db,
		`INSERT INTO issue_exec_events (issue_id, kind, summary, created_at) VALUES (?,'terminal','abandoned：out of scope',?)`,
		issueID, now.Add(-2*time.Hour))

	// Moved into the complete column, then back out 1 day later → fake completion.
	missionExec(t, db,
		`INSERT INTO issue_activities (issue_id, action, field, old_value, new_value, created_at) `+
			`VALUES (?,'moved','column','todo','done',?)`, issueID, now.Add(-48*time.Hour))
	missionExec(t, db,
		`INSERT INTO issue_activities (issue_id, action, field, old_value, new_value, created_at) `+
			`VALUES (?,'moved','column','done','todo',?)`, issueID, now.Add(-24*time.Hour))

	svc := newMissionControlService(db)
	view, err := svc.View(ctx, userID, nil)
	require.NoError(t, err)

	misfire := metricByKey(t, view.Intake.Metrics, "misfire_rate")
	require.NotNil(t, misfire.Value)
	assert.Equal(t, float64(100), *misfire.Value)
	assert.Equal(t, "warning", misfire.Status)

	fake := metricByKey(t, view.Completion.Metrics, "fake_completion_rate")
	require.NotNil(t, fake.Value)
	assert.Equal(t, float64(100), *fake.Value)
	assert.Equal(t, "warning", fake.Status)
	assert.Equal(t, int64(1), fake.Denom)
}

func TestMissionControl_GlobalPause(t *testing.T) {
	db := openOrgTestDB(t)
	ctx := context.Background()
	userID, projectID, _, _ := seedMissionControl(t, db)
	svc := newMissionControlService(db)

	n, err := svc.SetPaused(ctx, userID, nil, true)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	view, err := svc.View(ctx, userID, nil)
	require.NoError(t, err)
	assert.True(t, view.Intake.Paused)

	var paused int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT paused FROM intake_project_state WHERE project_id = ?`, projectID).Scan(&paused))
	assert.Equal(t, 1, paused)

	n, err = svc.SetPaused(ctx, userID, nil, false)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	view, err = svc.View(ctx, userID, nil)
	require.NoError(t, err)
	assert.False(t, view.Intake.Paused)
}

func TestMissionControl_OwnerFilterAndCrossOwnerIsolation(t *testing.T) {
	db := openOrgTestDB(t)
	ctx := context.Background()
	userID, projectID, _, _ := seedMissionControl(t, db)
	otherID := newOrgTestUser(t, db, "mission-other", "member")
	otherProject := missionExec(t, db,
		`INSERT INTO projects (name, owner_type, owner_id) VALUES ('other-p','user',?)`, otherID)

	// An intake event in the other user's project must not influence the caller.
	missionExec(t, db,
		`INSERT INTO intake_events (project_id, source_kind, source_ref, event_ext_id, event_kind) `+
			`VALUES (?,'im_bot','chat-x','evt-x','message')`, otherProject)

	svc := newMissionControlService(db)
	view, err := svc.View(ctx, userID, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, view.Intake.ProjectCount)

	// Narrowing to the caller's own owner keeps exactly their project.
	filter := &OwnerRef{Type: "user", ID: userID}
	view, err = svc.View(ctx, userID, filter)
	require.NoError(t, err)
	assert.Equal(t, 1, view.Intake.ProjectCount)

	// A foreign owner filter yields no projects → "—", never another tenant's data.
	foreign := &OwnerRef{Type: "user", ID: otherID}
	view, err = svc.View(ctx, userID, foreign)
	require.NoError(t, err)
	assert.Equal(t, 0, view.Intake.ProjectCount)
	assert.Equal(t, "no_data", metricByKey(t, view.Intake.Metrics, "today_accepted").Status)

	_ = projectID
}

// TestMissionControlOps covers the ops console aggregate: owner → channel →
// account grouping, per-account status/output/metrics/budget/approvals/risk,
// and per-account failure isolation.
func TestMissionControlOps(t *testing.T) {
	db := openOrgTestDB(t)
	ctx := context.Background()
	userID := newOrgTestUser(t, db, "ops-owner", "member")
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.Local)

	projectID := missionExec(t, db,
		`INSERT INTO projects (name, owner_type, owner_id) VALUES ('ops-p','user',?)`, userID)
	colID := missionExec(t, db,
		`INSERT INTO columns (project_id, name, position) VALUES (?,'todo',0)`, projectID)
	wsID := missionExec(t, db,
		`INSERT INTO workspaces (name, path, status, owner_type, owner_id) VALUES ('ops-ws','/tmp/ops-ws','running','user',?)`, userID)

	// Director bot: not an account, so it must not appear in a channel.
	missionExec(t, db,
		`INSERT INTO ops_bots (owner_type, owner_id, role, pack_id, account_ref, display_name) `+
			`VALUES ('user',?,'director','','','总监')`, userID)

	epicID := missionExec(t, db,
		`INSERT INTO issues (column_id, title, position) VALUES (?,'account epic',0)`, colID)
	childID := missionExec(t, db,
		`INSERT INTO issues (column_id, title, position, parent_issue_id) VALUES (?,'child',1,?)`, colID, epicID)

	wechatID := missionExec(t, db,
		`INSERT INTO ops_bots (owner_type, owner_id, role, pack_id, pack_version, account_ref, display_name, budget_json, epic_issue_id, status) `+
			`VALUES ('user',?,'account','wechat_mp',1,'brand-a','品牌 A',?,?,'paused')`,
		userID, `{"monthly_limit":100,"currency":"USD","paused_reason":"budget"}`, epicID)
	// A second channel: no epic, no budget configured.
	xhsID := missionExec(t, db,
		`INSERT INTO ops_bots (owner_type, owner_id, role, pack_id, pack_version, account_ref, display_name) `+
			`VALUES ('user',?,'account','xiaohongshu',2,'brand-b','品牌 B')`, userID)

	// Output: two published today, one earlier this week, one pending (not output).
	missionExec(t, db,
		`INSERT INTO ops_publish_records (issue_id, bot_id, idempotency_key, status, created_at) VALUES (?,?,'k1','published',?)`,
		childID, wechatID, now.Add(-time.Hour))
	missionExec(t, db,
		`INSERT INTO ops_publish_records (issue_id, bot_id, idempotency_key, status, created_at) VALUES (?,?,'k2','published',?)`,
		childID, wechatID, now.Add(-2*time.Hour))
	missionExec(t, db,
		`INSERT INTO ops_publish_records (issue_id, bot_id, idempotency_key, status, created_at) VALUES (?,?,'k3','published',?)`,
		childID, wechatID, now.AddDate(0, 0, -3))
	missionExec(t, db,
		`INSERT INTO ops_publish_records (issue_id, bot_id, idempotency_key, status, created_at) VALUES (?,?,'k4','pending',?)`,
		childID, wechatID, now.Add(-time.Hour))

	// Spend over the limit inside this month → budget_exceeded.
	missionExec(t, db,
		`INSERT INTO issue_exec_events (issue_id, kind, summary, cost_usd, created_at) VALUES (?,'cost','spend',150,?)`,
		childID, now)

	missionExec(t, db,
		`INSERT INTO ops_metrics_daily (bot_id, date, metrics_json, source) VALUES (?,?,?,'wechat')`,
		wechatID, now.Format("2006-01-02"), `{"views":1200,"likes":80}`)

	// Expired browser login and a high-risk engagement both add risk reasons.
	missionExec(t, db,
		`INSERT INTO ops_browser_sessions (bot_id, backend, profile_dir, login_status, health, risk_state) `+
			`VALUES (?,'local','/tmp/profile','expired','healthy','normal')`, wechatID)
	missionExec(t, db,
		`INSERT INTO ops_engagements (bot_id, platform_engagement_id, kind, text, risk_level, status) `+
			`VALUES (?,'eng-1','comment','hi','high','new')`, wechatID)

	missionExec(t, db,
		`INSERT INTO agent_ask_user_requests (workspace_id, owner_type, owner_id, session_id, questions_json, kind, metadata_json, status, expires_at) `+
			`VALUES (?, 'user', ?, 'sess-1', '{}', 'ops_approval', ?, 'pending', ?)`,
		wsID, userID, `{"bot_id":`+strconv.FormatInt(wechatID, 10)+`,"type":"publish"}`, now.Add(time.Hour))

	svc := newMissionControlService(db)
	svc.now = func() time.Time { return now }
	view, err := svc.OpsView(ctx, userID, nil)
	require.NoError(t, err)
	require.Len(t, view.Owners, 1)
	assert.Equal(t, 2, view.AccountCount)

	owner := view.Owners[0]
	assert.Empty(t, owner.Error)
	assert.Equal(t, "ops-owner", owner.OwnerLabel)
	require.Len(t, owner.Channels, 2)
	assert.Equal(t, "wechat_mp", owner.Channels[0].PackID)
	assert.Equal(t, "xiaohongshu", owner.Channels[1].PackID)

	require.Len(t, owner.Channels[0].Accounts, 1)
	acc := owner.Channels[0].Accounts[0]
	assert.Equal(t, wechatID, acc.BotID)
	assert.Equal(t, "paused", acc.Status)
	assert.Equal(t, "ops-owner", acc.Operator)
	assert.Equal(t, int64(2), acc.Output.Today)
	assert.Equal(t, int64(3), acc.Output.Week)
	require.NotNil(t, acc.Metrics)
	assert.Equal(t, now.Format("2006-01-02"), acc.Metrics.Date)
	assert.Equal(t, int64(1), acc.PendingApprovals)
	assert.Equal(t, int64(1), acc.HighRiskEngagements)
	assert.True(t, acc.Budget.Configured)
	assert.Equal(t, 100.0, acc.Budget.Limit)
	assert.InDelta(t, 150.0, acc.Budget.Spent, 1e-9)
	assert.InDelta(t, -50.0, acc.Budget.Remaining, 1e-9)
	assert.Equal(t, OpsRiskCritical, acc.Risk)
	assert.ElementsMatch(t,
		[]string{"paused", "budget_exceeded", "browser_login_expired", "high_risk_engagement"},
		acc.RiskReasons)
	require.NotNil(t, acc.Browser)
	assert.Equal(t, "expired", acc.Browser.LoginStatus)
	assert.Empty(t, acc.Error)

	require.Len(t, owner.Channels[1].Accounts, 1)
	other := owner.Channels[1].Accounts[0]
	assert.Equal(t, xhsID, other.BotID)
	assert.False(t, other.Budget.Configured)
	assert.Equal(t, OpsRiskNormal, other.Risk)
	assert.Empty(t, other.RiskReasons)
	assert.Nil(t, other.Browser)
	assert.Nil(t, other.Metrics)

	// A foreign owner filter is never exposed, even if it names a real user.
	foreign := &OwnerRef{Type: "user", ID: newOrgTestUser(t, db, "ops-other", "member")}
	path, err := svc.OpsView(ctx, userID, foreign)
	require.NoError(t, err)
	assert.Equal(t, 0, path.AccountCount)
	assert.Empty(t, path.Owners)
}
