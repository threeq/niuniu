package service

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

func openWorkspaceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	store.Migrate(db)
	t.Cleanup(func() { db.Close() })
	return db
}

// TestWorkspaceCreatedUnderOwnerRoot verifies that a workspace created for an
// org ends up with a Path that starts with <dataDir>/orgs/<orgID>/workspaces/.
func TestWorkspaceCreatedUnderOwnerRoot(t *testing.T) {
	db := openWorkspaceTestDB(t)
	q := store.New(db)

	// Insert a user first (required by organizations.created_by FK).
	var adminUserID int64
	if err := db.QueryRowContext(context.Background(),
		`INSERT INTO users (username, password_hash, display_name, role) VALUES ('admin', 'x', 'Admin', 'admin') RETURNING id`,
	).Scan(&adminUserID); err != nil {
		if _, err2 := db.ExecContext(context.Background(),
			`INSERT INTO users (username, password_hash, display_name, role) VALUES ('admin', 'x', 'Admin', 'admin')`); err2 != nil {
			t.Fatalf("insert user: %v", err2)
		}
		if err2 := db.QueryRowContext(context.Background(),
			`SELECT id FROM users WHERE username = 'admin'`).Scan(&adminUserID); err2 != nil {
			t.Fatalf("select user: %v", err2)
		}
	}

	// Insert a minimal org row so FK constraints are satisfied.
	var orgID int64
	err := db.QueryRowContext(context.Background(),
		`INSERT INTO organizations (name, slug, created_by) VALUES ('TestOrg', 'testorg', ?) RETURNING id`, adminUserID).Scan(&orgID)
	if err != nil {
		// Fallback for DBs without RETURNING support.
		if _, err2 := db.ExecContext(context.Background(),
			`INSERT INTO organizations (name, slug, created_by) VALUES ('TestOrg', 'testorg', ?)`, adminUserID); err2 != nil {
			t.Fatalf("insert org: %v", err2)
		}
		if err2 := db.QueryRowContext(context.Background(),
			`SELECT id FROM organizations WHERE slug = 'testorg'`).Scan(&orgID); err2 != nil {
			t.Fatalf("select org: %v", err2)
		}
	}

	dataDir := t.TempDir()
	cfg := &config.WorkspaceConfig{
		BaseDir: filepath.Join(dataDir, "workspaces"), // legacy fallback base
	}
	if err := os.MkdirAll(cfg.BaseDir, 0755); err != nil {
		t.Fatalf("create base dir: %v", err)
	}

	svc := NewWorkspaceService(q, nil, cfg, dataDir, nil, nil)

	ws, err := svc.Create(context.Background(), CreateWorkspaceInput{
		Name:      "test-workspace",
		OwnerType: "org",
		OwnerID:   orgID,
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	wantPrefix := filepath.Join(dataDir, "orgs")
	if !strings.HasPrefix(ws.Workspace.Path, wantPrefix) {
		t.Errorf("workspace path %q does not start with %q", ws.Workspace.Path, wantPrefix)
	}

	// More specifically, it should be <dataDir>/orgs/<orgID>/workspaces/<wsID>
	wantDir := OwnerRef{Type: "org", ID: orgID}.WorkspacePath(dataDir, ws.Workspace.ID)
	if ws.Workspace.Path != wantDir {
		t.Errorf("workspace path = %q; want %q", ws.Workspace.Path, wantDir)
	}

	// Verify directory exists on disk.
	if info, err := os.Stat(ws.Workspace.Path); err != nil {
		t.Errorf("workspace directory does not exist on disk: %v", err)
	} else if !info.IsDir() {
		t.Errorf("workspace path is not a directory")
	}
}
