package testing

// Re-exports of the driver-agnostic PG harness. Implementations live in
// internal/pgtest so internal/service tests can import them without an
// import cycle through this package.

import (
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// DriverSQLite identifies the SQLite test driver.
const DriverSQLite = pgtest.DriverSQLite

// DriverPostgres identifies the PostgreSQL test driver.
const DriverPostgres = pgtest.DriverPostgres

// Drivers is the canonical list of drivers exercised by ForEachDriver.
var Drivers = pgtest.Drivers

// ForEachDriver fans a test out into one subtest per driver. See
// pgtest.ForEachDriver for full doc.
func ForEachDriver(t *testing.T, fn func(t *testing.T, drv string)) {
	t.Helper()
	pgtest.ForEachDriver(t, fn)
}

// SetupDBForDriver returns (raw *sql.DB, *store.Queries) for the requested
// driver. See pgtest.SetupDBForDriver.
func SetupDBForDriver(t *testing.T, drv string) (*sql.DB, *store.Queries) {
	t.Helper()
	return pgtest.SetupDBForDriver(t, drv)
}

// NewIsolationEnvForDriver returns an IsolationEnv on the requested driver.
// PG branch auto-skips when Docker is unavailable.
func NewIsolationEnvForDriver(t *testing.T, drv string) *IsolationEnv {
	t.Helper()
	switch drv {
	case DriverSQLite:
		store.Driver = DriverSQLite
		return NewIsolationEnv(t)
	case DriverPostgres:
		return NewPGIsolationEnv(t)
	default:
		t.Fatalf("NewIsolationEnvForDriver: unknown driver %q", drv)
		return nil
	}
}

// PGAvailable reports whether the postgres branch would run via external
// DSN. See pgtest.PGAvailable.
func PGAvailable() bool { return pgtest.PGAvailable() }
