package migration

import (
	"context"
	cryptoRand "crypto/rand"
	"crypto/sha1"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// queryRunner is the subset of methods that *store.DB and *store.Tx both
// implement. execAndGetID uses it to support both the top-level DB and the
// in-tx case (ensureDefaultOrg) without code duplication.
type queryRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// execAndGetID runs an INSERT and returns the new row's primary key. SQLite
// uses Result.LastInsertId; Postgres has no LastInsertId support in pgx v5,
// so we append " RETURNING id" and Scan it back. The query string passes
// through *store.DB / *store.Tx, which apply ConvertPlaceholders + the
// INSERT-OR-IGNORE → ON CONFLICT rewrite for Postgres.
func execAndGetID(ctx context.Context, q queryRunner, query string, args ...any) (int64, error) {
	if store.Driver == "postgres" {
		var id int64
		err := q.QueryRowContext(ctx, query+" RETURNING id", args...).Scan(&id)
		return id, err
	}
	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ResolveTargetOwner determines the (type, id) that owns all pre-existing
// data after the upgrade. See spec §7.3.
//
// Side effects: ensures the target user or org exists (creating it if needed)
// and, for org targets, enrolls every existing user with the first admin
// promoted to role='owner'. Idempotent.
func ResolveTargetOwner(ctx context.Context, db *store.DB, cfg *config.Config) (service.OwnerRef, error) {
	kind, value := cfg.ResolvedUpgradeTarget()
	switch kind {
	case "user":
		return ensureSeedUser(ctx, db, value)
	case "org":
		return ensureDefaultOrg(ctx, db, value)
	default:
		return service.OwnerRef{}, fmt.Errorf("invalid upgrade target %q:%q", kind, value)
	}
}

func ensureSeedUser(ctx context.Context, db *store.DB, username string) (service.OwnerRef, error) {
	if strings.TrimSpace(username) == "" {
		return service.OwnerRef{}, fmt.Errorf("seed user username empty")
	}
	var id int64
	err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&id)
	if err == sql.ErrNoRows {
		hashed, hashErr := bcrypt.GenerateFromPassword([]byte(randomString(32)), bcrypt.MinCost)
		if hashErr != nil {
			return service.OwnerRef{}, hashErr
		}
		id, err = execAndGetID(ctx, db,
			`INSERT INTO users (username, password_hash, role, display_name) VALUES (?, ?, 'admin', ?)`,
			username, string(hashed), username)
		if err != nil {
			return service.OwnerRef{}, fmt.Errorf("seed user: %w", err)
		}
	} else if err != nil {
		return service.OwnerRef{}, err
	}
	return service.OwnerRef{Type: "user", ID: id}, nil
}

func ensureDefaultOrg(ctx context.Context, db *store.DB, name string) (service.OwnerRef, error) {
	slug := slugify(name)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return service.OwnerRef{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	var firstAdminID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE role = 'admin' ORDER BY id LIMIT 1`).Scan(&firstAdminID)
	if err == sql.ErrNoRows {
		err = tx.QueryRowContext(ctx, `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&firstAdminID)
		if err == sql.ErrNoRows {
			return service.OwnerRef{}, fmt.Errorf("cannot create default org: no users in DB")
		}
	}
	if err != nil {
		return service.OwnerRef{}, err
	}

	var orgID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM organizations WHERE slug = ?`, slug).Scan(&orgID)
	if err == sql.ErrNoRows {
		orgID, err = execAndGetID(ctx, tx,
			`INSERT INTO organizations (slug, name, description, created_by) VALUES (?, ?, ?, ?)`,
			slug, name, "Upgraded default organization", firstAdminID)
		if err != nil {
			return service.OwnerRef{}, fmt.Errorf("create default org: %w", err)
		}
	} else if err != nil {
		return service.OwnerRef{}, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT id FROM users`)
	if err != nil {
		return service.OwnerRef{}, err
	}
	var userIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return service.OwnerRef{}, err
		}
		userIDs = append(userIDs, id)
	}
	rows.Close()
	for _, uid := range userIDs {
		role := "member"
		if uid == firstAdminID {
			role = "owner"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO org_members (org_id, user_id, role) VALUES (?, ?, ?)`,
			orgID, uid, role); err != nil {
			return service.OwnerRef{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return service.OwnerRef{}, err
	}
	return service.OwnerRef{Type: "org", ID: orgID}, nil
}

// slugify converts name to a URL-safe ASCII slug. For names that consist
// entirely of non-ASCII characters (e.g. CJK), the ASCII portion would be
// empty; in that case a stable 8-char hex hash of the original name is used
// to produce a unique slug instead of always falling back to "default".
func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b = append(b, c)
		}
	}
	if len(b) == 0 {
		sum := sha1.Sum([]byte(name))
		return fmt.Sprintf("org-%x", sum[:4])
	}
	return string(b)
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, n)
	_, _ = cryptoRand.Read(buf)
	for i := range buf {
		buf[i] = letters[int(buf[i])%len(letters)]
	}
	return string(buf)
}

// moveEntry describes one workspace or repository directory that must be
// relocated as part of the owner-model migration.
type moveEntry struct {
	kind    string
	id      int64
	oldPath string
	newPath string
	shadow  string
	quarant string
}

// MigrateOwnerModel is the top-level entry. Idempotent, safe to rerun.
//
// Self-bootstraps owner_type / owner_id columns on legacy tables by calling
// store.Migrate up front. Without this, callers must remember to invoke
// store.Migrate first — and cmd/niuniu/main.go historically did not, so
// upgrading a pre-multi-tenant DB crashed with "stamp projects: no such
// column: owner_type" before stampOwnerColumns could run. store.Migrate is
// idempotent (CREATE INDEX IF NOT EXISTS, addColumnIfNotExists, etc.) so
// re-invoking it from server.New a moment later is harmless.
func MigrateOwnerModel(ctx context.Context, raw *sql.DB, cfg *config.Config, dataDir string) error {
	// Wrap once at the entry. Every method on *store.DB / *store.Tx applies
	// driver-aware placeholder + INSERT-OR-IGNORE rewriting, so the rest of
	// this function reads as plain SQLite-flavored SQL even when the active
	// driver is Postgres.
	db := store.Wrap(raw)
	if done, err := alreadyMigrated(ctx, db); err != nil {
		return err
	} else if done {
		return nil
	}

	// Ensure owner columns / per-owner indexes exist before we try to UPDATE
	// owner_type / owner_id. store.Migrate predates the wrapper and takes a
	// raw *sql.DB; that's fine since its DDL has no "?" placeholders.
	store.Migrate(raw)

	// Wipe stale shadow / quarantine residue from a previous interrupted run.
	// CLAUDE.md "Upgrade path" promises this migration is "idempotent and
	// resumable". Without this cleanup, BuildShadow would fail with "shadow
	// target … is not empty" whenever a prior attempt got past Phase 1
	// (which builds shadows) and was rolled back from Phase 2b — rollbackSwaps
	// renames newPath back into the shadow directory, and the next run would
	// see leftover content. oldPaths are unchanged across these failures
	// (Phase 3 only deletes them on full success), so rebuilding shadow from
	// oldPath produces identical content. Quarantine is similarly safe:
	// rollback drains it back to newPath, and a fresh Phase 2a re-creates per-id
	// subdirs as needed.
	if err := os.RemoveAll(filepath.Join(dataDir, ".migrate-shadow")); err != nil {
		return fmt.Errorf("clean stale shadow dir: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(dataDir, ".migrate-quarantine")); err != nil {
		return fmt.Errorf("clean stale quarantine dir: %w", err)
	}

	target, err := ResolveTargetOwner(ctx, db, cfg)
	if err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}

	var moves []moveEntry

	// managedRepoPrefix bounds which repository.path values niuniu owns.
	// Niuniu-managed clones live under dataDir/repositories/ (from
	// repository.go:cloneRepository → finishCreate → relocate). Repositories
	// the user attached via "Add Path" point at directories OUTSIDE this
	// prefix (e.g. E:\projects\foo) and must NOT be physically relocated:
	// AtomicSwap + os.RemoveAll(oldPath) would delete the user's source repo.
	// External rows still get owner_type/owner_id stamped via stampOwnerColumns
	// — only the on-disk move and the path column update are skipped.
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolve dataDir: %w", err)
	}
	managedRepoPrefix := filepath.Join(absDataDir, "repositories") + string(filepath.Separator)

	appendMoves := func(q, kind string) error {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var path string
			if err := rows.Scan(&id, &path); err != nil {
				return err
			}
			var newPath string
			switch kind {
			case "workspace":
				newPath = target.WorkspacePath(dataDir, id)
			case "repository":
				absPath, aerr := filepath.Abs(path)
				if aerr != nil {
					slog.Warn("MigrateOwnerModel: cannot resolve repository path, leaving in place", "id", id, "path", path, "error", aerr)
					continue
				}
				if !strings.HasPrefix(absPath+string(filepath.Separator), managedRepoPrefix) {
					slog.Info("MigrateOwnerModel: repository registered with external path, leaving in place", "id", id, "path", path)
					continue
				}
				newPath = target.RepositoryPath(dataDir, id)
			default:
				return fmt.Errorf("unknown kind %q", kind)
			}
			moves = append(moves, moveEntry{
				kind:    kind,
				id:      id,
				oldPath: path,
				newPath: newPath,
				shadow:  filepath.Join(dataDir, ".migrate-shadow", kind, fmt.Sprintf("%d", id)),
				quarant: filepath.Join(dataDir, ".migrate-quarantine", kind, fmt.Sprintf("%d", id)),
			})
		}
		return rows.Err()
	}

	if err := appendMoves(`SELECT id, path FROM workspaces`, "workspace"); err != nil {
		return err
	}
	if err := appendMoves(`SELECT id, path FROM repositories WHERE path != ''`, "repository"); err != nil {
		return err
	}

	// Phase 1: build shadow copies (originals untouched).
	for _, m := range moves {
		if _, err := os.Stat(m.oldPath); os.IsNotExist(err) {
			continue
		}
		if err := BuildShadow(m.oldPath, m.shadow); err != nil {
			_ = os.RemoveAll(filepath.Join(dataDir, ".migrate-shadow"))
			return fmt.Errorf("shadow build for %s %d: %w", m.kind, m.id, err)
		}
	}

	// Phase 2a: filesystem AtomicSwaps FIRST (spec §7.4).
	// Record which swaps succeeded so we can roll back on partial failure or DB
	// commit failure.
	var swapped []swapRecord

	for _, m := range moves {
		if _, err := os.Stat(m.shadow); os.IsNotExist(err) {
			swapped = append(swapped, swapRecord{m: m, didSwap: false})
			continue
		}
		if err := AtomicSwap(m.newPath, m.shadow, m.quarant); err != nil {
			// Roll back every swap that succeeded before this one.
			rollbackSwaps(swapped)
			_ = os.RemoveAll(filepath.Join(dataDir, ".migrate-shadow"))
			return fmt.Errorf("atomic swap for %s %d: %w", m.kind, m.id, err)
		}
		swapped = append(swapped, swapRecord{m: m, didSwap: true})
	}

	// Phase 2b: DB update inside a transaction.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		rollbackSwaps(swapped)
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if err := stampOwnerColumns(ctx, tx, target); err != nil {
		rollbackSwaps(swapped)
		return err
	}

	for _, m := range moves {
		tbl := m.kind + "s" // workspaces → workspaces
		if m.kind == "repository" {
			tbl = "repositories"
		}
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET path = ? WHERE id = ?`, tbl),
			m.newPath, m.id); err != nil {
			rollbackSwaps(swapped)
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		// Best-effort rollback: rename the already-swapped directories back so
		// the state is "as-if-swap-never-happened". This leaves quarantine dirs
		// as the original data, and removes the new-path dirs — callers should
		// inspect the quarantine if something goes wrong here.
		rollbackSwaps(swapped)
		return fmt.Errorf("DB commit after filesystem swap: %w", err)
	}

	// Phase 3: clean up legacy paths and quarantine dirs.
	for _, r := range swapped {
		if !r.didSwap {
			continue
		}
		m := r.m
		if m.oldPath != m.newPath {
			_ = os.RemoveAll(m.oldPath)
		}
	}

	if err := markMigrated(ctx, db); err != nil {
		return err
	}

	_ = os.RemoveAll(filepath.Join(dataDir, ".migrate-quarantine"))
	return nil
}

