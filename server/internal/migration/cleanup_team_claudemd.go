package migration

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// teamSectionStart / teamSectionEnd are the markers the deleted
// harness.InjectTeamIntoCLAUDEMD used to wrap its rendered "团队成员" block.
// Old workspace CLAUDE.md files written before the AI Teams feature was
// removed still carry orphan sections; this one-shot sweep strips them.
const (
	teamSectionStart = "<!-- TEAM:START -->"
	teamSectionEnd   = "<!-- TEAM:END -->"
)

// MigrateCleanupTeamCLAUDEMD scans every workspace CLAUDE.md under dataDir and
// removes any TEAM:START/END section left behind by the now-removed AI Teams
// feature. Idempotent via schema_migrations key 'cleanup_team_claudemd_v1'.
//
// Failures on individual files are logged but do not abort the migration —
// CLAUDE.md is a user-editable file and a single unreadable one shouldn't
// block server startup. The migration marker is only written if the directory
// scan completes without I/O error on the root.
func MigrateCleanupTeamCLAUDEMD(ctx context.Context, raw *sql.DB, dataDir string) error {
	db := store.Wrap(raw)

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		key TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	var dummy string
	err := db.QueryRowContext(ctx, `SELECT key FROM schema_migrations WHERE key = ?`,
		"cleanup_team_claudemd_v1").Scan(&dummy)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("check migration marker: %w", err)
	}

	swept := 0
	for _, ownerKind := range []string{"users", "orgs"} {
		root := filepath.Join(dataDir, ownerKind)
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read %s: %w", root, err)
		}
		for _, owner := range entries {
			if !owner.IsDir() {
				continue
			}
			wsRoot := filepath.Join(root, owner.Name(), "workspaces")
			wsEntries, err := os.ReadDir(wsRoot)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				slog.Warn("cleanup_team_claudemd: read workspaces dir failed", "path", wsRoot, "err", err)
				continue
			}
			for _, ws := range wsEntries {
				if !ws.IsDir() {
					continue
				}
				path := filepath.Join(wsRoot, ws.Name(), "CLAUDE.md")
				if stripped, err := stripTeamSection(path); err != nil {
					slog.Warn("cleanup_team_claudemd: strip failed", "path", path, "err", err)
				} else if stripped {
					swept++
				}
			}
		}
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO schema_migrations (key) VALUES (?)`, "cleanup_team_claudemd_v1"); err != nil {
		return fmt.Errorf("write migration marker: %w", err)
	}
	if swept > 0 {
		slog.Info("cleanup_team_claudemd: stripped orphan TEAM section", "files", swept)
	}
	return nil
}

// stripTeamSection removes the first <!-- TEAM:START --> ... <!-- TEAM:END -->
// block from the file at path. Missing files / files without the markers are
// no-ops; returns true only when a section was actually removed and rewritten.
func stripTeamSection(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	content := string(data)
	startIdx := strings.Index(content, teamSectionStart)
	if startIdx < 0 {
		return false, nil
	}
	endIdx := strings.Index(content[startIdx:], teamSectionEnd)
	if endIdx < 0 {
		return false, nil
	}
	endIdx += startIdx + len(teamSectionEnd)
	// Also consume one trailing newline so we don't leave a blank gap.
	if endIdx < len(content) && content[endIdx] == '\n' {
		endIdx++
	}
	cleaned := content[:startIdx] + content[endIdx:]
	if cleaned == content {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(cleaned), 0644); err != nil {
		return false, err
	}
	return true, nil
}
