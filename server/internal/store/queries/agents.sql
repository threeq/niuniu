-- name: ListAgents :many
SELECT * FROM agents ORDER BY name ASC;

-- name: GetAgent :one
SELECT * FROM agents WHERE id = ?;

-- name: GetAgentByName :one
SELECT * FROM agents WHERE name = ?;

-- name: CreateAgent :one
INSERT INTO agents (name, description, dir_path, file_hash, source_url, owner_type, owner_id)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateAgent :exec
UPDATE agents
SET description = ?, dir_path = ?, file_hash = ?, source_url = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteAgent :exec
DELETE FROM agents WHERE id = ?;
