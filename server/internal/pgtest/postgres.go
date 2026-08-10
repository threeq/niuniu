// Package pgtest provides a PostgreSQL integration test harness for the
// niuniu server. Tests use SetupPGDB or ForEachDriver to exercise sqlc
// code paths against a real Postgres backend, catching PG-only SQL errors
// (SQLSTATE 42P18 untyped parameter, 42601 syntax error, ON CONFLICT
// rewrite edge cases) that SQLite's loose typing hides.
//
// Container management: uses testcontainers-go to start a shared
// postgres:16-alpine instance on first call, reused across tests. Each
// test gets an isolated schema via CREATE SCHEMA and search_path. The
// shared container is dropped at process exit (handled by
// testcontainers-go's Ryuk reaper).
//
// Local dev: when Docker is unreachable, SetupPGDB calls t.Skipf to skip
// the test cleanly. Setting NIUNIU_TEST_PG_DSN bypasses testcontainers
// and connects to an externally-managed Postgres (CI services pattern).
//
// This package deliberately has no dependency on internal/service or
// internal/testing so that internal/service tests can import it without
// creating an import cycle.
package pgtest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// EnvDSN is the env var that, when set, bypasses testcontainers and points
// SetupPGDB at an externally-managed Postgres. CI typically injects this
// via a services.postgres block.
const EnvDSN = "NIUNIU_TEST_PG_DSN"

// EnvSQLiteOnly is the env var that, when set to "1", forces PostgresDSN
// to call t.Skipf immediately without consulting Docker. Use on the
// SQLite-only CI job so that the testcontainers branch doesn't spawn a
// Postgres container behind Docker's back (GitHub-hosted runners ship
// with Docker, so the regular "skip when Docker is unavailable" logic
// does NOT trigger there).
const EnvSQLiteOnly = "NIUNIU_TEST_SQLITE_ONLY"

var (
	sharedPG     *postgres.PostgresContainer
	sharedPGOnce sync.Once
	sharedPGDSN  string
	sharedPGErr  error
)

// PostgresDSN returns a base DSN for the shared Postgres instance. Starts a
// testcontainers postgres:16-alpine on first call when NIUNIU_TEST_PG_DSN is
// unset. Auto-skips via t.Skipf when Docker is unavailable (rootless / WSL
// quirks, daemon down, etc.). Tests should normally call SetupPGDB, which
// adds per-test schema isolation on top of this DSN.
func PostgresDSN(t *testing.T) string {
	t.Helper()
	if strings.TrimSpace(os.Getenv(EnvSQLiteOnly)) == "1" {
		t.Skipf("PG path disabled (%s=1)", EnvSQLiteOnly)
	}
	if dsn := strings.TrimSpace(os.Getenv(EnvDSN)); dsn != "" {
		return dsn
	}
	sharedPGOnce.Do(func() {
		ctx := context.Background()
		sharedPG, sharedPGErr = postgres.Run(ctx,
			"postgres:16-alpine",
			postgres.WithDatabase("niuniu_test"),
			postgres.WithUsername("test"),
			postgres.WithPassword("test"),
			// 90s outer deadline for the whole wait strategy; inner
			// occurrence=2 catches the "ready ... ready" double-log that
			// postgres:16-alpine prints (first ready is during init, second
			// is when the server is actually accepting connections).
			testcontainers.WithWaitStrategyAndDeadline(
				90*time.Second,
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2),
			),
		)
		if sharedPGErr != nil {
			return
		}
		sharedPGDSN, sharedPGErr = sharedPG.ConnectionString(ctx, "sslmode=disable")
	})
	if sharedPGErr != nil {
		t.Skipf("testcontainers postgres unavailable (Docker required, or set %s): %v", EnvDSN, sharedPGErr)
	}
	return sharedPGDSN
}

var nonAlphanumRE = regexp.MustCompile(`[^a-zA-Z0-9]`)

// SanitizeName converts a test name into a valid Postgres identifier
// fragment (lowercase alphanumeric, max 40 chars).
func SanitizeName(name string) string {
	s := nonAlphanumRE.ReplaceAllString(name, "_")
	s = strings.ToLower(s)
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// NewSchemaDSN creates an isolated Postgres schema for a single test and
// returns a DSN pointing at it (via search_path). The schema is dropped on
// test cleanup. Reuses the shared container so each test pays ~10ms for
// CREATE SCHEMA rather than ~1s+ for a fresh database.
func NewSchemaDSN(t *testing.T) string {
	t.Helper()
	base := PostgresDSN(t)
	schemaName := fmt.Sprintf("t_%s_%d", SanitizeName(t.Name()), time.Now().UnixNano())

	db, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("pgtest.NewSchemaDSN: open base: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(fmt.Sprintf("CREATE SCHEMA %q", schemaName)); err != nil {
		t.Fatalf("pgtest.NewSchemaDSN: CREATE SCHEMA %s: %v", schemaName, err)
	}

	t.Cleanup(func() {
		cleanDB, err := sql.Open("pgx", base)
		if err != nil {
			t.Logf("pgtest.NewSchemaDSN cleanup: open: %v", err)
			return
		}
		defer cleanDB.Close()
		if _, err := cleanDB.Exec(fmt.Sprintf("DROP SCHEMA %q CASCADE", schemaName)); err != nil {
			t.Logf("pgtest.NewSchemaDSN cleanup: DROP SCHEMA %s: %v", schemaName, err)
		}
	})

	sep := "&"
	if !strings.Contains(base, "?") {
		sep = "?"
	}
	return base + sep + "search_path=" + schemaName
}

// SetupPGDB opens a connection to an isolated Postgres schema, applies the
// full PG schema and all open-time + Migrate() migrations, and returns the
// raw *sql.DB plus a sqlc *Queries already wrapped for PG placeholder
// rewriting.
//
// Side-effect: sets store.Driver = "postgres" for the duration of the test, and
// restores it on cleanup so the package-global doesn't leak "postgres" into a later
// SQLite test in the same suite (which would send store.Migrate down the PG
// information_schema branch and panic on an in-memory SQLite DB). When running
// dual-driver tests via ForEachDriver, the harness runs SQLite first then Postgres
// serially, so the global isn't raced.
//
// Auto-skips via t.Skipf when Docker is unavailable.
func SetupPGDB(t *testing.T) (*sql.DB, *store.Queries) {
	t.Helper()
	dsn := NewSchemaDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("pgtest.SetupPGDB: open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	prevDriver := store.Driver
	t.Cleanup(func() { store.Driver = prevDriver })
	store.Driver = "postgres"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("pgtest.SetupPGDB: ApplySchema: %v", err)
	}
	store.Migrate(db)

	return db, store.NewQueries(db)
}
