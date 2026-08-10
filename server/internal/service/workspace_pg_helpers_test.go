package service

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// openWorkspaceTestDBForDriver returns a *sql.DB seeded with schema +
// migrations for the requested driver. On postgres, auto-skips when Docker
// is unavailable. Mirrors openWorkspaceTestDB (SQLite-only) but parameterized.
func openWorkspaceTestDBForDriver(t *testing.T, drv string) *sql.DB {
	t.Helper()
	switch drv {
	case pgtest.DriverSQLite:
		return openWorkspaceTestDB(t)
	case pgtest.DriverPostgres:
		raw, _ := pgtest.SetupPGDB(t)
		return raw
	default:
		t.Fatalf("openWorkspaceTestDBForDriver: unknown driver %q", drv)
		return nil
	}
}

// newWorkspaceServiceForTestDriver mirrors newWorkspaceServiceForTest but
// returns a service backed by the requested driver. PG branch auto-skips
// when Docker is unavailable.
//
// Uses store.NewQueries(db) (driver-aware) rather than store.New(db) so
// generated sqlc queries route through the placeholder rewriter on PG.
func newWorkspaceServiceForTestDriver(t *testing.T, drv string) (*WorkspaceService, *sql.DB) {
	t.Helper()
	db := openWorkspaceTestDBForDriver(t, drv)
	q := store.NewQueries(db)
	dataDir := t.TempDir()
	cfg := &config.WorkspaceConfig{
		BaseDir: filepath.Join(dataDir, "workspaces"),
	}
	if err := os.MkdirAll(cfg.BaseDir, 0o755); err != nil {
		t.Fatalf("create base dir: %v", err)
	}
	svc := NewWorkspaceService(q, db, cfg, dataDir, nil, nil)
	return svc, db
}
