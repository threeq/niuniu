package service

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// newEvolveSvc returns a MemoryService, a query handle, the raw DB, and a
// context. Tests need the project handle to create a real project row and the
// raw DB to age specific memories deterministically.
func newEvolveSvc(t *testing.T) (*MemoryService, *store.Queries, *sql.DB, context.Context) {
	t.Helper()
	db := setupMemoryTestDB(t)
	q := store.New(db)
	return NewMemoryService(q, db, "claude"), q, db, context.Background()
}

func newEvolveProject(t *testing.T, q *store.Queries, ctx context.Context) int64 {
	t.Helper()
	p, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "p", OwnerType: "user"})
	require.NoError(t, err)
	return p.ID
}

// backdate makes a memory look untouched since `at` (older created_at, cleared
// reinforcement) so decay logic can be exercised at real "now".
func backdate(t *testing.T, db *sql.DB, id int64, at time.Time) {
	t.Helper()
	_, err := db.Exec(
		"UPDATE memories SET created_at = ?, last_reinforced_at = NULL WHERE id = ?",
		at.UTC(), id)
	require.NoError(t, err)
}

// Reinforce bumps the count and stamps the reaffirmation time.
func TestReinforce_BumpsCountAndTimestamp(t *testing.T) {
	svc, _, _, ctx := newEvolveSvc(t)
	m, err := svc.Create(ctx, CreateMemoryInput{
		Owner: OwnerRef{Type: "user", ID: 1}, MemType: "gotcha", Title: "t", Content: "c", Source: "extract",
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), m.ReinforceCount)
	require.False(t, m.LastReinforcedAt.Valid)

	require.NoError(t, svc.Reinforce(ctx, m.ID, time.Now().Add(-time.Hour)))
	got, err := svc.Get(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), got.ReinforceCount)
	require.True(t, got.LastReinforcedAt.Valid)

	require.NoError(t, svc.Reinforce(ctx, m.ID, time.Now()))
	got, err = svc.Get(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), got.ReinforceCount)
}

// Supersession: a newer insight with the same type+title but different content
// retires the older one and records what replaced it.
func TestEvolve_SupersedesContradictedInsight(t *testing.T) {
	svc, q, _, ctx := newEvolveSvc(t)
	pid := newEvolveProject(t, q, ctx)

	old, err := svc.Create(ctx, CreateMemoryInput{
		Owner: OwnerRef{Type: "user", ID: 0}, ProjectID: &pid,
		MemType: "decision", Title: "auth strategy", Content: "use sessions", Source: "extract",
	})
	require.NoError(t, err)
	// The project changed: a newer extraction records the corrected decision.
	newer, err := svc.Create(ctx, CreateMemoryInput{
		Owner: OwnerRef{Type: "user", ID: 0}, ProjectID: &pid,
		MemType: "decision", Title: "auth strategy", Content: "use JWT", Source: "extract",
	})
	require.NoError(t, err)

	res, err := svc.EvolveForProject(ctx, pid, EvolveOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Superseded)
	require.Equal(t, 0, res.Archived)

	alive, err := svc.ListForProject(ctx, pid, "")
	require.NoError(t, err)
	require.Len(t, alive, 1)
	require.Equal(t, newer.ID, alive[0].ID)
	require.Equal(t, "use JWT", alive[0].Content)

	retired, err := svc.Get(ctx, old.ID)
	require.NoError(t, err)
	require.True(t, retired.SupersededBy.Valid)
	require.Equal(t, newer.ID, retired.SupersededBy.Int64)
	require.True(t, retired.DeletedAt.Valid)
}

// Free-form note/reference titles must NOT be superseded (the same title can mean
// unrelated things, e.g. two different "TODO" notes).
func TestEvolve_DoesNotSupersedeFreeformNotes(t *testing.T) {
	svc, q, _, ctx := newEvolveSvc(t)
	pid := newEvolveProject(t, q, ctx)
	for _, c := range []string{"fix auth", "add tests"} {
		_, err := svc.Create(ctx, CreateMemoryInput{
			Owner: OwnerRef{Type: "user", ID: 0}, ProjectID: &pid,
			MemType: "note", Title: "TODO", Content: c, Source: "extract",
		})
		require.NoError(t, err)
	}
	res, err := svc.EvolveForProject(ctx, pid, EvolveOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, res.Superseded)
	alive, err := svc.ListForProject(ctx, pid, "")
	require.NoError(t, err)
	require.Len(t, alive, 2)
}

