package testing

import (
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/server"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// SetupTestDB creates an in-memory SQLite database for testing
func SetupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	// SQLite :memory: is per-connection — multiple connections see different DBs.
	// Pin to a single connection so background goroutines spawned by server.New
	// (e.g. subagent reconcile/GC) don't race against test setup with empty DBs.
	db.SetMaxOpenConns(1)

	// Apply schema
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("failed to run schema: %v", err)
	}

	// Apply column migrations that are handled in store/open.go at runtime
	migrations := []string{
		"ALTER TABLE workspaces ADD COLUMN is_temporary INTEGER NOT NULL DEFAULT 0",
	}
	for _, m := range migrations {
		db.Exec(m) // Ignore errors (column may already exist in schema)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("failed to close test database: %v", err)
		}
	})

	return db
}

// SetupTestServer creates a test server with an in-memory database
func SetupTestServer(t *testing.T) (*server.Server, *sql.DB) {
	t.Helper()

	db := SetupTestDB(t)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Port: 0, // Use random port for tests
			Host: "127.0.0.1",
		},
		Storage: config.StorageConfig{
			Driver: "sqlite",
		},
		Workspace: config.WorkspaceConfig{
			BaseDir: t.TempDir(), // Use temp directory for tests
		},
		Agent: config.AgentConfig{
			ClaudeCode: config.ClaudeCodeConfig{
				Command: "echo",
				Args:    []string{"test"},
			},
		},
		// server.New writes integration_secret under DataDir; pin to a per-test
		// temp dir so it doesn't leak into the package CWD (e.g.
		// server/internal/api/integration_secret showing up as untracked).
		DataDir: t.TempDir(),
	}

	srv := server.New(cfg, db, nil)

	return srv, db
}

// CleanupTestDB is a no-op helper for compatibility
// (cleanup is handled by t.Cleanup in SetupTestDB)
func CleanupTestDB(t *testing.T, db *sql.DB) {
	t.Helper()
	// Cleanup is handled by t.Cleanup in SetupTestDB
}

// SetupTestQueries creates a new Queries instance for testing
func SetupTestQueries(t *testing.T) *store.Queries {
	t.Helper()

	db := SetupTestDB(t)
	return store.New(db)
}
