package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/completion"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedCompletionProject inserts an org-owned project (owner org 1, created by
// user 1) with one column and one issue.
func seedCompletionProject(t *testing.T, db *sql.DB) (projectID, issueID int64) {
	t.Helper()
	ctx := context.Background()
	actor := newOrgTestUser(t, db, "completion-actor", "member")
	_, err := db.ExecContext(ctx, `INSERT INTO organizations (id, slug, name, created_by) VALUES (1,'acme','Acme',?)`, actor)
	require.NoError(t, err)

	res, err := db.ExecContext(ctx,
		`INSERT INTO projects (name, owner_type, owner_id) VALUES ('completion-p','org',1)`)
	require.NoError(t, err)
	projectID, err = res.LastInsertId()
	require.NoError(t, err)
	res, err = db.ExecContext(ctx,
		`INSERT INTO columns (project_id, name, position) VALUES (?,'todo',0)`, projectID)
	require.NoError(t, err)
	colID, err := res.LastInsertId()
	require.NoError(t, err)
	res, err = db.ExecContext(ctx,
		`INSERT INTO issues (column_id, title, position) VALUES (?,'task',0)`, colID)
	require.NoError(t, err)
	issueID, err = res.LastInsertId()
	require.NoError(t, err)
	return projectID, issueID
}

func countAudit(t *testing.T, db *sql.DB, action string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM org_audit_log WHERE action = ?`, action).Scan(&n))
	return n
}

func TestCompletionService_AuditsPolicyMutations(t *testing.T) {
	ctx := context.Background()
	db := openOrgTestDB(t)
	projectID, issueID := seedCompletionProject(t, db)
	svc := NewCompletionService(db, store.New(db))

	actor := int64(1)
	_, err := svc.UpdateSettings(ctx, projectID, actor, CompletionSettingsInput{MaxRounds: int64Ptr(5)})
	require.NoError(t, err)
	assert.Equal(t, 1, countAudit(t, db, "completion.settings_updated"))

	profileName := "测试档"
	profile, err := svc.CreateProfile(ctx, projectID, actor, CompletionProfileInput{Name: &profileName})
	require.NoError(t, err)
	assert.Equal(t, 1, countAudit(t, db, "completion.profile_created"))

	_, err = svc.UpdateProfile(ctx, projectID, profile.ID, actor, CompletionProfileInput{MaxRounds: int64Ptr(9)})
	require.NoError(t, err)
	assert.Equal(t, 1, countAudit(t, db, "completion.profile_updated"))

	_, err = svc.SetIssueProfile(ctx, issueID, actor, &profile.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, countAudit(t, db, "completion.issue_profile_changed"))

	// Clearing the assignment releases the delete guard; the delete is audited.
	_, err = svc.SetIssueProfile(ctx, issueID, actor, nil)
	require.NoError(t, err)
	require.NoError(t, svc.DeleteProfile(ctx, projectID, profile.ID, actor))
	assert.Equal(t, 1, countAudit(t, db, "completion.profile_deleted"))

	// The audit row carries the operator and a timestamp.
	var gotActor int64
	var gotAt sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT actor_user_id, created_at FROM org_audit_log WHERE action = 'completion.settings_updated'`).
		Scan(&gotActor, &gotAt))
	assert.Equal(t, actor, gotActor)
	assert.True(t, gotAt.Valid)
}

func TestCompletionService_PersonalProjectsHaveNoOrgAudit(t *testing.T) {
	ctx := context.Background()
	db := openOrgTestDB(t)
	_, err := db.ExecContext(ctx, `INSERT INTO projects (name, owner_type, owner_id) VALUES ('personal','user',1)`)
	require.NoError(t, err)
	var projectID int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM projects`).Scan(&projectID))
	svc := NewCompletionService(db, store.New(db))

	_, err = svc.UpdateSettings(ctx, projectID, 1, CompletionSettingsInput{MaxRounds: int64Ptr(6)})
	require.NoError(t, err)
	assert.Equal(t, 0, countAudit(t, db, "completion.settings_updated"))
}

func TestCreateProject_SeedsCompletionDefaults(t *testing.T) {
	ctx := context.Background()
	db := openOrgTestDB(t)
	q := store.New(db)
	kanban := NewKanbanService(db, q, NewIssueActivityService(q), nil, nil)

	project, err := kanban.CreateProjectWithDefaults(ctx, "seeded", "", "user", 1)
	require.NoError(t, err)

	settings, err := q.GetCompletionSettings(ctx, project.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(20), settings.MaxRounds)
	profiles, err := q.ListCompletionProfiles(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, profiles, 4)

	// The seeded row resolves as the project default for a fresh issue.
	col, err := q.GetColumn(ctx, mustFirstColumn(t, q, project.ID))
	require.NoError(t, err)
	res, err := db.ExecContext(ctx, `INSERT INTO issues (column_id, title, position) VALUES (?, 'fresh', 0)`, col.ID)
	require.NoError(t, err)
	issueID, err := res.LastInsertId()
	require.NoError(t, err)

	svc := NewCompletionService(db, q)
	effective, err := svc.ResolveIssuePolicy(ctx, issueID)
	require.NoError(t, err)
	assert.Equal(t, completion.SourceProjectDefault, effective.Source)
	assert.Equal(t, int64(20), effective.MaxRounds)
}

func TestCreateProjectWithColumns_SeedsCompletionDefaults(t *testing.T) {
	ctx := context.Background()
	db := openOrgTestDB(t)
	q := store.New(db)
	kanban := NewKanbanService(db, q, NewIssueActivityService(q), nil, nil)

	project, err := kanban.CreateProjectWithColumns(ctx, "blueprinted", "", "user", 1, defaultColumnSeeds())
	require.NoError(t, err)
	profiles, err := q.ListCompletionProfiles(ctx, project.ID)
	require.NoError(t, err)
	assert.Len(t, profiles, 4)
}

func mustFirstColumn(t *testing.T, q *store.Queries, projectID int64) int64 {
	t.Helper()
	cols, err := q.ListColumnsByProject(context.Background(), projectID)
	require.NoError(t, err)
	require.NotEmpty(t, cols)
	return cols[0].ID
}
