-- name: ListCommentsByWorkspace :many
SELECT * FROM comments WHERE workspace_id = ? ORDER BY created_at;

-- name: GetComment :one
SELECT * FROM comments WHERE id = ?;

-- name: CreateComment :one
INSERT INTO comments (workspace_id, repo, file_path, line_number, content) VALUES (?, ?, ?, ?, ?) RETURNING *;

-- name: MarkCommentSent :exec
UPDATE comments SET sent_to_agent = TRUE WHERE id = ?;
