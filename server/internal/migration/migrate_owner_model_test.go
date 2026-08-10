package migration

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

func TestSlugifyCJK(t *testing.T) {
	// CJK names should produce a stable hash-based slug rather than "default".
	name := "日本チーム"
	slug := slugify(name)
	if slug == "default" {
		t.Errorf("slugify(%q) = %q; want hash-based slug, not 'default'", name, slug)
	}
	if !strings.HasPrefix(slug, "org-") {
		t.Errorf("slugify(%q) = %q; want prefix 'org-'", name, slug)
	}
	// Must be stable (deterministic).
	slug2 := slugify(name)
	if slug != slug2 {
		t.Errorf("slugify is not deterministic: %q != %q", slug, slug2)
	}
	// Two distinct CJK names should produce different slugs.
	slug3 := slugify("中国团队")
	if slug == slug3 {
		t.Errorf("distinct CJK names produced same slug %q", slug)
	}
	// ASCII names should still work as before.
	if got := slugify("My Org"); got != "my-org" {
		t.Errorf("slugify('My Org') = %q; want 'my-org'", got)
	}
	if got := slugify(""); got == "default" {
		// Empty name falls back to hash of empty string — that's acceptable.
		// But it should not panic.
		_ = got
	}
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatal(err)
	}
	store.Migrate(db)
	return db
}

func TestResolveTargetOwnerAuthOff(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Enabled:    false,
			SingleUser: config.UserConfig{Username: "local"},
		},
	}
	owner, err := ResolveTargetOwner(context.Background(), store.Wrap(db), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Type != "user" {
		t.Errorf("type = %q; want user", owner.Type)
	}
	if owner.ID <= 0 {
		t.Errorf("id = %d; want > 0 (seed user should exist)", owner.ID)
	}
}

func TestResolveTargetOwnerAuthOnDefaultOrg(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	_, err := db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('alice', 'x', 'admin')`)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Auth: config.AuthConfig{Enabled: true}}
	owner, err := ResolveTargetOwner(context.Background(), store.Wrap(db), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Type != "org" {
		t.Errorf("type = %q; want org", owner.Type)
	}
	if owner.ID <= 0 {
		t.Errorf("org id = %d; want > 0", owner.ID)
	}
}

func TestMigrateOwnerModelMovesWorkspaceAndUpdatesRows(t *testing.T) {
	tmp := t.TempDir()
	db := openDB(t)
	defer db.Close()

	_, err := db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('alice', 'x', 'admin')`)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(tmp, "workspaces", "42")
	writeFile(t, filepath.Join(legacyPath, "README"), "legacy ws")
	_, err = db.Exec(`INSERT INTO workspaces (id, name, path, status) VALUES (42, 'ws', ?, 'created')`, legacyPath)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Auth:    config.AuthConfig{Enabled: true},
		DataDir: tmp,
	}
	if err := MigrateOwnerModel(context.Background(), db, cfg, tmp); err != nil {
		t.Fatal(err)
	}

	var newPath string
	var ownerType string
	var ownerID int64
	if err := db.QueryRow(`SELECT path, owner_type, owner_id FROM workspaces WHERE id = 42`).
		Scan(&newPath, &ownerType, &ownerID); err != nil {
		t.Fatal(err)
	}
	if ownerType != "org" {
		t.Errorf("owner_type = %q; want org", ownerType)
	}
	expectedPrefix := filepath.Join(tmp, "orgs")
	if !strings.HasPrefix(newPath, expectedPrefix) {
		t.Errorf("path %q should start with %q", newPath, expectedPrefix)
	}
	if got := readFile(t, filepath.Join(newPath, "README")); got != "legacy ws" {
		t.Errorf("content lost: %q", got)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Errorf("legacy path still present: %v", err)
	}
}

