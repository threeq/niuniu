package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// setupEnvPresetTestDB mirrors workspace_test.go's setupTestDB but kept local
// to env_preset_test.go so a single failing test isn't dependent on workspace
// fixtures. Same in-memory SQLite + ApplySchema + Migrate setup.
func setupEnvPresetTestDB(t *testing.T) *sql.DB {
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

// TestSeedDefaults_InsertsAllProviders asserts every name in the defaults
// slice lands in the DB on a fresh install. The six providers (智谱, MiniMax,
// DeepSeek, 通义千问, Kimi, 火山方舟) are the contract — adding/removing one
// should surface here so an accidental drop in env_preset.go is caught.
func TestSeedDefaults_InsertsAllProviders(t *testing.T) {
	ctx := context.Background()
	db := setupEnvPresetTestDB(t)
	q := store.New(db)
	svc := NewEnvPresetService(q, db, nil)

	require.NoError(t, svc.SeedDefaults(ctx))

	got, err := svc.List(ctx)
	require.NoError(t, err)
	names := make(map[string]store.EnvPreset, len(got))
	for _, p := range got {
		names[p.Name] = p
	}
	for _, want := range []string{"智谱", "MiniMax", "DeepSeek", "通义千问", "Kimi", "火山方舟"} {
		_, ok := names[want]
		require.Truef(t, ok, "expected default preset %q to be seeded; got %v", want, keysOf(names))
	}
}

// TestSeedDefaults_Idempotent guards the existing `existing[d.name]` skip path:
// running SeedDefaults twice must not duplicate rows. UNIQUE(name) would
// surface as an error from CreateEnvPreset; the skip path swallows that case
// up front so concurrent server starts (or the planned per-owner re-seed)
// don't fight the schema.
func TestSeedDefaults_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := setupEnvPresetTestDB(t)
	q := store.New(db)
	svc := NewEnvPresetService(q, db, nil)

	require.NoError(t, svc.SeedDefaults(ctx))
	first, err := svc.List(ctx)
	require.NoError(t, err)
	firstCount := len(first)

	require.NoError(t, svc.SeedDefaults(ctx))
	second, err := svc.List(ctx)
	require.NoError(t, err)
	require.Equalf(t, firstCount, len(second), "second SeedDefaults must not insert duplicates; got %d→%d rows", firstCount, len(second))
}

// TestSeedDefaults_PreservesUserEdits confirms that editing the env JSON of a
// seeded preset survives a subsequent SeedDefaults call. The skip-by-name
// guard is the only thing protecting user customizations on every server
// restart; if it ever regresses to "overwrite on match", users would lose
// their edits silently.
func TestSeedDefaults_PreservesUserEdits(t *testing.T) {
	ctx := context.Background()
	db := setupEnvPresetTestDB(t)
	q := store.New(db)
	svc := NewEnvPresetService(q, db, nil)

	require.NoError(t, svc.SeedDefaults(ctx))
	all, err := svc.List(ctx)
	require.NoError(t, err)
	var deepseek store.EnvPreset
	for _, p := range all {
		if p.Name == "DeepSeek" {
			deepseek = p
			break
		}
	}
	require.NotZero(t, deepseek.ID, "DeepSeek preset not found after seed")

	custom := map[string]string{
		"ANTHROPIC_BASE_URL":   "https://api.deepseek.com/anthropic",
		"ANTHROPIC_AUTH_TOKEN": "sk-my-real-key-12345",
	}
	require.NoError(t, svc.Update(ctx, deepseek.ID, deepseek.Name, deepseek.Description, custom))

	require.NoError(t, svc.SeedDefaults(ctx))
	got, err := svc.Get(ctx, deepseek.ID)
	require.NoError(t, err)

	var gotEnv map[string]string
	require.NoError(t, json.Unmarshal([]byte(got.Env), &gotEnv))
	require.Equal(t, "sk-my-real-key-12345", gotEnv["ANTHROPIC_AUTH_TOKEN"], "user-edited token must survive re-seed")
}

