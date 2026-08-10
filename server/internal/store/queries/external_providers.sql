-- name: CreateProvider :one
INSERT INTO external_providers (name, label, api_base_url, auth_type, auth_header, auth_prefix, profile, openapi_url, whitelist, enabled, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetProviderByID :one
SELECT * FROM external_providers WHERE id = ?;

-- name: GetProviderByName :one
SELECT * FROM external_providers WHERE name = ?;

-- name: ListProviders :many
SELECT * FROM external_providers ORDER BY name;

-- name: UpdateProvider :one
UPDATE external_providers
SET label = ?, api_base_url = ?, auth_type = ?, auth_header = ?, auth_prefix = ?,
    profile = ?, openapi_url = ?, whitelist = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: DeleteProvider :exec
DELETE FROM external_providers WHERE id = ?;

-- name: CreateAuditLog :exec
INSERT INTO external_api_audit (user_id, provider_id, method, path, status_code)
VALUES (?, ?, ?, ?, ?);

-- name: ListAuditLogs :many
SELECT * FROM external_api_audit ORDER BY created_at DESC LIMIT ?;

-- name: GetAPIWritePrefs :one
SELECT * FROM external_api_write_prefs WHERE user_id = ? AND provider_id = ?;

-- name: UpsertAPIWritePrefs :exec
INSERT INTO external_api_write_prefs (user_id, provider_id, enabled)
VALUES (?, ?, ?)
ON CONFLICT(user_id, provider_id) DO UPDATE SET enabled = excluded.enabled;

-- name: ListAPIWritePrefsForUser :many
SELECT * FROM external_api_write_prefs WHERE user_id = ?;
