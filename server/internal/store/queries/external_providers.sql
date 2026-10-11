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
INSERT INTO external_api_audit (user_id, provider_id, credential_id, method, path, status_code, body_sha256, body_bytes, body_fields)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

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

-- name: ListOrgExternalApiAudit :many
-- Team-edition cross-user API audit feed (#743). Scoped to the members of
-- org_id, so an org admin reads what every member did. Rows carry the OWNER of
-- the credential that authenticated the call: credential_scope '' (any),
-- 'user' (members' own credentials) or 'org' (the organization's credentials)
-- -- that filter is what answers "which of our credentials was used". A call
-- whose credential predates the credential_id column (0) joins to nothing and
-- is reported as unattributed rather than guessed at. outcome maps onto the
-- HTTP status code: 'ok' < 400, 'error' >= 400. member_id/provider_id = 0
-- means "any".
--
-- Cross-tenant guard: a user may belong to several orgs, so membership alone
-- would surface a call made with ANOTHER org's credential (with that org's
-- alias and owner id) to this org's admin. Rows whose credential belongs to a
-- different org are therefore excluded outright; a member's own credential
-- (owner_type='user') and an unattributed row (credential_id 0, c NULL) stay.
--
-- project_id narrows to the integrations BOUND to that project. The audit row
-- itself has no project column (a credential is org- or user-owned, and one
-- credential can be bound to several projects), so the project is resolved
-- through project_external_sources -- the same binding the proxy uses to pick
-- a credential for an in-workspace agent. A call made with an unbound
-- credential is therefore excluded when a project filter is set, which is the
-- honest answer to "what did project X's agents do".
SELECT
    a.id, a.user_id, a.provider_id, a.credential_id, a.method, a.path,
    a.status_code, a.body_sha256, a.body_bytes, a.body_fields, a.created_at,
    COALESCE(c.owner_type, '') AS credential_owner_type,
    COALESCE(c.owner_id, 0)    AS credential_owner_id,
    COALESCE(c.alias, '')      AS credential_alias
FROM external_api_audit a
JOIN org_members m ON m.user_id = a.user_id AND m.org_id = sqlc.arg(org_id)
LEFT JOIN external_provider_credentials c ON c.id = a.credential_id
WHERE (CAST(sqlc.arg(member_id) AS BIGINT) = 0 OR a.user_id = sqlc.arg(member_id))
  AND (CAST(sqlc.arg(provider_id) AS BIGINT) = 0 OR a.provider_id = sqlc.arg(provider_id))
  AND (CAST(sqlc.arg(project_id) AS BIGINT) = 0
       OR a.credential_id IN (
            SELECT pes.credential_id FROM project_external_sources pes
            WHERE pes.project_id = sqlc.arg(project_id)
              AND pes.credential_id IS NOT NULL))
  AND (c.owner_type IS NULL OR c.owner_type <> 'org' OR c.owner_id = sqlc.arg(org_id))
  AND (CAST(sqlc.arg(credential_scope) AS TEXT) = ''
       OR COALESCE(c.owner_type, '') = CAST(sqlc.arg(credential_scope) AS TEXT))
  AND (CAST(sqlc.arg(outcome) AS TEXT) = ''
       OR (sqlc.arg(outcome) = 'ok' AND a.status_code < 400)
       OR (sqlc.arg(outcome) = 'error' AND a.status_code >= 400))
  AND a.created_at >= COALESCE(sqlc.narg(from_ts), a.created_at)
  AND a.created_at <= COALESCE(sqlc.narg(to_ts), a.created_at)
ORDER BY a.created_at DESC, a.id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CountOrgExternalApiAudit :one
SELECT COUNT(*) AS n
FROM external_api_audit a
JOIN org_members m ON m.user_id = a.user_id AND m.org_id = sqlc.arg(org_id)
LEFT JOIN external_provider_credentials c ON c.id = a.credential_id
WHERE (CAST(sqlc.arg(member_id) AS BIGINT) = 0 OR a.user_id = sqlc.arg(member_id))
  AND (CAST(sqlc.arg(provider_id) AS BIGINT) = 0 OR a.provider_id = sqlc.arg(provider_id))
  AND (CAST(sqlc.arg(project_id) AS BIGINT) = 0
       OR a.credential_id IN (
            SELECT pes.credential_id FROM project_external_sources pes
            WHERE pes.project_id = sqlc.arg(project_id)
              AND pes.credential_id IS NOT NULL))
  AND (c.owner_type IS NULL OR c.owner_type <> 'org' OR c.owner_id = sqlc.arg(org_id))
  AND (CAST(sqlc.arg(credential_scope) AS TEXT) = ''
       OR COALESCE(c.owner_type, '') = CAST(sqlc.arg(credential_scope) AS TEXT))
  AND (CAST(sqlc.arg(outcome) AS TEXT) = ''
       OR (sqlc.arg(outcome) = 'ok' AND a.status_code < 400)
       OR (sqlc.arg(outcome) = 'error' AND a.status_code >= 400))
  AND a.created_at >= COALESCE(sqlc.narg(from_ts), a.created_at)
  AND a.created_at <= COALESCE(sqlc.narg(to_ts), a.created_at);

-- name: GetAPIWritePrefs :one
SELECT * FROM external_api_write_prefs WHERE user_id = ? AND provider_id = ?;

-- name: UpsertAPIWritePrefs :exec
INSERT INTO external_api_write_prefs (user_id, provider_id, enabled)
VALUES (?, ?, ?)
ON CONFLICT(user_id, provider_id) DO UPDATE SET enabled = excluded.enabled;

-- name: ListAPIWritePrefsForUser :many
SELECT * FROM external_api_write_prefs WHERE user_id = ?;
