package service_test

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// intakeSourceKindCount mirrors service.intakeSourceKinds; the settings view
// renders one row per kind, and seeding must produce the same set.
const intakeSourceKindCount = 5

func modeByKind(rows []store.IntakeSetting) map[string]string {
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.SourceKind] = r.Mode
	}
	return out
}

func TestEnsureIntakeProjectDefaultsSeedsObserveRows(t *testing.T) {
	ctx := context.Background()
	db := testutil.SetupTestDB(t)
	wrapped := store.Wrap(db)
	q := store.NewQueries(db)

	p, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "p", OwnerType: "user"})
	require.NoError(t, err)

	require.NoError(t, service.EnsureIntakeProjectDefaults(ctx, wrapped, p.ID))
	rows, err := q.ListIntakeSettingsByProject(ctx, p.ID)
	require.NoError(t, err)
	require.Len(t, rows, intakeSourceKindCount)
	for _, r := range rows {
		assert.Equal(t, "observe", r.Mode, "source %s", r.SourceKind)
		assert.EqualValues(t, 4, r.DailyCap, "source %s", r.SourceKind)
		assert.JSONEq(t, "[]", r.AllowListJson)
		assert.JSONEq(t, "[]", r.BotListJson)
	}

	// Idempotent, and a saved mode is never overwritten.
	_, err = db.ExecContext(ctx,
		`UPDATE intake_settings SET mode = 'auto' WHERE project_id = ? AND source_kind = 'im_bot'`, p.ID)
	require.NoError(t, err)
	require.NoError(t, service.EnsureIntakeProjectDefaults(ctx, wrapped, p.ID))

	rows, err = q.ListIntakeSettingsByProject(ctx, p.ID)
	require.NoError(t, err)
	require.Len(t, rows, intakeSourceKindCount)
	assert.Equal(t, "auto", modeByKind(rows)["im_bot"])
	assert.Equal(t, "observe", modeByKind(rows)["github"])

	// A project-scoped seed must not touch another project.
	other, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "other", OwnerType: "user"})
	require.NoError(t, err)
	otherRows, err := q.ListIntakeSettingsByProject(ctx, other.ID)
	require.NoError(t, err)
	assert.Empty(t, otherRows)
}

func TestBackfillIntakeProjectDefaultsSeedsExistingProjects(t *testing.T) {
	ctx := context.Background()
	db := testutil.SetupTestDB(t)
	wrapped := store.Wrap(db)
	q := store.NewQueries(db)

	p1, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "p1", OwnerType: "user"})
	require.NoError(t, err)
	p2, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "p2", OwnerType: "user"})
	require.NoError(t, err)

	require.NoError(t, service.BackfillIntakeProjectDefaults(ctx, wrapped))

	for _, id := range []int64{p1.ID, p2.ID} {
		rows, err := q.ListIntakeSettingsByProject(ctx, id)
		require.NoError(t, err)
		require.Len(t, rows, intakeSourceKindCount, "project %d", id)
		for _, r := range rows {
			assert.Equal(t, "observe", r.Mode, "project %d source %s", id, r.SourceKind)
		}
	}
}
