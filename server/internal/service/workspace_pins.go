package service

import (
	"context"
	"database/sql"
	"errors"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// PinWorkspace pins a workspace to the top of the caller's sidebar list.
// Idempotent: re-pinning refreshes pinned_at (jumps to the top of the pinned
// zone). Authz (workspace access) is the handler's responsibility.
func (s *WorkspaceService) PinWorkspace(ctx context.Context, userID, workspaceID int64) error {
	return s.q.PinWorkspace(ctx, store.PinWorkspaceParams{UserID: userID, WorkspaceID: workspaceID})
}

// UnpinWorkspace removes the caller's pin for a workspace. No-op if not pinned.
func (s *WorkspaceService) UnpinWorkspace(ctx context.Context, userID, workspaceID int64) error {
	return s.q.UnpinWorkspace(ctx, store.UnpinWorkspaceParams{UserID: userID, WorkspaceID: workspaceID})
}

// ListPinnedWorkspaceIDs returns the caller's pinned workspace ids, ordered
// most-recently-pinned first. Returns an empty slice for an unauthenticated
// caller. Stale ids (archived / out-of-scope workspaces) are the frontend's to
// filter -- it resolves ids against the currently visible workspace list.
func (s *WorkspaceService) ListPinnedWorkspaceIDs(ctx context.Context, userID int64) ([]int64, error) {
	if userID <= 0 {
		return []int64{}, nil
	}
	ids, err := s.q.ListPinnedWorkspaceIDsForUser(ctx, userID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if ids == nil {
		ids = []int64{}
	}
	return ids, nil
}
