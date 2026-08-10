package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// setupHarnessTestDB mirrors setupEnvPresetTestDB. Kept local so a single
// failing test is not coupled to other suites' fixtures.
func setupHarnessTestDB(t *testing.T) *sql.DB {
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

// TestHarnessSeedDefaults_PersistsRowsWithOwnerSentinel guards the regression
// where SeedDefaults built CreateHarnessSpecParams without OwnerType/OwnerID,
// causing the CHECK (owner_type IN ('user','org')) to fire on the very first
// spec ("commit/conventional-commits") and the entire seed loop to abort.
//
// On a fresh DB:
//   - SeedDefaults must succeed (no CHECK violation).
//   - All harness.DefaultSpecs() rows must be present.
//   - Every seeded row must carry the system-default owner sentinel
//     ('user', 0) so it satisfies the constraint while remaining
//     globally addressable via ListHarnessSpecsForOwners.
func TestHarnessSeedDefaults_PersistsRowsWithOwnerSentinel(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	require.NoError(t, svc.SeedDefaults(ctx),
		"SeedDefaults must succeed; previous regression failed on CHECK constraint")

	rows, err := q.ListGlobalHarnessSpecs(ctx)
	require.NoError(t, err)

	wantNames := make(map[string]bool, len(harness.DefaultSpecs()))
	for _, d := range harness.DefaultSpecs() {
		wantNames[d.Category+"/"+d.Name] = true
	}
	gotNames := make(map[string]bool, len(rows))
	for _, r := range rows {
		gotNames[r.Category+"/"+r.Name] = true
	}
	require.Equal(t, wantNames, gotNames, "every DefaultSpec must land in the DB")
}

// TestHarnessSeedDefaults_Idempotent ensures the existing skip-when-non-empty
// guard still holds — running SeedDefaults twice must not duplicate rows.
func TestHarnessSeedDefaults_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	require.NoError(t, svc.SeedDefaults(ctx))
	first, err := q.ListGlobalHarnessSpecs(ctx)
	require.NoError(t, err)
	firstCount := len(first)
	require.NotZero(t, firstCount, "first SeedDefaults should produce rows")

	require.NoError(t, svc.SeedDefaults(ctx))
	second, err := q.ListGlobalHarnessSpecs(ctx)
	require.NoError(t, err)
	require.Equalf(t, firstCount, len(second),
		"second SeedDefaults must not duplicate rows; got %d→%d", firstCount, len(second))
}

// TestCanAccessHarnessSpec_SystemDefaultIsWritable mirrors env preset
// behavior: seeded global harness specs use the system-default owner sentinel
// (user,0), but personal-edition callers resolve to a real user id. The
// settings page must be able to toggle/edit those defaults without hitting
// the generic personal-owner check.
func TestCanAccessHarnessSpec_SystemDefaultIsWritable(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewHarnessService(q, authz)

	require.NoError(t, svc.SeedDefaults(ctx))

	rows, err := q.ListGlobalHarnessSpecs(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, rows)

	var branchName store.HarnessSpec
	for _, r := range rows {
		if r.Category == "commit" && r.Name == "branch-name" {
			branchName = r
			break
		}
	}
	require.NotZero(t, branchName.ID, "branch-name default not seeded")

	const localUserID int64 = 1
	owner, err := authz.CanAccessHarnessSpec(ctx, localUserID, branchName.ID)
	require.NoError(t, err, "local user must be allowed to access system-default harness spec for edit/delete")
	require.Equal(t, "user", owner.Type)
	require.EqualValues(t, 0, owner.ID)
}

func TestHarnessResolveForProject_ReturnsGlobalSpecsOnly(t *testing.T) {
	ctx := context.Background()
	db := setupHarnessTestDB(t)
	q := store.New(db)
	svc := NewHarnessService(q, nil)

	_, err := db.Exec(`INSERT INTO users (id, username, password_hash, role) VALUES (1, 'user-a', 'x', 'admin')`)
	require.NoError(t, err)
	projectID := int64(1)
	_, err = db.Exec(`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (?, 'proj-a', 'user', 1)`, projectID)
	require.NoError(t, err)

	_, err = q.CreateHarnessSpec(ctx, store.CreateHarnessSpecParams{
		Category:  "commit",
		Name:      "global-only",
		Enabled:   1,
		Severity:  "warning",
		Config:    "{}",
		Kind:      harness.KindRegexMatch,
		Target:    harness.TargetCommitMessage,
		Pattern:   "^feat:",
		FilePaths: "[]",
		TriggerOn: harness.TriggerPhaseExit,
	})
	require.NoError(t, err)
	specs, err := svc.ResolveForProject(ctx, &projectID)
	require.NoError(t, err)
	require.Len(t, specs, 1)
	require.Equal(t, "global-only", specs[0].Name)
}