// Exact duplicates (same content) are left to ConsolidateForOwner, not superseded.
func TestEvolve_LeavesExactDuplicatesForConsolidate(t *testing.T) {
	svc, q, _, ctx := newEvolveSvc(t)
	pid := newEvolveProject(t, q, ctx)
	for i := 0; i < 2; i++ {
		_, err := svc.Create(ctx, CreateMemoryInput{
			Owner: OwnerRef{Type: "user", ID: 0}, ProjectID: &pid,
			MemType: "pattern", Title: "layout", Content: "one file per domain", Source: "extract",
		})
		require.NoError(t, err)
	}
	res, err := svc.EvolveForProject(ctx, pid, EvolveOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, res.Superseded, "identical duplicates are consolidate's job, not supersession")
}

// Decay: a machine-captured memory untouched past the horizon is archived.
func TestEvolve_ArchivesStaleMemory(t *testing.T) {
	svc, q, db, ctx := newEvolveSvc(t)
	pid := newEvolveProject(t, q, ctx)
	m, err := svc.Create(ctx, CreateMemoryInput{
		Owner: OwnerRef{Type: "user", ID: 0}, ProjectID: &pid,
		MemType: "gotcha", Title: "stale insight", Content: "x", Source: "extract",
	})
	require.NoError(t, err)
	backdate(t, db, m.ID, time.Now().Add(-100*24*time.Hour))

	res, err := svc.EvolveForProject(ctx, pid, EvolveOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Archived)

	alive, err := svc.ListForProject(ctx, pid, "")
	require.NoError(t, err)
	require.Empty(t, alive)
}

// Decay protections: manual (human) memories, sufficiently reinforced memories,
// and still-fresh memories are never auto-archived — only an aged, unreinforced
// machine memory is.
func TestEvolve_DecayProtections(t *testing.T) {
	svc, q, db, ctx := newEvolveSvc(t)
	pid := newEvolveProject(t, q, ctx)
	oldT := time.Now().Add(-100 * 24 * time.Hour)

	manual, err := svc.Create(ctx, CreateMemoryInput{
		Owner: OwnerRef{Type: "user", ID: 0}, ProjectID: &pid,
		MemType: "decision", Title: "manual rule", Content: "keep", Source: "manual",
	})
	require.NoError(t, err)
	backdate(t, db, manual.ID, oldT)

	reinforced, err := svc.Create(ctx, CreateMemoryInput{
		Owner: OwnerRef{Type: "user", ID: 0}, ProjectID: &pid,
		MemType: "pattern", Title: "proven pattern", Content: "keep", Source: "extract",
	})
	require.NoError(t, err)
	backdate(t, db, reinforced.ID, oldT)
	// Reinforced twice with old timestamps: aged, but count=2 >= protect.
	require.NoError(t, svc.Reinforce(ctx, reinforced.ID, oldT))
	require.NoError(t, svc.Reinforce(ctx, reinforced.ID, oldT))

	fresh, err := svc.Create(ctx, CreateMemoryInput{
		Owner: OwnerRef{Type: "user", ID: 0}, ProjectID: &pid,
		MemType: "gotcha", Title: "fresh note", Content: "keep", Source: "extract",
	})
	require.NoError(t, err)

	decayed, err := svc.Create(ctx, CreateMemoryInput{
		Owner: OwnerRef{Type: "user", ID: 0}, ProjectID: &pid,
		MemType: "error_fix", Title: "aged fix", Content: "drop", Source: "extract",
	})
	require.NoError(t, err)
	backdate(t, db, decayed.ID, oldT)

	res, err := svc.EvolveForProject(ctx, pid, EvolveOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Archived)

	alive, err := svc.ListForProject(ctx, pid, "")
	require.NoError(t, err)
	aliveIDs := map[int64]bool{}
	for _, m := range alive {
		aliveIDs[m.ID] = true
	}
	require.True(t, aliveIDs[manual.ID], "manual memory must survive decay")
	require.True(t, aliveIDs[reinforced.ID], "reinforced memory must survive decay")
	require.True(t, aliveIDs[fresh.ID], "fresh memory must survive decay")
	require.False(t, aliveIDs[decayed.ID], "aged unreinforced machine memory should be archived")
}