// TestMigrateOwnerModel_KeepsExternalRepoInPlace verifies the contract that
// repositories whose path is OUTSIDE dataDir (typical when the user attached
// an existing local repo via "Add Path") are not physically relocated by the
// owner-model migration. The user owns those directories; moving them would
// trigger BuildShadow + AtomicSwap + os.RemoveAll(oldPath), effectively
// deleting the source repo from its registered location.
//
// Spec basis (docs/superpowers/specs/2026-04-24-server-multi-tenant-design.md
// L361-363): "Path columns ... remain absolute in this release. This is
// consistent with the status quo." The repository.go finishCreate path also
// registers external paths without moving them.
//
// Required behavior: external repo's on-disk path stays untouched, DB path
// column unchanged, but owner_type / owner_id are stamped via
// stampOwnerColumns just like every other top-level row.
func TestMigrateOwnerModel_KeepsExternalRepoInPlace(t *testing.T) {
	tmp := t.TempDir()           // serves as dataDir
	externalRoot := t.TempDir()  // distinct tempdir simulating an external location
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('alice', 'x', 'admin')`); err != nil {
		t.Fatal(err)
	}

	externalPath := filepath.Join(externalRoot, "my-dev-repo")
	writeFile(t, filepath.Join(externalPath, "src", "main.go"), "package main")
	writeFile(t, filepath.Join(externalPath, ".git", "HEAD"), "ref: refs/heads/main")

	if _, err := db.Exec(`INSERT INTO repositories (id, name, path) VALUES (7, 'extern', ?)`, externalPath); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Auth: config.AuthConfig{Enabled: true}, DataDir: tmp}
	if err := MigrateOwnerModel(context.Background(), db, cfg, tmp); err != nil {
		t.Fatalf("MigrateOwnerModel: %v", err)
	}

	if got := readFile(t, filepath.Join(externalPath, "src", "main.go")); got != "package main" {
		t.Errorf("external repo source lost: %q", got)
	}
	if got := readFile(t, filepath.Join(externalPath, ".git", "HEAD")); !strings.Contains(got, "main") {
		t.Errorf("external repo .git lost: %q", got)
	}

	var dbPath, ownerType string
	var ownerID int64
	if err := db.QueryRow(`SELECT path, owner_type, owner_id FROM repositories WHERE id = 7`).
		Scan(&dbPath, &ownerType, &ownerID); err != nil {
		t.Fatal(err)
	}
	if dbPath != externalPath {
		t.Errorf("repositories.path = %q; want unchanged %q", dbPath, externalPath)
	}
	if ownerType != "org" {
		t.Errorf("owner_type = %q; want org", ownerType)
	}
	if ownerID <= 0 {
		t.Errorf("owner_id = %d; want > 0", ownerID)
	}
}

// TestMigrateOwnerModel_MovesManagedRepo guards the other half of the
// contract: niuniu-managed clones (paths under dataDir/repositories/) are
// still relocated to the per-owner layout, so we don't break the upgrade
// path for cloned repos.
func TestMigrateOwnerModel_MovesManagedRepo(t *testing.T) {
	tmp := t.TempDir()
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('alice', 'x', 'admin')`); err != nil {
		t.Fatal(err)
	}

	managedPath := filepath.Join(tmp, "repositories", "11")
	writeFile(t, filepath.Join(managedPath, "README"), "managed clone")
	if _, err := db.Exec(`INSERT INTO repositories (id, name, path) VALUES (11, 'managed', ?)`, managedPath); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Auth: config.AuthConfig{Enabled: true}, DataDir: tmp}
	if err := MigrateOwnerModel(context.Background(), db, cfg, tmp); err != nil {
		t.Fatalf("MigrateOwnerModel: %v", err)
	}

	var newPath string
	if err := db.QueryRow(`SELECT path FROM repositories WHERE id = 11`).Scan(&newPath); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(newPath, filepath.Join(tmp, "orgs")) {
		t.Errorf("managed repo path = %q; want under %s/orgs", newPath, tmp)
	}
	if got := readFile(t, filepath.Join(newPath, "README")); got != "managed clone" {
		t.Errorf("content lost after move: %q", got)
	}
	if _, err := os.Stat(managedPath); !os.IsNotExist(err) {
		t.Errorf("legacy managed path still present: %v", err)
	}
}

