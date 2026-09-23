-- name: UpsertSessionState :exec
INSERT INTO session_state (workspace_id, session_id, repo_states, last_user_msg, snapshot_at)
VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(workspace_id, session_id) DO UPDATE SET
    repo_states   = excluded.repo_states,
    last_user_msg = excluded.last_user_msg,
    snapshot_at   = CURRENT_TIMESTAMP;

-- name: UpsertSessionLastContextTokens :exec
-- Persist the session's last observed context-window occupancy (tokens) so
-- the auto-compact heuristic survives agent/server restarts on long
-- --resume conversations. Called once per completed turn.
INSERT INTO session_state (workspace_id, session_id, last_context_tokens, snapshot_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(workspace_id, session_id) DO UPDATE SET
    last_context_tokens = excluded.last_context_tokens,
    snapshot_at   = CURRENT_TIMESTAMP;

-- name: GetSessionState :one
SELECT * FROM session_state
WHERE workspace_id = ? AND session_id = ?;

-- name: DeleteSessionState :exec
DELETE FROM session_state WHERE workspace_id = ? AND session_id = ?;

-- name: DeleteWorkspaceSessionStates :exec
DELETE FROM session_state WHERE workspace_id = ?;
