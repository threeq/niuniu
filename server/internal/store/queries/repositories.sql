-- name: ListRepositories :many
SELECT * FROM repositories ORDER BY created_at DESC;

-- name: GetRepository :one
SELECT * FROM repositories WHERE id = ?;

-- name: GetRepositoryByName :one
SELECT * FROM repositories WHERE name = ?;

-- name: CreateRepository :one
INSERT INTO repositories (name, path, git_remote, default_branch, owner_type, owner_id) VALUES (?, ?, ?, ?, ?, ?) RETURNING *;

-- name: UpdateRepository :one
UPDATE repositories SET name = ?, path = ?, git_remote = ?, default_branch = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? RETURNING *;

-- name: DeleteRepository :exec
DELETE FROM repositories WHERE id = ?;