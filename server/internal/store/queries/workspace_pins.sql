-- Per-user workspace pins for the overview page. All comments are pure ASCII
-- (a stray non-ASCII byte silently truncates a later query -- see CLAUDE.md).

-- name: PinWorkspace :exec
-- Idempotent: re-pinning refreshes pinned_at so the row jumps to the top of
-- the pinned section. user_id / workspace_id anchor the ON CONFLICT target.
INSERT INTO workspace_pins (user_id, workspace_id, pinned_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT (user_id, workspace_id) DO UPDATE SET pinned_at = CURRENT_TIMESTAMP;

-- name: UnpinWorkspace :exec
DELETE FROM workspace_pins WHERE user_id = ? AND workspace_id = ?;

-- name: ListPinnedWorkspaceIDsForUser :many
-- Ordered most-recently-pinned first, matching the sidebar pinned zone order.
SELECT workspace_id FROM workspace_pins WHERE user_id = ? ORDER BY pinned_at DESC, id DESC;