// TestSeedDefaults_VisibleViaListForUser confirms the schema/sql-level half
// of the seed contract: a real user (id ≥ 1) with no org memberships still
// sees system-default presets through the per-user filter, because
// ListEnvPresetsForOwners ORs in (owner_type='user' AND owner_id=0).
//
// Without the third disjunct in env_presets_owner_filter.sql the test would
// fail — the user would only see their own personal presets, and the seeded
// system rows would be invisible. A legitimate user-created preset in the
// caller's own scope is also asserted to coexist (system defaults are
// additive, not exclusive).
func TestSeedDefaults_VisibleViaListForUser(t *testing.T) {
	ctx := context.Background()
	db := setupEnvPresetTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewEnvPresetService(q, db, authz)

	require.NoError(t, svc.SeedDefaults(ctx))

	// User id=1 (the auto-increment value the SingleUser/local seed receives
	// in fresh installs) creates a personal preset alongside the system ones.
	const userID int64 = 1
	_, err := svc.Create(ctx, "MyOwn", "personal", map[string]string{"X": "Y"}, "user", userID)
	require.NoError(t, err)

	got, err := svc.ListForUser(ctx, userID)
	require.NoError(t, err)
	names := make(map[string]struct{}, len(got))
	for _, p := range got {
		names[p.Name] = struct{}{}
	}
	for _, want := range []string{"智谱", "MiniMax", "DeepSeek", "通义千问", "Kimi", "火山方舟", "MyOwn"} {
		_, ok := names[want]
		require.Truef(t, ok, "expected preset %q visible to user %d; got %v", want, userID, mapKeys(names))
	}
}

// TestCanAccessEnvPreset_SystemDefaultIsWritable mirrors the SQL-layer
// "globally visible" semantic from env_presets_owner_filter.sql at the authz
// layer: a system-default preset (owner_type='user', owner_id=0) must be
// reachable by any authenticated user so they can edit/delete it.
//
// Regression guard for the personal-edition 403: the local user has id≥1, the
// seeded default sits at owner_id=0, and the generic checkAccess rule
// (owner_type='user' requires owner.ID == userID) would otherwise reject the
// preflight Authz.CanAccessEnvPreset call in the Update/Delete handlers.
func TestCanAccessEnvPreset_SystemDefaultIsWritable(t *testing.T) {
	ctx := context.Background()
	db := setupEnvPresetTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewEnvPresetService(q, db, authz)

	require.NoError(t, svc.SeedDefaults(ctx))

	all, err := svc.List(ctx)
	require.NoError(t, err)
	var deepseek store.EnvPreset
	for _, p := range all {
		if p.Name == "DeepSeek" {
			deepseek = p
			break
		}
	}
	require.NotZero(t, deepseek.ID, "DeepSeek default not seeded")
	require.Equal(t, "user", deepseek.OwnerType)
	require.EqualValues(t, 0, deepseek.OwnerID, "system default must use owner_id=0 sentinel")

	// Personal edition: local user resolves to id=1 (see
	// auth/identity_resolver_test.go). The bug: this returned ErrForbidden.
	const localUserID int64 = 1
	owner, err := authz.CanAccessEnvPreset(ctx, localUserID, deepseek.ID)
	require.NoError(t, err, "local user must be allowed to access system-default preset for edit/delete")
	require.Equal(t, "user", owner.Type)
	require.EqualValues(t, 0, owner.ID)
}

