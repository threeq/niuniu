-- name: UpsertSessionState :exec
INSERT INTO session_state (workspace_id, session_id, repo_states, last_user_msg, snapshot_at)
VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(workspace_id, session_id) DO UPDATE SET
    repo_states   = excluded.repo_states,
    last_user_msg = excluded.last_user_msg,
    snapshot_at   = CURRENT_TIMESTAMP;

-- name: GetSessionState :one
SELECT * FROM session_state
WHERE workspace_id = ? AND session_id = ?;

-- name: DeleteSessionState :exec
DELETE FROM session_state WHERE workspace_id = ? AND session_id = ?;

-- name: DeleteWorkspaceSessionStates :exec
DELETE FROM session_state WHERE workspace_id = ?;
