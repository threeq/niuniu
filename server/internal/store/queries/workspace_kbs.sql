-- name: CreateWorkspaceKB :one
-- Records an explicit workspace -> KB mount. Unique per (workspace, kb); the
-- dataset_path is the read-only materialized dir inside the workspace tree.
INSERT INTO workspace_kbs (workspace_id, kb_id, dataset_path)
VALUES (?, ?, ?) RETURNING *;

-- name: GetWorkspaceKB :one
SELECT * FROM workspace_kbs WHERE workspace_id = ? AND kb_id = ?;

-- name: DeleteWorkspaceKBByWorkspaceAndKB :exec
DELETE FROM workspace_kbs WHERE workspace_id = ? AND kb_id = ?;

-- name: ListWorkspaceKBsForWorkspace :many
SELECT * FROM workspace_kbs WHERE workspace_id = ? ORDER BY created_at ASC;

-- name: ListMountedKBsForWorkspace :many
-- KBs explicitly mounted to a workspace, joined with their dataset_path so the
-- service can expose the read-only dir (and fall back to project-bound KBs when
-- a workspace has no explicit mounts). owner_type/owner_id enforce tenant
-- isolation; a disabled KB is excluded so disabling stops both kb_search and
-- the agent direct-read mount.
SELECT kb.id, kb.name, kb.description, kb.source_kind, kb.source_addr, kb.status,
       wk.dataset_path, wk.created_at
FROM workspace_kbs wk
JOIN knowledge_bases kb ON kb.id = wk.kb_id
WHERE wk.workspace_id = ?
  AND kb.owner_type = ?
  AND kb.owner_id = ?
  AND kb.status <> 'disabled'
ORDER BY wk.created_at ASC;