-- name: ListEnvAccounts :many
SELECT * FROM env_accounts ORDER BY name ASC;

-- name: ListEnvAccountsForOwners :many
-- The third disjunct (owner_id = 0) surfaces system-wide defaults to every
-- caller regardless of their personal or org scope, mirroring env_presets.
SELECT * FROM env_accounts
WHERE (owner_type = 'user' AND owner_id = ?)
   OR (owner_type = 'org'  AND owner_id IN (sqlc.slice('org_ids')))
   OR (owner_type = 'user' AND owner_id = 0)
ORDER BY created_at DESC;

-- name: GetEnvAccount :one
SELECT * FROM env_accounts WHERE id = ?;

-- name: GetEnvAccountByName :one
SELECT * FROM env_accounts WHERE name = ?;

-- name: CreateEnvAccount :one
INSERT INTO env_accounts (name, platform, description, api_key, owner_type, owner_id, slug)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateEnvAccount :exec
UPDATE env_accounts
SET name = ?, platform = ?, description = ?, api_key = ?, slug = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteEnvAccount :exec
DELETE FROM env_accounts WHERE id = ?;