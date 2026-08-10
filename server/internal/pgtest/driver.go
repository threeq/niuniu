package pgtest

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// DriverSQLite identifies the SQLite test driver.
const DriverSQLite = "sqlite"

// DriverPostgres identifies the PostgreSQL test driver.
const DriverPostgres = "postgres"

// Drivers is the canonical list of drivers exercised by ForEachDriver.
// SQLite always runs; postgres runs if Docker is available or
// NIUNIU_TEST_PG_DSN is set (auto-skips otherwise inside the PG subtest).
var Drivers = []string{DriverSQLite, DriverPostgres}

// ForEachDriver fans a test out into one subtest per driver. Use when a
// test can run identically on SQLite and PostgreSQL and you want the
// harness to catch PG-only SQL errors (SQLSTATE 42P18, 42601) that
// SQLite's loose typing hides.
//
//	func TestFoo(t *testing.T) {
//	    pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
//	        raw, q := pgtest.SetupDBForDriver(t, drv)
//	        // ... assertions against q ...
//	    })
//	}
//
// The postgres subtest skips when Docker is unavailable; the sqlite
// subtest always runs.
func ForEachDriver(t *testing.T, fn func(t *testing.T, drv string)) {
	t.Helper()
	for _, drv := range Drivers {
		drv := drv
		t.Run(drv, func(t *testing.T) {
			fn(t, drv)
		})
	}
}

// SetupSQLiteDB opens an in-memory SQLite DB, applies the full schema and
// migrations, and returns the raw *sql.DB plus a *store.Queries. Mirrors
// internal/testing.SetupDBRaw but lives here so internal/service tests
// can build dual-driver harnesses without an import cycle through
// internal/testing.
func SetupSQLiteDB(t *testing.T) (*sql.DB, *store.Queries) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("pgtest.SetupSQLiteDB: open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("pgtest.SetupSQLiteDB: ApplySchema: %v", err)
	}
	store.Migrate(db)
	return db, store.New(db)
}

// SetupDBForDriver returns (raw *sql.DB, *store.Queries) for the requested
// driver. Sets store.Driver as a side effect — see SetupPGDB doc for the
// rationale.
func SetupDBForDriver(t *testing.T, drv string) (*sql.DB, *store.Queries) {
	t.Helper()
	switch drv {
	case DriverSQLite:
		return SetupSQLiteDB(t)
	case DriverPostgres:
		return SetupPGDB(t)
	default:
		t.Fatalf("pgtest.SetupDBForDriver: unknown driver %q", drv)
		return nil, nil
	}
}

// PGAvailable reports whether the postgres branch would run via the
// external-DSN path. Does NOT probe Docker — that would spawn the shared
// container as a side effect. Callers that need full fidelity should call
// SetupPGDB and let it t.Skipf.
func PGAvailable() bool {
	return strings.TrimSpace(os.Getenv(EnvDSN)) != ""
}
