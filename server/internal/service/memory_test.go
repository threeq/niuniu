package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func setupMemoryTestDB(t *testing.T) *sql.DB {
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

func newMemorySvc(t *testing.T) (*MemoryService, context.Context) {
	t.Helper()
	db := setupMemoryTestDB(t)
	return NewMemoryService(store.New(db), db, "claude"), context.Background()
}

func TestMemory_CreateRecordsProvenance(t *testing.T) {
	svc, ctx := newMemorySvc(t)
	wsID := int64(7)
	m, err := svc.Create(ctx, CreateMemoryInput{
		Owner:       OwnerRef{Type: "user", ID: 2},
		WorkspaceID: &wsID,
		MemType:     "decision",
		Title:       "use sqlc tx.Queries()",
		Content:     "never WithTx on pgx path",
		Source:      "mcp",
		SourcePath:  "server/internal/store/dbwrap.go",
	})
	require.NoError(t, err)
	require.Equal(t, "user", m.OwnerType)
	require.Equal(t, int64(2), m.OwnerID)
	require.Equal(t, "decision", m.MemType)
	require.Equal(t, "mcp", m.Source)
	require.Equal(t, "server/internal/store/dbwrap.go", m.SourcePath)
	require.True(t, m.WorkspaceID.Valid && m.WorkspaceID.Int64 == 7)
	require.Equal(t, int64(1), m.Version)
	require.False(t, m.CreatedAt.IsZero())

	// First version snapshot exists.
	vers, err := svc.ListVersions(ctx, m.ID)
	require.NoError(t, err)
	require.Len(t, vers, 1)
	require.Equal(t, int64(1), vers[0].Version)
}

func TestMemory_UpdateWritesVersionsAndBumps(t *testing.T) {
	svc, ctx := newMemorySvc(t)
	m, err := svc.Create(ctx, CreateMemoryInput{
		Owner: OwnerRef{Type: "user", ID: 1}, MemType: "note", Title: "v1 title", Content: "v1",
	})
	require.NoError(t, err)

	_, err = svc.Update(ctx, m.ID, UpdateMemoryInput{MemType: "note", Title: "v2 title", Content: "v2"})
	require.NoError(t, err)
	u3, err := svc.Update(ctx, m.ID, UpdateMemoryInput{MemType: "gotcha", Title: "v3 title", Content: "v3"})
	require.NoError(t, err)

	require.Equal(t, int64(3), u3.Version)
	require.Equal(t, "v3", u3.Content)
	require.Equal(t, "gotcha", u3.MemType)

	vers, err := svc.ListVersions(ctx, m.ID)
	require.NoError(t, err)
	require.Len(t, vers, 3) // v1 (create), v2, v3 — ordered DESC
	require.Equal(t, int64(3), vers[0].Version)
	require.Equal(t, int64(1), vers[2].Version)
}

func TestMemory_RollbackIsForwardOnly(t *testing.T) {
	svc, ctx := newMemorySvc(t)
	m, err := svc.Create(ctx, CreateMemoryInput{
		Owner: OwnerRef{Type: "user", ID: 1}, MemType: "note", Title: "orig", Content: "original content",
	})
	require.NoError(t, err)
	_, err = svc.Update(ctx, m.ID, UpdateMemoryInput{MemType: "note", Title: "edited", Content: "edited content"})
	require.NoError(t, err)

	// Roll back to version 1.
	rolled, err := svc.Rollback(ctx, m.ID, 1)
	require.NoError(t, err)
	require.Equal(t, "original content", rolled.Content)
	require.Equal(t, "orig", rolled.Title)
	require.Equal(t, int64(3), rolled.Version) // forward: 1(create),2(edit),3(rollback)

	vers, err := svc.ListVersions(ctx, m.ID)
	require.NoError(t, err)
	require.Len(t, vers, 3)
}

func TestMemory_SoftDeleteAndRestore(t *testing.T) {
	svc, ctx := newMemorySvc(t)
	owner := OwnerRef{Type: "user", ID: 5}
	m, err := svc.Create(ctx, CreateMemoryInput{Owner: owner, MemType: "note", Title: "ephemeral", Content: "x"})
	require.NoError(t, err)

	require.NoError(t, svc.SoftDelete(ctx, m.ID))
	alive, err := svc.ListForOwner(ctx, owner, "")
	require.NoError(t, err)
	require.Empty(t, alive)

	deleted, err := svc.ListDeleted(ctx, owner)
	require.NoError(t, err)
	require.Len(t, deleted, 1)

	require.NoError(t, svc.Restore(ctx, m.ID))
	alive, err = svc.ListForOwner(ctx, owner, "")
	require.NoError(t, err)
	require.Len(t, alive, 1)
}

func TestMemoryConsolidate_DedupesExactDuplicates(t *testing.T) {
	svc, ctx := newMemorySvc(t)
	owner := OwnerRef{Type: "user", ID: 3}
	// Two EXACT duplicates (same type+title+content) collapse to one.
	_, err := svc.Create(ctx, CreateMemoryInput{Owner: owner, MemType: "gotcha", Title: "pgx 42P18", Content: "anchor every ? to a typed column"})
	require.NoError(t, err)
	_, err = svc.Create(ctx, CreateMemoryInput{Owner: owner, MemType: "gotcha", Title: "pgx 42P18", Content: "anchor every ? to a typed column"})
	require.NoError(t, err)
	_, err = svc.Create(ctx, CreateMemoryInput{Owner: owner, MemType: "note", Title: "design tokens", Content: "z"})
	require.NoError(t, err)

	res, err := svc.ConsolidateForOwner(ctx, owner)
	require.NoError(t, err)
	require.Equal(t, 3, res.Scanned)
	require.Equal(t, 1, res.Deleted)
	require.Equal(t, 2, res.Kept)

	alive, err := svc.ListForOwner(ctx, owner, "")
	require.NoError(t, err)
	require.Len(t, alive, 2)
}

func TestMemoryConsolidate_KeepsSameTitleDifferentContent(t *testing.T) {
	svc, ctx := newMemorySvc(t)
	owner := OwnerRef{Type: "user", ID: 4}
	// Same title, DIFFERENT content -> distinct memories, must NOT be collapsed.
	_, err := svc.Create(ctx, CreateMemoryInput{Owner: owner, MemType: "note", Title: "TODO", Content: "fix auth"})
	require.NoError(t, err)
	_, err = svc.Create(ctx, CreateMemoryInput{Owner: owner, MemType: "note", Title: "TODO", Content: "add tests"})
	require.NoError(t, err)

	res, err := svc.ConsolidateForOwner(ctx, owner)
	require.NoError(t, err)
	require.Equal(t, 0, res.Deleted, "content-distinct memories sharing a title must be kept")

	alive, err := svc.ListForOwner(ctx, owner, "")
	require.NoError(t, err)
	require.Len(t, alive, 2)
}

func TestMemory_SearchAndTypeFilter(t *testing.T) {
	svc, ctx := newMemorySvc(t)
	owner := OwnerRef{Type: "user", ID: 9}
	_, _ = svc.Create(ctx, CreateMemoryInput{Owner: owner, MemType: "gotcha", Title: "pgx parse error", Content: "42P18"})
	_, _ = svc.Create(ctx, CreateMemoryInput{Owner: owner, MemType: "note", Title: "design system", Content: "use tokens"})

	hits, err := svc.Search(ctx, owner, "PGX")
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "pgx parse error", hits[0].Title)

	gotchas, err := svc.ListForOwner(ctx, owner, "gotcha")
	require.NoError(t, err)
	require.Len(t, gotchas, 1)
}

