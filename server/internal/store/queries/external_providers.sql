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
INSERT INTO external_api_audit (user_id, provider_id, method, path, status_code, body_sha256, body_bytes, body_fields)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListExternalApiAudit :many
-- Caller-scoped audit feed, newest first (id DESC tracks insert order and is
-- stable across same-second rows, unlike created_at). provider_id = 0 means
-- "any provider". from_ts/to_ts are nullable (NULL = unbounded); COALESCE
-- anchors the parameter type to the created_at column so Postgres can infer it
-- at parse time, and makes the NULL case a no-op comparison.
SELECT * FROM external_api_audit
WHERE user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(provider_id) AS BIGINT) = 0 OR provider_id = sqlc.arg(provider_id))
  AND created_at >= COALESCE(sqlc.narg(from_ts), created_at)
  AND created_at <= COALESCE(sqlc.narg(to_ts), created_at)
ORDER BY id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CountExternalApiAudit :one
SELECT COUNT(*) AS n FROM external_api_audit
WHERE user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(provider_id) AS BIGINT) = 0 OR provider_id = sqlc.arg(provider_id))
  AND created_at >= COALESCE(sqlc.narg(from_ts), created_at)
  AND created_at <= COALESCE(sqlc.narg(to_ts), created_at);

-- name: GetAPIWritePrefs :one
SELECT * FROM external_api_write_prefs WHERE user_id = ? AND provider_id = ?;

-- name: UpsertAPIWritePrefs :exec
INSERT INTO external_api_write_prefs (user_id, provider_id, enabled)
VALUES (?, ?, ?)
ON CONFLICT(user_id, provider_id) DO UPDATE SET enabled = excluded.enabled;

-- name: ListAPIWritePrefsForUser :many
SELECT * FROM external_api_write_prefs WHERE user_id = ?;
