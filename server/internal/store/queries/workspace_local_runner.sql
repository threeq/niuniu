-- name: GetWorkspaceLocalRunner :one
SELECT * FROM workspace_local_runner WHERE workspace_id = ?;

-- name: UpsertWorkspaceLocalRunner :one
INSERT INTO workspace_local_runner (
    workspace_id, local_dir, prompt_snippet, allowed_commands, always_allow_persist, updated_at
) VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT (workspace_id) DO UPDATE SET
    local_dir = excluded.local_dir,
    prompt_snippet = excluded.prompt_snippet,
    allowed_commands = excluded.allowed_commands,
    always_allow_persist = excluded.always_allow_persist,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: DeleteWorkspaceLocalRunner :exec
DELETE FROM workspace_local_runner WHERE workspace_id = ?;
