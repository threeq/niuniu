package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func setupModelSeedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	store.Driver = "sqlite"
	require.NoError(t, store.ApplySchema(db))
	store.Migrate(db)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func countBuiltinModels(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM models WHERE owner_type='user' AND owner_id=0`).Scan(&n))
	return n
}

// TestSeedBuiltinModels_InsertsAllBuiltins pins the seed contract: every name in
// builtinModels lands as an owner_id=0 builtin row with source='builtin'.
func TestSeedBuiltinModels_InsertsAllBuiltins(t *testing.T) {
	ctx := context.Background()
	db := setupModelSeedTestDB(t)
	q := store.New(db)

	require.NoError(t, SeedBuiltinModels(ctx, q))
	require.Equal(t, len(builtinModels), countBuiltinModels(t, db))

	for _, name := range builtinModels {
		m, err := q.GetModelByName(ctx, store.GetModelByNameParams{
			OwnerType: "user", OwnerID: 0, Name: name,
		})
		require.NoError(t, err)
		require.Equal(t, "builtin", m.Source)
		require.Equal(t, name, m.DisplayName)
		require.Equal(t, int64(1), m.Enabled)
	}
}

// TestSeedBuiltinModels_Idempotent runs the seed twice: the unique key must hold
// and no duplicate rows may appear (this is what makes it safe on every boot).
func TestSeedBuiltinModels_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := setupModelSeedTestDB(t)
	q := store.New(db)

	require.NoError(t, SeedBuiltinModels(ctx, q))
	require.NoError(t, SeedBuiltinModels(ctx, q))
	require.Equal(t, len(builtinModels), countBuiltinModels(t, db))
}

// TestSeedBuiltinModels_PreservesExistingEdits proves a re-seed never overwrites
// an existing row: a renamed/disabled builtin stays exactly as it was.
func TestSeedBuiltinModels_PreservesExistingEdits(t *testing.T) {
	ctx := context.Background()
	db := setupModelSeedTestDB(t)
	q := store.New(db)
	require.NoError(t, SeedBuiltinModels(ctx, q))

	_, err := db.Exec(
		`UPDATE models SET display_name = ?, enabled = 0
		 WHERE owner_type = 'user' AND owner_id = 0 AND name = ?`,
		"Custom GLM", "glm-5.1")
	require.NoError(t, err)

	require.NoError(t, SeedBuiltinModels(ctx, q))

	m, err := q.GetModelByName(ctx, store.GetModelByNameParams{
		OwnerType: "user", OwnerID: 0, Name: "glm-5.1",
	})
	require.NoError(t, err)
	require.Equal(t, "Custom GLM", m.DisplayName)
	require.Equal(t, int64(0), m.Enabled)
	require.Equal(t, len(builtinModels), countBuiltinModels(t, db))
}
