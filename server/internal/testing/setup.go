package testing

import (
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

// SetupDB opens an in-memory SQLite DB, applies the full schema and all
// open-time + Migrate() migrations, and returns a *store.Queries ready for
// service/api unit tests.
//
// The DB is closed automatically via t.Cleanup.
func SetupDB(t *testing.T) *store.Queries {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("failed to open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	store.Migrate(db)

	return store.New(db)
}

// SetupDBRaw is like SetupDB but also returns the underlying *sql.DB for
// callers that need it to build *store.DB wrappers (services that hold
// db: *store.DB per CLAUDE.md "Driver-aware DB access").
func SetupDBRaw(t *testing.T) (*sql.DB, *store.Queries) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("failed to open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	store.Migrate(db)

	return db, store.New(db)
}
