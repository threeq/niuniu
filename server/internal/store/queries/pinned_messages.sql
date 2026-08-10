-- name: ListPinnedMessages :many
SELECT * FROM pinned_messages
WHERE workspace_id = ?
ORDER BY created_at ASC, id ASC;

-- name: CreatePinnedMessage :one
-- Upsert: re-pinning the same message refreshes the captured preview/role.
INSERT INTO pinned_messages (workspace_id, message_id, role, preview)
VALUES (?, ?, ?, ?)
ON CONFLICT (workspace_id, message_id) DO UPDATE SET
    role = excluded.role,
    preview = excluded.preview
RETURNING *;

-- name: DeletePinnedMessage :exec
DELETE FROM pinned_messages
WHERE id = ? AND workspace_id = ?;