// #254: LIKE wildcards (% and _) in the query must be matched literally, not as
// wildcards. With SQL LIKE these would over-match; Go substring match is literal.
func TestMemory_SearchTreatsWildcardsLiterally(t *testing.T) {
	svc, ctx := newMemorySvc(t)
	owner := OwnerRef{Type: "user", ID: 11}
	_, _ = svc.Create(ctx, CreateMemoryInput{Owner: owner, MemType: "note", Title: "battery at 100%", Content: "x"})
	_, _ = svc.Create(ctx, CreateMemoryInput{Owner: owner, MemType: "note", Title: "var a_b naming", Content: "y"})
	_, _ = svc.Create(ctx, CreateMemoryInput{Owner: owner, MemType: "note", Title: "plain text", Content: "z"})

	// "%" is a wildcard in LIKE; a literal search must match only the "100%" row,
	// not every row.
	pct, err := svc.Search(ctx, owner, "100%")
	require.NoError(t, err)
	require.Len(t, pct, 1)
	require.Equal(t, "battery at 100%", pct[0].Title)

	// "_" is a single-char LIKE wildcard; literal search matches only "a_b".
	under, err := svc.Search(ctx, owner, "a_b")
	require.NoError(t, err)
	require.Len(t, under, 1)
	require.Equal(t, "var a_b naming", under[0].Title)
}