// swapRecord tracks the outcome of a single AtomicSwap call so that
// rollbackSwaps can undo completed swaps on error.
type swapRecord struct {
	m       moveEntry
	didSwap bool // false when shadow was absent (no disk data to move)
}

// rollbackSwaps undoes a list of AtomicSwap operations in reverse order.
// For each swap that succeeded, it renames new path back to quarantine path
// (restoring the original). Uses crossDeviceRename for EXDEV safety.
// Best-effort: errors are not returned.
func rollbackSwaps(swapped []swapRecord) {
	for i := len(swapped) - 1; i >= 0; i-- {
		rec := swapped[i]
		if !rec.didSwap {
			continue
		}
		m := rec.m
		// Attempt to undo: move orig (newPath) → shadow, move quarantine → orig.
		// This mirrors what AtomicSwap did in reverse.
		if _, err := os.Stat(m.newPath); err == nil {
			if err2 := crossDeviceRename(m.newPath, m.shadow); err2 != nil {
				// Can't restore shadow; try putting quarantine back at least.
				_ = crossDeviceRename(m.quarant, m.newPath)
				continue
			}
		}
		if _, err := os.Stat(m.quarant); err == nil {
			_ = crossDeviceRename(m.quarant, m.newPath)
		}
	}
}

// stampOwnerColumns sets owner columns on every top-level table to target
// for rows where owner_id is still the migration default (0).
func stampOwnerColumns(ctx context.Context, tx *store.Tx, target service.OwnerRef) error {
	// Note: 'harnesses' and 'teams' were removed in Phase 7 (drop_legacy_phase7_v1).
	// They are no longer top-level owned tables; MigrateOwnerModel runs before
	// MigrateDropLegacyPhase7, so we must guard against their absence on
	// fresh installs that never had these tables.
	//
	// IMPORTANT: env_presets / quick_actions / harness_specs are intentionally
	// EXCLUDED. Those tables use owner_id=0 as a GLOBAL sentinel (seeded by
	// *Service.SeedDefaults and surfaced to every caller via their
	// *_owner_filter.sql). Stamping their owner_id=0 rows onto the Default org
	// both hid the global defaults from everyone AND left the org permanently
	// undeletable ("org still owns resources in harness_specs"). Leaving them at
	// (user,0) keeps them global, which is the correct post-upgrade state.
	tables := []string{
		"projects", "repositories", "workspaces",
		"agents",
	}
	// Stamp harnesses / teams only if they still exist (pre-Phase-7 databases).
	for _, tbl := range []string{"harnesses", "teams"} {
		if legacyTableExists(ctx, tx, tbl) {
			tables = append(tables, tbl)
		}
	}
	for _, tbl := range tables {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET owner_type = ?, owner_id = ? WHERE owner_id = 0`, tbl),
			target.Type, target.ID); err != nil {
			return fmt.Errorf("stamp %s: %w", tbl, err)
		}
	}
	return nil
}

func alreadyMigrated(ctx context.Context, db *store.DB) (bool, error) {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		key TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return false, err
	}
	var dummy string
	err := db.QueryRowContext(ctx, `SELECT key FROM schema_migrations WHERE key = 'owner_model_v1'`).Scan(&dummy)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func markMigrated(ctx context.Context, db *store.DB) error {
	_, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO schema_migrations (key) VALUES ('owner_model_v1')`)
	return err
}

// legacyTableExists returns true if the named table exists in the current
// database. Driver-aware: uses sqlite_master on SQLite, information_schema on
// PostgreSQL. Returns false on any error (conservative — avoids cascading failures).
func legacyTableExists(ctx context.Context, q queryRunner, table string) bool {
	var n int
	var err error
	if store.Driver == "postgres" {
		err = q.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM information_schema.tables
			 WHERE table_schema = 'public' AND table_name = ?`, table).Scan(&n)
	} else {
		err = q.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n)
	}
	return err == nil && n > 0
}
