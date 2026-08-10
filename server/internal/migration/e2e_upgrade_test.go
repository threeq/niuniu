package migration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
)

// Simulate a pre-multi-tenant database with a few projects / workspaces /
// repositories, run the upgrade end-to-end, assert the post-state.
func TestUpgradeFromLegacy_AuthEnabled(t *testing.T) {
	tmp := t.TempDir()
	db := openDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('alice', 'x', 'admin')`)
	_, _ = db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('bob',   'x', 'member')`)
	_, _ = db.Exec(`INSERT INTO projects (id, name, status) VALUES (1, 'P1', 'active')`)
	_, _ = db.Exec(`INSERT INTO repositories (id, name, path, git_remote, default_branch) VALUES (1, 'R', ?, 'git@x:r', 'main')`,
		filepath.Join(tmp, "repositories", "1"))
	_, _ = db.Exec(`INSERT INTO workspaces (id, name, path, status) VALUES (1, 'WS', ?, 'created')`,
		filepath.Join(tmp, "workspaces", "1"))

	writeFile(t, filepath.Join(tmp, "workspaces", "1", "keep"), "ws")
	writeFile(t, filepath.Join(tmp, "repositories", "1", "keep"), "repo")

	cfg := &config.Config{Auth: config.AuthConfig{Enabled: true}, DataDir: tmp}
	if err := MigrateOwnerModel(context.Background(), db, cfg, tmp); err != nil {
		t.Fatal(err)
	}

	var ownerCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM org_members WHERE role='owner'`).Scan(&ownerCount)
	if ownerCount != 1 {
		t.Errorf("owner count = %d; want 1", ownerCount)
	}
	var bobRole string
	_ = db.QueryRow(`SELECT m.role FROM org_members m JOIN users u ON u.id = m.user_id WHERE u.username = 'bob'`).Scan(&bobRole)
	if bobRole != "member" {
		t.Errorf("bob role = %q; want member", bobRole)
	}
	var projOwnerType string
	_ = db.QueryRow(`SELECT owner_type FROM projects WHERE id = 1`).Scan(&projOwnerType)
	if projOwnerType != "org" {
		t.Errorf("project owner_type = %q; want org", projOwnerType)
	}
	var newPath string
	_ = db.QueryRow(`SELECT path FROM workspaces WHERE id = 1`).Scan(&newPath)
	if _, err := os.Stat(filepath.Join(newPath, "keep")); err != nil {
		t.Errorf("workspace content lost: %v", err)
	}
}

func TestUpgradeFromLegacy_AuthDisabled_PersonalEdition(t *testing.T) {
	tmp := t.TempDir()
	db := openDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO projects (id, name, status) VALUES (1, 'Personal', 'active')`)

	cfg := &config.Config{
		Auth:    config.AuthConfig{Enabled: false, SingleUser: config.UserConfig{Username: "local"}},
		DataDir: tmp,
	}
	if err := MigrateOwnerModel(context.Background(), db, cfg, tmp); err != nil {
		t.Fatal(err)
	}

	var orgCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM organizations`).Scan(&orgCount)
	if orgCount != 0 {
		t.Errorf("orgCount = %d; personal-edition upgrade must not create an org", orgCount)
	}
	var ownerType string
	var ownerID int64
	_ = db.QueryRow(`SELECT owner_type, owner_id FROM projects WHERE id = 1`).Scan(&ownerType, &ownerID)
	if ownerType != "user" {
		t.Errorf("owner_type = %q; want user", ownerType)
	}
	if ownerID == 0 {
		t.Error("owner_id should point to the seeded local user")
	}
}
