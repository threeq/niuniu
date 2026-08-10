package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func setupQuickActionTestDB(t *testing.T) *sql.DB {
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

// seededQuickActionSlugs is the contract: every built-in slug SeedDefaults must
// install on a fresh DB (issue #234, expanded to 7 studio actions). Adding or
// removing a default in quick_action.go should surface here so an accidental
// drop is caught.
var seededQuickActionSlugs = []string{
	"studio-deliver",
	"studio-save",
	"studio-continue",
	"studio-discard",
	"studio-sync",
	"studio-review",
	"studio-autopilot",
}

// TestQuickActionSeedDefaults covers the fresh-install contract: all studio
// presets land under the user/0 sentinel, carry the right auto_send flags, are
// dense-positioned in slice order, and surface to an arbitrary caller via the
// owner_id=0 disjunct.
func TestQuickActionSeedDefaults(t *testing.T) {
	ctx := context.Background()
	db := setupQuickActionTestDB(t)
	q := store.New(db)
	svc := NewQuickActionService(q, db, nil)

	require.NoError(t, svc.SeedDefaults(ctx))

	all, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, len(seededQuickActionSlugs))

	bySlug := map[string]store.QuickAction{}
	for _, qa := range all {
		bySlug[qa.Slug] = qa
	}
	for _, slug := range seededQuickActionSlugs {
		qa, ok := bySlug[slug]
		require.Truef(t, ok, "expected default quick action %q seeded", slug)
		require.Equal(t, "user", qa.OwnerType, slug)
		require.EqualValues(t, 0, qa.OwnerID, "system default must use owner_id=0 sentinel: %s", slug)
	}

	// auto_send flags: 放弃改动 (destructive) must not auto-send; the rest do.
	require.EqualValues(t, 0, bySlug["studio-discard"].AutoSend, "放弃改动 must NOT auto-send")
	require.EqualValues(t, 1, bySlug["studio-save"].AutoSend, "保存工作 should auto-send")
	require.EqualValues(t, 1, bySlug["studio-autopilot"].AutoSend, "自主完成工作 should auto-send")

	// Positions are dense in slice order on a fresh install (List orders by
	// position, id).
	for i, qa := range all {
		require.EqualValues(t, i+1, qa.Position, "positions should be dense in seed order")
		require.Equal(t, seededQuickActionSlugs[i], qa.Slug, "seed order must match the slice order")
	}

	// An arbitrary user (id 7, no orgs) sees the sentinel rows via the filter.
	visible, err := q.ListQuickActionsForOwners(ctx, store.ListQuickActionsForOwnersParams{
		OwnerID: 7,
		OrgIds:  []int64{-1},
	})
	require.NoError(t, err)
	require.Len(t, visible, len(seededQuickActionSlugs), "owner_id=0 defaults must be visible to every caller")
}

// TestQuickActionSeedDefaults_Idempotent guards requirement #2: re-running the
// seed must not duplicate rows.
func TestQuickActionSeedDefaults_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := setupQuickActionTestDB(t)
	q := store.New(db)
	svc := NewQuickActionService(q, db, nil)

	require.NoError(t, svc.SeedDefaults(ctx))
	first, err := svc.List(ctx)
	require.NoError(t, err)

	require.NoError(t, svc.SeedDefaults(ctx))
	second, err := svc.List(ctx)
	require.NoError(t, err)

	require.Equalf(t, len(first), len(second),
		"second SeedDefaults must not insert duplicates; got %d→%d", len(first), len(second))
}

// TestQuickActionSeedDefaults_SkipsExistingPreservesEdit is the core of
// requirement #2: on upgrade an already-seeded entry (matched by slug) is not
// re-initialized and a user edit to its content survives a re-seed.
func TestQuickActionSeedDefaults_SkipsExistingPreservesEdit(t *testing.T) {
	ctx := context.Background()
	db := setupQuickActionTestDB(t)
	q := store.New(db)
	svc := NewQuickActionService(q, db, nil)

	require.NoError(t, svc.SeedDefaults(ctx))

	// Find the seeded "保存工作" (studio-save) and edit its content.
	all, err := svc.List(ctx)
	require.NoError(t, err)
	var save store.QuickAction
	for _, qa := range all {
		if qa.Slug == "studio-save" {
			save = qa
			break
		}
	}
	require.NotZero(t, save.ID, "studio-save not seeded")
	_, err = svc.Update(ctx, save.ID, save.Label, "我自己改过的内容", true)
	require.NoError(t, err)

	// Re-seed (simulates a server restart / upgrade).
	require.NoError(t, svc.SeedDefaults(ctx))

	after, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, after, len(seededQuickActionSlugs), "re-seed must not add duplicates")

	var saveAfter store.QuickAction
	for _, qa := range after {
		if qa.Slug == "studio-save" {
			saveAfter = qa
			break
		}
	}
	require.Equal(t, save.ID, saveAfter.ID, "same row, not re-created")
	require.Equal(t, "我自己改过的内容", saveAfter.Content, "user edit must survive re-seed")
}
