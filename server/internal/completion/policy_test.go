package completion_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/completion"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func openCompletionTestDB(t *testing.T) (*sql.DB, *store.Queries) {
	t.Helper()
	store.Driver = "sqlite"
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	return db, store.New(db)
}

// seedIssue creates project 1 (default column 1) plus the given issue id.
func seedIssue(t *testing.T, db *sql.DB, issueID int64) {
	t.Helper()
	if issueID == 1 {
		_, err := db.Exec(`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1,'p1','user',1)`)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO columns (id, project_id, name, position) VALUES (1,1,'todo',0)`)
		require.NoError(t, err)
	}
	_, err := db.Exec(`INSERT INTO issues (id, column_id, title, position) VALUES (?, 1, 'task', 0)`, issueID)
	require.NoError(t, err)
}

func TestResolver_ProjectDefaultAndNullInheritance(t *testing.T) {
	ctx := context.Background()
	db, q := openCompletionTestDB(t)
	seedIssue(t, db, 1)
	r := completion.NewResolver(q)
	issue, err := q.GetIssue(ctx, 1)
	require.NoError(t, err)

	// No settings row: schema defaults.
	got, err := r.Resolve(ctx, issue)
	require.NoError(t, err)
	assert.Equal(t, completion.DefaultPolicy(), got.Policy)
	assert.Equal(t, completion.SourceProjectDefault, got.Source)
	assert.Nil(t, got.ProfileID)

	// Stored settings row wins over the schema defaults.
	_, err = q.UpsertCompletionSettings(ctx, store.UpsertCompletionSettingsParams{
		ProjectID: 1, AutohostEnabled: 1, MaxRounds: 7, TokenBudget: 12_000,
		StopOnDecision: 0, RequireReview: 1,
	})
	require.NoError(t, err)
	got, err = r.Resolve(ctx, issue)
	require.NoError(t, err)
	assert.Equal(t, completion.Policy{
		AutohostEnabled: true, MaxRounds: 7, TokenBudget: 12_000,
		StopOnDecision: false, RequireReview: true,
	}, got.Policy)
	assert.Equal(t, completion.SourceProjectDefault, got.Source)
}

func TestResolver_IssueProfileWinsAndChangeAppliesNextTurn(t *testing.T) {
	ctx := context.Background()
	db, q := openCompletionTestDB(t)
	seedIssue(t, db, 1)
	require.NoError(t, completion.EnsureProjectDefaults(context.Background(), store.Wrap(db), 1))
	r := completion.NewResolver(q)

	issue, err := q.GetIssue(ctx, 1)
	require.NoError(t, err)
	got, err := r.Resolve(ctx, issue)
	require.NoError(t, err)
	assert.Equal(t, completion.SourceProjectDefault, got.Source, "NULL profile inherits the project default")

	// Assign 快速 (3 rounds / 20K) — a profile beats the project default.
	var fastID int64
	for _, p := range mustProfiles(t, q) {
		if p.Name == "快速" {
			fastID = p.ID
		}
	}
	require.NotZero(t, fastID)
	issue, err = q.SetIssueCompletionProfile(ctx, store.SetIssueCompletionProfileParams{
		CompletionProfileID: sql.NullInt64{Int64: fastID, Valid: true},
		ID:                  1,
	})
	require.NoError(t, err)

	got, err = r.Resolve(ctx, issue)
	require.NoError(t, err)
	assert.Equal(t, completion.SourceIssueProfile, got.Source)
	assert.Equal(t, "快速", got.ProfileName)
	require.NotNil(t, got.ProfileID)
	assert.Equal(t, fastID, *got.ProfileID)
	assert.Equal(t, int64(3), got.MaxRounds)
	assert.Equal(t, int64(20_000), got.TokenBudget)

	// Editing the profile is visible on the very next Resolve call — same
	// resolver instance, no restart/cache invalidation involved.
	_, err = q.UpdateCompletionProfile(ctx, store.UpdateCompletionProfileParams{
		Name: "快速", AutohostEnabled: 1, MaxRounds: 9, TokenBudget: 50_000,
		StopOnDecision: 1, RequireReview: 0, ID: fastID,
	})
	require.NoError(t, err)
	got, err = r.Resolve(ctx, issue)
	require.NoError(t, err)
	assert.Equal(t, int64(9), got.MaxRounds)

	// Clearing the override returns to inheritance on the next turn.
	issue, err = q.SetIssueCompletionProfile(ctx, store.SetIssueCompletionProfileParams{
		CompletionProfileID: sql.NullInt64{},
		ID:                  1,
	})
	require.NoError(t, err)
	got, err = r.Resolve(ctx, issue)
	require.NoError(t, err)
	assert.Equal(t, completion.SourceProjectDefault, got.Source)
	assert.Equal(t, int64(20), got.MaxRounds)
}

func TestEnsureProjectDefaults_Idempotent(t *testing.T) {
	ctx := context.Background()
	db, q := openCompletionTestDB(t)
	seedIssue(t, db, 1)

	require.NoError(t, completion.EnsureProjectDefaults(ctx, store.Wrap(db), 1))
	require.NoError(t, completion.EnsureProjectDefaults(ctx, store.Wrap(db), 1))

	settings, err := q.GetCompletionSettings(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(20), settings.MaxRounds)

	profiles := mustProfiles(t, q)
	require.Len(t, profiles, 4)
	names := []string{profiles[0].Name, profiles[1].Name, profiles[2].Name, profiles[3].Name}
	assert.ElementsMatch(t, []string{"标准", "快速", "深度", "高风险需审查"}, names)
}

func TestBackfillProjectDefaults_OnlyUninitializedProjects(t *testing.T) {
	ctx := context.Background()
	db, q := openCompletionTestDB(t)
	seedIssue(t, db, 1)
	// Project 2 was already configured: a settings row exists but no profiles.
	_, err := db.Exec(`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (2,'p2','user',1)`)
	require.NoError(t, err)
	_, err = q.UpsertCompletionSettings(ctx, store.UpsertCompletionSettingsParams{
		ProjectID: 2, AutohostEnabled: 1, MaxRounds: 4, StopOnDecision: 1,
	})
	require.NoError(t, err)

	require.NoError(t, completion.BackfillProjectDefaults(ctx, store.Wrap(db)))
	require.NoError(t, completion.BackfillProjectDefaults(ctx, store.Wrap(db)))

	// Legacy project 1 got a settings row + four profiles.
	_, err = q.GetCompletionSettings(ctx, 1)
	require.NoError(t, err)
	assert.Len(t, mustProfilesFor(t, q, 1), 4)

	// Project 2 keeps its own settings and gains no profiles.
	settings, err := q.GetCompletionSettings(ctx, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(4), settings.MaxRounds)
	assert.Empty(t, mustProfilesFor(t, q, 2))
}

func mustProfiles(t *testing.T, q *store.Queries) []store.CompletionProfile {
	t.Helper()
	rows, err := q.ListCompletionProfiles(context.Background(), 1)
	require.NoError(t, err)
	return rows
}

func mustProfilesFor(t *testing.T, q *store.Queries, projectID int64) []store.CompletionProfile {
	t.Helper()
	rows, err := q.ListCompletionProfiles(context.Background(), projectID)
	require.NoError(t, err)
	return rows
}
