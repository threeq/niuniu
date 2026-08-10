package migration

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateCleanupTeamCLAUDEMD_StripsMarkers(t *testing.T) {
	dataDir := t.TempDir()
	wsPath := filepath.Join(dataDir, "users", "1", "workspaces", "42")
	if err := os.MkdirAll(wsPath, 0755); err != nil {
		t.Fatal(err)
	}
	original := "# Workspace\n\nLead text.\n\n" +
		teamSectionStart + "\n## 团队成员\n- Alice\n" + teamSectionEnd + "\n" +
		"\nTrailing content.\n"
	claudemd := filepath.Join(wsPath, "CLAUDE.md")
	if err := os.WriteFile(claudemd, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	db := openMemDB(t)
	defer db.Close()

	if err := MigrateCleanupTeamCLAUDEMD(context.Background(), db, dataDir); err != nil {
		t.Fatalf("first run: %v", err)
	}

	got, err := os.ReadFile(claudemd)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), teamSectionStart) || strings.Contains(string(got), teamSectionEnd) {
		t.Errorf("markers not stripped: %s", got)
	}
	if !strings.Contains(string(got), "Lead text.") || !strings.Contains(string(got), "Trailing content.") {
		t.Errorf("non-team content lost: %s", got)
	}

	// Idempotent: second invocation must be a no-op (marker check) and not
	// re-touch a file that no longer has markers.
	if err := MigrateCleanupTeamCLAUDEMD(context.Background(), db, dataDir); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

func TestMigrateCleanupTeamCLAUDEMD_NoOpWhenNoMarkers(t *testing.T) {
	dataDir := t.TempDir()
	wsPath := filepath.Join(dataDir, "orgs", "5", "workspaces", "99")
	if err := os.MkdirAll(wsPath, 0755); err != nil {
		t.Fatal(err)
	}
	clean := "# Workspace\n\nNo team markers here.\n"
	claudemd := filepath.Join(wsPath, "CLAUDE.md")
	if err := os.WriteFile(claudemd, []byte(clean), 0644); err != nil {
		t.Fatal(err)
	}

	db := openMemDB(t)
	defer db.Close()

	if err := MigrateCleanupTeamCLAUDEMD(context.Background(), db, dataDir); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, err := os.ReadFile(claudemd)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != clean {
		t.Errorf("file should be untouched\nwant: %q\ngot:  %q", clean, got)
	}
}

func TestMigrateCleanupTeamCLAUDEMD_MissingDataDirIsOK(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "does-not-exist")

	db := openMemDB(t)
	defer db.Close()

	if err := MigrateCleanupTeamCLAUDEMD(context.Background(), db, dataDir); err != nil {
		t.Fatalf("missing dataDir should be tolerated: %v", err)
	}
}

func openMemDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	return db
}
