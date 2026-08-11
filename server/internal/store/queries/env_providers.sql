-- name: ListEnvProviders :many
SELECT * FROM env_providers ORDER BY name ASC;

-- name: ListEnvProvidersForOwners :many
-- The third disjunct (owner_id = 0) surfaces system-wide defaults to every
-- caller regardless of their personal or org scope, mirroring env_presets.
SELECT * FROM env_providers
WHERE (owner_type = 'user' AND owner_id = ?)
   OR (owner_type = 'org'  AND owner_id IN (sqlc.slice('org_ids')))
   OR (owner_type = 'user' AND owner_id = 0)
ORDER BY created_at DESC;

-- name: GetEnvProvider :one
SELECT * FROM env_providers WHERE id = ?;

-- name: CreateEnvProvider :one
INSERT INTO env_providers (name, platform, description, base_url, protocol, api_key, model, haiku_model, sonnet_model, opus_model, subagent_model, extra_env, owner_type, owner_id, slug)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateEnvProvider :exec
UPDATE env_providers
SET name = ?, platform = ?, description = ?, base_url = ?, protocol = ?, api_key = ?, model = ?,
    haiku_model = ?, sonnet_model = ?, opus_model = ?, subagent_model = ?, extra_env = ?, slug = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteEnvProvider :exec
DELETE FROM env_providers WHERE id = ?;