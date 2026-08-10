package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// MigrateWTToWorktrees migrates existing wt/ directories to worktrees/ format
// and updates the database paths accordingly.
func MigrateWTToWorktrees(ctx context.Context, q *store.Queries) error {
	// List all worktrees with old wt/ paths
	wtList, err := q.ListAllWorktrees(ctx)
	if err != nil {
		return fmt.Errorf("list workspace repos: %w", err)
	}

	for _, wt := range wtList {
		if !strings.Contains(wt.WorktreePath, "/wt/") {
			continue // Already migrated or new format
		}

		// Old path: .../wt/reponame-branch-repoId
		// New path: .../worktrees/{repoId}_{wsId}_{branchName}

		wtDir := filepath.Dir(wt.WorktreePath)   // .../wt
		wtBase := filepath.Base(wt.WorktreePath) // reponame-branch-repoId
		wsDir := filepath.Dir(wtDir)             // .../workspaceId

		// Parse repoId from suffix
		parts := strings.Split(wtBase, "-")
		if len(parts) < 2 {
			continue // Invalid format, skip
		}

		// Generate new worktree path using the new format
		// branch name is sanitized - reconstruct from old path (all parts except last which is repoId)
		nameBranch := strings.Join(parts[:len(parts)-1], "-")
		newWorktreeDir := fmt.Sprintf("%d_%d_%s", wt.RepositoryID, wt.WorkspaceID, sanitizeBranch(nameBranch))
		newWorktreePath := filepath.Join(wsDir, "worktrees", newWorktreeDir)

		// Rename directory on disk: wt/ -> worktrees/
		newWtDir := filepath.Join(wsDir, "worktrees")
		if _, err := os.Stat(newWtDir); os.IsNotExist(err) {
			if err := os.MkdirAll(newWtDir, 0755); err != nil {
				return fmt.Errorf("create worktrees dir: %w", err)
			}
		}

		oldWorktreeDir := filepath.Join(wtDir) // full path to the worktree dir
		if _, err := os.Stat(oldWorktreeDir); err == nil {
			if err := os.Rename(oldWorktreeDir, newWorktreePath); err != nil {
				return fmt.Errorf("rename worktree dir from %s to %s: %w", oldWorktreeDir, newWorktreePath, err)
			}
		}

		// Update DB
		if err := q.UpdateWorktreePath(ctx, store.UpdateWorktreePathParams{
			ID:           wt.ID,
			WorktreePath: newWorktreePath,
		}); err != nil {
			return fmt.Errorf("update worktree_path for id %d: %w", wt.ID, err)
		}
	}

	return nil
}

// sanitizeBranch replaces special chars and truncates if > 128 chars
func sanitizeBranch(name string) string {
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_",
	)
	result := replacer.Replace(name)
	if len(result) > 128 {
		h := sha256.Sum256([]byte(name))
		result = result[:120] + "_" + hex.EncodeToString(h[:4])
	}
	return result
}
