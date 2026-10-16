-- name: CreateWorkspaceShare :one
INSERT INTO workspace_shares (workspace_id, token_hash, password_hash, expires_at, created_by)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: ListWorkspaceSharesByWorkspace :many
SELECT * FROM workspace_shares
WHERE workspace_id = ?
ORDER BY created_at DESC, id DESC;

-- name: GetWorkspaceShareByTokenHash :one
SELECT * FROM workspace_shares
WHERE token_hash = ?;

-- name: GetWorkspaceShare :one
SELECT * FROM workspace_shares
WHERE id = ? AND workspace_id = ?;

-- name: RevokeWorkspaceShare :exec
UPDATE workspace_shares
SET revoked_at = CURRENT_TIMESTAMP
WHERE id = ? AND workspace_id = ? AND revoked_at IS NULL;

-- name: TouchWorkspaceShareAccess :exec
UPDATE workspace_shares
SET last_access_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteExpiredWorkspaceShares :exec
-- Retention sweep: rows already past their expiry (plus a grace window so the
-- owner still sees recently-expired shares in the list) are physical-deleted.
DELETE FROM workspace_shares
WHERE expires_at < ?;
