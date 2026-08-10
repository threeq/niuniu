package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// TestProjectUniqueViolationIsClassified guards the TOCTOU hardening in
// CreateProjectWithDefaults / CreateProjectWithColumns. The owner-scoped
// pre-check is advisory; the per-owner DB unique index
// (idx_projects_owner_name_unique) is the real source of truth. On a concurrent
// create the loser's INSERT — not the pre-check — rejects the duplicate, and the
// create methods rely on isUniqueViolation to translate that raw DB error into
// ErrProjectNameExists (a clean "name taken") instead of leaking a 500.
//
// This test proves both premises deterministically by going straight to the
// store (bypassing the service pre-check, exactly as a race would):
//  1. the index rejects a same-owner duplicate INSERT, and
//  2. isUniqueViolation classifies that error.
func TestProjectUniqueViolationIsClassified(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)

	q := store.New(db)
	ctx := context.Background()
	params := store.CreateProjectParams{Name: "牛牛助手", OwnerType: "org", OwnerID: 7}

	_, err = q.CreateProject(ctx, params)
	require.NoError(t, err)

	// Same (owner_type, owner_id, name) — the index must reject it, and the
	// helper the create methods depend on must recognize the violation.
	_, err = q.CreateProject(ctx, params)
	require.Error(t, err, "per-owner unique index must reject a same-owner duplicate")
	assert.True(t, isUniqueViolation(err),
		"isUniqueViolation must classify the duplicate so create maps it to ErrProjectNameExists; got: %v", err)

	// A different owner reusing the name must still be allowed at the DB level.
	_, err = q.CreateProject(ctx, store.CreateProjectParams{Name: "牛牛助手", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err, "a different owner must be able to reuse the name")
}
