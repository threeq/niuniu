-- name: ListWorkspaceEnv :many
SELECT * FROM workspace_env WHERE workspace_id = ? ORDER BY key;

-- name: GetWorkspaceEnv :many
SELECT * FROM workspace_env WHERE workspace_id = ? AND key = ?;

-- name: SetWorkspaceEnv :exec
INSERT INTO workspace_env (workspace_id, key, value) VALUES (?, ?, ?)
ON CONFLICT(workspace_id, key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP;

-- name: DeleteWorkspaceEnv :exec
DELETE FROM workspace_env WHERE workspace_id = ? AND key = ?;

-- name: DeleteAllWorkspaceEnv :exec
DELETE FROM workspace_env WHERE workspace_id = ?;
