-- name: CreateMCPSessionToken :exec
INSERT INTO mcp_session_tokens (workspace_id, token_hash, expires_at)
VALUES (?, ?, ?);

-- name: GetMCPSessionByHash :one
SELECT * FROM mcp_session_tokens WHERE token_hash = ?;

-- name: RenewMCPSessionToken :exec
UPDATE mcp_session_tokens SET expires_at = ? WHERE token_hash = ?;

-- name: RenewMCPSessionsForWorkspace :exec
UPDATE mcp_session_tokens SET expires_at = ? WHERE workspace_id = ?;

-- name: DeleteMCPSessionsForWorkspace :exec
DELETE FROM mcp_session_tokens WHERE workspace_id = ?;

-- name: DeleteExpiredMCPSessions :exec
DELETE FROM mcp_session_tokens WHERE expires_at < CURRENT_TIMESTAMP;