// openLegacyDB returns a DB whose projects table is built WITHOUT
// owner_type / owner_id columns — i.e. the pre-multi-tenant schema. Other
// tables come from current schema.sql; we just shadow `projects` with the
// legacy DDL to simulate an existing user DB created before 2026-04-25.
//
// Modelled on the real production failure: niuniu-personal-20260426 launched
// against ~/.niuniu/niuniu.db (created weeks before multi-tenant) and crashed
// with "stamp projects: no such column: owner_type" because main.go runs
// MigrateOwnerModel BEFORE server.New → store.Migrate → addOwnerModel.
func openLegacyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatal(err)
	}
	// Recreate projects without owner columns (legacy schema).
	if _, err := db.Exec(`DROP TABLE projects`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE projects (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		name        TEXT NOT NULL,
		description TEXT DEFAULT '',
		status      TEXT NOT NULL DEFAULT 'active',
		created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestMigrateOwnerModel_BootstrapsOnLegacySchema reproduces the production
// failure
//
//	owner-model migration failed: stamp projects: SQL logic error: no such column: owner_type (1)
//
// observed when niuniu-personal-20260426 launched against a populated DB.
// MigrateOwnerModel runs in main.go BEFORE server.New → store.Migrate, so
// owner columns added by addOwnerModel don't exist yet on legacy tables.
// The test suite's openDB helper masks this because it pre-calls store.Migrate.
//
// MigrateOwnerModel must guarantee its own precondition (owner columns exist)
// rather than depending on caller order.
func TestMigrateOwnerModel_BootstrapsOnLegacySchema(t *testing.T) {
	tmp := t.TempDir()
	db := openLegacyDB(t)
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('alice', 'x', 'admin')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects (id, name, status) VALUES (1, 'P1', 'active')`); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Auth: config.AuthConfig{Enabled: true}, DataDir: tmp}
	if err := MigrateOwnerModel(context.Background(), db, cfg, tmp); err != nil {
		t.Fatalf("MigrateOwnerModel must self-bootstrap missing owner columns on legacy schema; got: %v", err)
	}

	var ownerType string
	if err := db.QueryRow(`SELECT owner_type FROM projects WHERE id = 1`).Scan(&ownerType); err != nil {
		t.Fatalf("owner_type column should exist after migration: %v", err)
	}
	if ownerType != "org" {
		t.Errorf("owner_type = %q; want org", ownerType)
	}
}

// TestMigrateOwnerModel_RecoversFromStaleShadow reproduces the production
// failure
//
//	owner-model migration failed: shadow build for workspace 209:
//	shadow target C:\Users\...\.niuniu\.migrate-shadow\workspace\209 is not empty
//
// observed when an earlier migration attempt failed in Phase 2b
// (stampOwnerColumns or DB commit), triggering rollbackSwaps which renames
// newPath back into shadow. The .migrate-shadow tree is then non-empty on
// the next startup, and BuildShadow refuses to overwrite — wedging the
// upgrade indefinitely until the user manually rm -rf's the directory.
//
// CLAUDE.md "Upgrade path" promises the two-phase commit is "idempotent
// and resumable". The fix makes that promise true: MigrateOwnerModel cleans
// up stale shadow/quarantine state from any previous interrupted run before
// starting fresh.
func TestMigrateOwnerModel_RecoversFromStaleShadow(t *testing.T) {
	tmp := t.TempDir()
	db := openDB(t)
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('alice', 'x', 'admin')`); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(tmp, "workspaces", "42")
	writeFile(t, filepath.Join(legacyPath, "README"), "legacy ws")
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, path, status) VALUES (42, 'ws', ?, 'created')`, legacyPath); err != nil {
		t.Fatal(err)
	}

	// Simulate residue from a previous interrupted run that hit rollbackSwaps:
	// a non-empty .migrate-shadow directory for the same workspace ID.
	staleShadow := filepath.Join(tmp, ".migrate-shadow", "workspace", "42")
	writeFile(t, filepath.Join(staleShadow, "README"), "rolled-back content")

	cfg := &config.Config{Auth: config.AuthConfig{Enabled: true}, DataDir: tmp}
	if err := MigrateOwnerModel(context.Background(), db, cfg, tmp); err != nil {
		t.Fatalf("MigrateOwnerModel must auto-recover from stale shadow; got: %v", err)
	}

	var newPath string
	if err := db.QueryRow(`SELECT path FROM workspaces WHERE id = 42`).Scan(&newPath); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(newPath, "README")); got != "legacy ws" {
		t.Errorf("workspace content lost: %q", got)
	}
	if _, err := os.Stat(staleShadow); !os.IsNotExist(err) {
		t.Errorf("stale shadow should be cleaned up after successful migration: %v", err)
	}
}

func TestMigrateOwnerModelIsIdempotent(t *testing.T) {
	tmp := t.TempDir()
	db := openDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('alice', 'x', 'admin')`)

	cfg := &config.Config{Auth: config.AuthConfig{Enabled: true}, DataDir: tmp}
	if err := MigrateOwnerModel(context.Background(), db, cfg, tmp); err != nil {
		t.Fatal(err)
	}
	if err := MigrateOwnerModel(context.Background(), db, cfg, tmp); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
}
