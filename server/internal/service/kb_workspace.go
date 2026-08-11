package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// WorkspaceKB mounting makes a knowledge base a first-class workspace citizen,
// mirroring how a repository is checked out as a worktree: mounting a KB
// materializes its source content read-only into <workspace>/datasets/<name>/
// and auto-ingests it, so the workspace agent can Read/Grep/Glob it directly and
// the file tree shows it as a browsable directory. Per-workspace explicit
// mounts REPLACE the old project-implicit inheritance for workspace visibility.

// WorkspaceKBMount is a knowledge base explicitly mounted to a workspace.
type WorkspaceKBMount struct {
	KBID        int64     `json:"kb_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	SourceKind  string    `json:"source_kind"`
	DatasetPath string    `json:"dataset_path"` // read-only materialized dir inside the workspace tree
	MountedAt   time.Time `json:"mounted_at,omitempty"`
}

// workspaceDatasetDir returns the read-only dataset dir for a mounted KB inside
// the workspace path: <wsPath>/datasets/<sanitized-name>. The name is sanitized
// with the same rules as worktree branch names (sanitizeBranch); a name that
// sanitizes to empty falls back to kb-<id> so the dir is always stable + unique
// (KB names are unique per owner).
func workspaceDatasetDir(wsPath, kbName string, kbID int64) string {
	name := sanitizeBranch(kbName)
	if name == "" {
		name = fmt.Sprintf("kb-%d", kbID)
	}
	return filepath.Join(wsPath, "datasets", name)
}

// ListWorkspaceKBs returns the knowledge bases explicitly mounted to a
// workspace, with their materialized (read-only) dataset path. Owner-scoped:
// a workspace owned by another tenant, or a KB owned by another tenant, is never
// returned.
func (s *KBService) ListWorkspaceKBs(ctx context.Context, owner OwnerRef, workspaceID int64) ([]WorkspaceKBMount, error) {
	if err := owner.Validate(); err != nil {
		return nil, err
	}
	if err := s.assertWorkspaceOwner(ctx, owner, workspaceID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListMountedKBsForWorkspace(ctx, store.ListMountedKBsForWorkspaceParams{
		WorkspaceID: workspaceID, OwnerType: owner.Type, OwnerID: owner.ID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]WorkspaceKBMount, 0, len(rows))
	for _, r := range rows {
		out = append(out, WorkspaceKBMount{
			KBID:        r.ID,
			Name:        r.Name,
			Description: r.Description,
			SourceKind:  r.SourceKind,
			DatasetPath: r.DatasetPath,
			MountedAt:   r.CreatedAt,
		})
	}
	return out, nil
}

// MountKB mounts a knowledge base to a workspace: it materializes the KB source
// content read-only into <workspace>/datasets/<name>/ (visible in the file tree)
// and auto-ingests it so the mounted content is immediately searchable
// (attach = sync). Both the KB and the workspace must belong to owner. The
// operation is idempotent: mounting an already-mounted KB is a no-op returning
// the existing mount. Materialization/ingest are best-effort (a url source not
// yet downloaded does not fail the mount; the dataset dir is then empty until a
// sync resolves it).
func (s *KBService) MountKB(ctx context.Context, owner OwnerRef, workspaceID, kbID int64) (WorkspaceKBMount, error) {
	if err := owner.Validate(); err != nil {
		return WorkspaceKBMount{}, err
	}
	kb, err := s.GetKB(ctx, owner, kbID) // ownership gate on the KB
	if err != nil {
		return WorkspaceKBMount{}, err
	}
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return WorkspaceKBMount{}, fmt.Errorf("workspace not found: %w", err)
	}
	if ws.OwnerType != owner.Type || ws.OwnerID != owner.ID {
		return WorkspaceKBMount{}, fmt.Errorf("workspace not found")
	}

	dir := workspaceDatasetDir(ws.Path, kb.Name, kb.ID)

	// Idempotent: already mounted returns the existing mount.
	if m, gerr := s.q.GetWorkspaceKB(ctx, store.GetWorkspaceKBParams{WorkspaceID: workspaceID, KbID: kbID}); gerr == nil {
		return WorkspaceKBMount{
			KBID: kbID, Name: kb.Name, Description: kb.Description,
			SourceKind: kb.SourceKind, DatasetPath: m.DatasetPath, MountedAt: m.CreatedAt,
		}, nil
	} else if !errors.Is(gerr, sql.ErrNoRows) {
		return WorkspaceKBMount{}, fmt.Errorf("check existing mount: %w", gerr)
	}

	// Materialize the read-only copy first (best-effort; a source that can't
	// resolve yet leaves an empty dir that a later sync fills).
	if mErr := s.materializeDataset(ctx, owner, kb, dir); mErr != nil {
		slog.Warn("kb: materialize dataset dir on mount failed (best-effort)",
			"kb_id", kb.ID, "workspace_id", workspaceID, "err", mErr)
	}

	if _, err := s.q.CreateWorkspaceKB(ctx, store.CreateWorkspaceKBParams{
		WorkspaceID: workspaceID, KbID: kbID, DatasetPath: dir,
	}); err != nil {
		return WorkspaceKBMount{}, fmt.Errorf("mount kb: %w", err)
	}

	// Attach = sync: auto-ingest so the mounted corpus is immediately searchable.
	// content-hash idempotency makes re-mounts cheap.
	if _, ierr := s.Ingest(ctx, owner, kbID, IngestOptions{}); ierr != nil {
		slog.Warn("kb: auto-ingest on mount failed (best-effort)", "kb_id", kbID, "err", ierr)
	}

	return WorkspaceKBMount{
		KBID: kbID, Name: kb.Name, Description: kb.Description,
		SourceKind: kb.SourceKind, DatasetPath: dir, MountedAt: time.Now(),
	}, nil
}

// UnmountKB removes a knowledge base from a workspace and deletes its
// materialized (read-only) dataset dir. Idempotent: unmounting a KB that is not
// mounted is a no-op. Directory removal is best-effort (a leftover dir never
// blocks the unmount).
func (s *KBService) UnmountKB(ctx context.Context, owner OwnerRef, workspaceID, kbID int64) error {
	if err := owner.Validate(); err != nil {
		return err
	}
	if err := s.assertWorkspaceOwner(ctx, owner, workspaceID); err != nil {
		return err
	}
	if _, err := s.GetKB(ctx, owner, kbID); err != nil { // ownership gate on the KB
		return err
	}
	m, err := s.q.GetWorkspaceKB(ctx, store.GetWorkspaceKBParams{WorkspaceID: workspaceID, KbID: kbID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // not mounted: idempotent no-op
		}
		return fmt.Errorf("get mount: %w", err)
	}
	if err := s.q.DeleteWorkspaceKBByWorkspaceAndKB(ctx, store.DeleteWorkspaceKBByWorkspaceAndKBParams{
		WorkspaceID: workspaceID, KbID: kbID,
	}); err != nil {
		return fmt.Errorf("unmount kb: %w", err)
	}
	if m.DatasetPath != "" {
		if rerr := os.RemoveAll(m.DatasetPath); rerr != nil {
			slog.Warn("kb: remove dataset dir on unmount failed (best-effort)",
				"kb_id", kbID, "path", m.DatasetPath, "err", rerr)
		}
	}
	return nil
}

// SyncWorkspaceKB re-materializes a mounted KB's content into its workspace
// dataset dir and force re-ingests it, so a changed source (new files, edited
// corpus) propagates into both the file tree and the search index. The KB must
// be mounted to the workspace.
func (s *KBService) SyncWorkspaceKB(ctx context.Context, owner OwnerRef, workspaceID, kbID int64) error {
	if err := owner.Validate(); err != nil {
		return err
	}
	if err := s.assertWorkspaceOwner(ctx, owner, workspaceID); err != nil {
		return err
	}
	kb, err := s.GetKB(ctx, owner, kbID)
	if err != nil {
		return err
	}
	m, err := s.q.GetWorkspaceKB(ctx, store.GetWorkspaceKBParams{WorkspaceID: workspaceID, KbID: kbID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("kb %d is not mounted to workspace %d", kbID, workspaceID)
		}
		return fmt.Errorf("get mount: %w", err)
	}
	if err := s.materializeDataset(ctx, owner, kb, m.DatasetPath); err != nil {
		return err
	}
	_, err = s.Ingest(ctx, owner, kbID, IngestOptions{Force: true})
	return err
}

// materializeDataset copies the KB's resolved source content (read-only) into
// destDir inside the workspace tree. A source that cannot resolve on disk
// (url not yet downloaded, missing local dir) returns an error so the caller can
// treat it as best-effort.
func (s *KBService) materializeDataset(ctx context.Context, owner OwnerRef, kb store.KnowledgeBase, destDir string) error {
	root, err := s.resolveSourceRoot(owner, kb)
	if err != nil {
		return err // not-yet-downloaded url source / missing local dir: skip
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	return copyDirContent(root, destDir)
}

// assertWorkspaceOwner verifies workspaceID belongs to owner (tenant isolation).
func (s *KBService) assertWorkspaceOwner(ctx context.Context, owner OwnerRef, workspaceID int64) error {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("workspace not found: %w", err)
	}
	if ws.OwnerType != owner.Type || ws.OwnerID != owner.ID {
		return fmt.Errorf("workspace not found")
	}
	return nil
}