// Integration: a full evolve cycle as the project changes — an insight is
// corrected (superseded), an unused one is archived, an actively-reinforced one
// survives, the freshly-corrected one survives — and the result is reversible and
// reflected in the agent-facing generated file.
func TestEvolve_FullCycleReflectsProjectChange(t *testing.T) {
	svc, q, db, ctx := newEvolveSvc(t)
	pid := newEvolveProject(t, q, ctx)
	owner := OwnerRef{Type: "user", ID: 0}
	oldT := time.Now().Add(-100 * 24 * time.Hour)

	// Knowledge captured early in the project (all aged).
	oldDecision, err := svc.Create(ctx, CreateMemoryInput{
		Owner: owner, ProjectID: &pid, MemType: "decision",
		Title: "storage", Content: "use local files", Source: "extract",
	})
	require.NoError(t, err)
	backdate(t, db, oldDecision.ID, oldT)

	stale, err := svc.Create(ctx, CreateMemoryInput{
		Owner: owner, ProjectID: &pid, MemType: "gotcha",
		Title: "obsolete workaround", Content: "patch the build", Source: "extract",
	})
	require.NoError(t, err)
	backdate(t, db, stale.ID, oldT)

	durable, err := svc.Create(ctx, CreateMemoryInput{
		Owner: owner, ProjectID: &pid, MemType: "pattern",
		Title: "layering", Content: "handler -> service -> store", Source: "extract",
	})
	require.NoError(t, err)
	backdate(t, db, durable.ID, oldT)

	// The project changes: storage decision is corrected (fresh), and the durable
	// pattern keeps being reaffirmed across later sessions.
	newDecision, err := svc.Create(ctx, CreateMemoryInput{
		Owner: owner, ProjectID: &pid, MemType: "decision",
		Title: "storage", Content: "use SQLite", Source: "extract",
	})
	require.NoError(t, err)
	require.NoError(t, svc.Reinforce(ctx, durable.ID, time.Now()))
	require.NoError(t, svc.Reinforce(ctx, durable.ID, time.Now()))

	res, err := svc.EvolveForProject(ctx, pid, EvolveOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, res.Superseded)
	require.Equal(t, 1, res.Archived)

	alive, err := svc.ListForProject(ctx, pid, "")
	require.NoError(t, err)
	aliveIDs := map[int64]bool{}
	for _, m := range alive {
		aliveIDs[m.ID] = true
	}
	require.True(t, aliveIDs[newDecision.ID], "corrected decision survives")
	require.True(t, aliveIDs[durable.ID], "reinforced pattern survives")
	require.False(t, aliveIDs[oldDecision.ID], "superseded decision retired")
	require.False(t, aliveIDs[stale.ID], "stale gotcha archived")

	// The agent-facing file reflects only the evolved (current) knowledge.
	dir := t.TempDir()
	path := svc.GenerateMemoryFile(ctx, pid, dir)
	require.NotEmpty(t, path)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(data)
	require.Contains(t, content, "use SQLite")
	require.NotContains(t, content, "use local files")
	require.NotContains(t, content, "obsolete workaround")
	require.Contains(t, content, "layering")

	// Reversible: evolution only soft-deletes, so a retired memory can be restored.
	require.NoError(t, svc.Restore(ctx, stale.ID))
	restored, err := svc.Get(ctx, stale.ID)
	require.NoError(t, err)
	require.False(t, restored.DeletedAt.Valid)

	// Idempotent supersession: a second pass makes no further supersessions.
	res2, err := svc.EvolveForProject(ctx, pid, EvolveOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, res2.Superseded, "no duplicate supersession on re-run")
}
