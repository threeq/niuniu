-- name: ListQuickActions :many
SELECT * FROM quick_actions ORDER BY position, id;

-- name: GetQuickAction :one
SELECT * FROM quick_actions WHERE id = ?;

-- name: CreateQuickAction :one
INSERT INTO quick_actions (label, content, auto_send, position, owner_type, owner_id)
VALUES (?, ?, ?, ?, ?, ?) RETURNING *;

-- name: UpdateQuickAction :one
UPDATE quick_actions
SET label = ?, content = ?, auto_send = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: UpdateQuickActionPosition :exec
UPDATE quick_actions SET position = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: DeleteQuickAction :exec
DELETE FROM quick_actions WHERE id = ?;
