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
INSERT INTO env_providers (name, platform, description, base_urls, api_key, model, haiku_model, sonnet_model, opus_model, subagent_model, extra_env, context_window, group_name, group_position, enabled, owner_type, owner_id, slug)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateEnvProvider :exec
UPDATE env_providers
SET name = ?, platform = ?, description = ?, base_urls = ?, api_key = ?, model = ?,
    haiku_model = ?, sonnet_model = ?, opus_model = ?, subagent_model = ?, extra_env = ?, context_window = ?, group_name = ?, group_position = ?, enabled = ?, slug = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: SetProviderEnabled :exec
-- Manual on/off switch: enabled=0 takes the provider out of rotation (group
-- fallback and binding both skip it) until the user re-enables it.
UPDATE env_providers SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: SetProviderGroupPosition :exec
-- Manual order within a group; smaller = used first as fallback. Reorder is a
-- dedicated operation so it does not clobber the rest of the provider config.
UPDATE env_providers SET group_position = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: SetProviderCooldown :exec
-- Marks a provider as rate-limited until the given reset time (cooldown_until).
-- While the value is in the future, sceneenv.ActiveProvider skips the provider
-- and falls back to another member of its group.
UPDATE env_providers SET cooldown_until = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: ClearProviderCooldown :exec
-- Clears a provider's rate-limit cooldown (user-initiated reset, or when the
-- stored value has passed and the provider should be retried immediately).
UPDATE env_providers SET cooldown_until = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: DeleteEnvProvider :exec
DELETE FROM env_providers WHERE id = ?;