// TestCanAccessEnvPreset_PersonalIsolation locks in that the system-default
// short-circuit does NOT loosen ownership checks for real personal presets:
// UserB still cannot reach UserA's personal env_preset.
func TestCanAccessEnvPreset_PersonalIsolation(t *testing.T) {
	ctx := context.Background()
	db := setupEnvPresetTestDB(t)
	q := store.New(db)
	authz := NewAuthz(q, db)
	svc := NewEnvPresetService(q, db, authz)

	// Seed two real users so owner_id matches a row in users (mirrors the
	// IdentityResolver/local-user shape).
	_, err := db.Exec(`INSERT INTO users (id, username, password_hash, role) VALUES (1, 'user-a', 'x', 'admin')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO users (id, username, password_hash, role) VALUES (2, 'user-b', 'x', 'admin')`)
	require.NoError(t, err)

	mine, err := svc.Create(ctx, "MyOwn", "personal", map[string]string{"X": "Y"}, "user", 1)
	require.NoError(t, err)

	// Owner can access.
	if _, err := authz.CanAccessEnvPreset(ctx, 1, mine.ID); err != nil {
		t.Fatalf("owner should access own preset: %v", err)
	}
	// Stranger must be forbidden — the owner_id=0 short-circuit must not bleed.
	if _, err := authz.CanAccessEnvPreset(ctx, 2, mine.ID); err != ErrForbidden {
		t.Errorf("stranger should be forbidden, got: %v", err)
	}
}

// TestResolveOneShotEnv_MarkedPreset verifies a preset carrying
// OneShotProviderMarker is selected: its provider env is returned, the NIUNIU_*
// marker is stripped, and unmarked presets are ignored.
func TestResolveOneShotEnv_MarkedPreset(t *testing.T) {
	ctx := context.Background()
	db := setupEnvPresetTestDB(t)
	svc := NewEnvPresetService(store.New(db), db, nil)

	_, err := svc.Create(ctx, "智谱-oneshot", "marked", map[string]string{
		OneShotProviderMarker:  "1",
		"ANTHROPIC_AUTH_TOKEN": "zhipu-real-key",
		"ANTHROPIC_BASE_URL":   "https://open.bigmodel.cn/api/anthropic",
	}, "user", 0)
	require.NoError(t, err)
	_, err = svc.Create(ctx, "Other", "unmarked", map[string]string{"FOO": "bar"}, "user", 0)
	require.NoError(t, err)

	env := svc.ResolveOneShotEnv(ctx)
	require.Contains(t, env, "ANTHROPIC_AUTH_TOKEN=zhipu-real-key")
	require.Contains(t, env, "ANTHROPIC_BASE_URL=https://open.bigmodel.cn/api/anthropic")
	for _, e := range env {
		require.NotContains(t, e, "NIUNIU_", "marker control key must not leak into subprocess env: %q", e)
		require.NotContains(t, e, "FOO=bar", "unmarked preset must be ignored")
	}
}

// TestResolveOneShotEnv_NoneMarked returns nil so one-shot helpers fall back to
// the host env.
func TestResolveOneShotEnv_NoneMarked(t *testing.T) {
	ctx := context.Background()
	db := setupEnvPresetTestDB(t)
	svc := NewEnvPresetService(store.New(db), db, nil)

	_, err := svc.Create(ctx, "Plain", "no marker", map[string]string{"ANTHROPIC_AUTH_TOKEN": "k"}, "user", 0)
	require.NoError(t, err)

	require.Nil(t, svc.ResolveOneShotEnv(ctx))
}

// TestResolveOneShotEnv_MultipleMarkedLowestIDWins locks in deterministic
// selection when more than one preset carries the marker.
func TestResolveOneShotEnv_MultipleMarkedLowestIDWins(t *testing.T) {
	ctx := context.Background()
	db := setupEnvPresetTestDB(t)
	svc := NewEnvPresetService(store.New(db), db, nil)

	first, err := svc.Create(ctx, "First", "marked", map[string]string{
		OneShotProviderMarker: "true", "ANTHROPIC_AUTH_TOKEN": "first-key",
	}, "user", 0)
	require.NoError(t, err)
	_, err = svc.Create(ctx, "Second", "marked", map[string]string{
		OneShotProviderMarker: "yes", "ANTHROPIC_AUTH_TOKEN": "second-key",
	}, "user", 0)
	require.NoError(t, err)
	require.NotZero(t, first.ID)

	env := svc.ResolveOneShotEnv(ctx)
	require.Contains(t, env, "ANTHROPIC_AUTH_TOKEN=first-key", "lowest-ID marked preset must win")
	require.NotContains(t, env, "ANTHROPIC_AUTH_TOKEN=second-key")
}

func keysOf(m map[string]store.EnvPreset) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mapKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
