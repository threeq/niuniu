package service

import (
	"context"
	"database/sql"
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
