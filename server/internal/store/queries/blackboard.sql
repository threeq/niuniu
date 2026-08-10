-- name: ListBlackboardEntries :many
SELECT * FROM blackboard_entries WHERE workspace_id = ? ORDER BY created_at DESC;

-- name: ListBlackboardEntriesByType :many
SELECT * FROM blackboard_entries WHERE workspace_id = ? AND entry_type = ? ORDER BY created_at DESC;

-- name: GetBlackboardEntry :one
SELECT * FROM blackboard_entries WHERE workspace_id = ? AND entry_key = ?;

-- name: UpsertBlackboardEntry :one
INSERT INTO blackboard_entries (workspace_id, producer_agent, entry_type, entry_key, content, metadata, ref_path, harness_run_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(workspace_id, entry_key) DO UPDATE SET
  content = excluded.content, metadata = excluded.metadata, producer_agent = excluded.producer_agent
RETURNING *;

-- name: DeleteBlackboardEntry :exec
DELETE FROM blackboard_entries WHERE workspace_id = ? AND entry_key = ?;

-- name: ClearBlackboardForWorkspace :exec
DELETE FROM blackboard_entries WHERE workspace_id = ?;
