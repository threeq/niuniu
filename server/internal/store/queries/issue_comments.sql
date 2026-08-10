-- name: ListIssueComments :many
SELECT * FROM issue_comments WHERE issue_id = ? ORDER BY created_at ASC;

-- name: CreateIssueComment :one
INSERT INTO issue_comments (issue_id, author, content) VALUES (?, ?, ?) RETURNING *;

-- name: UpdateIssueComment :one
UPDATE issue_comments SET content = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? RETURNING *;

-- name: DeleteIssueComment :exec
DELETE FROM issue_comments WHERE id = ?;

-- name: GetIssueComment :one
SELECT * FROM issue_comments WHERE id = ?;
