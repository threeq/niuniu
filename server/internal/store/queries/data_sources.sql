-- name: CreateDataSource :one
INSERT INTO data_sources
  (owner_type, owner_id, user_id, name, kind, config, scope_config, default_access_mode, require_confirm)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: GetDataSource :one
SELECT * FROM data_sources WHERE id = ?;

-- name: ListDataSourcesForOwners :many
SELECT * FROM data_sources
WHERE (owner_type = 'user' AND owner_id = ?)
   OR (owner_type = 'org'  AND owner_id IN (sqlc.slice('org_ids')))
ORDER BY created_at DESC;

-- name: UpdateDataSource :exec
UPDATE data_sources
SET name = ?, config = ?, scope_config = ?, default_access_mode = ?, require_confirm = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: TouchDataSourceVerified :exec
UPDATE data_sources SET last_verified_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: DeleteDataSource :exec
DELETE FROM data_sources WHERE id = ?;

-- name: CreateDataSourceAudit :exec
INSERT INTO data_source_audit
  (user_id, source_id, access_mode, database_name, objects, statement_summary, status, rows, duration_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListDataSourceAudit :many
-- Caller-scoped data-query audit feed, newest first. source_id = 0 means "any
-- source"; from_ts/to_ts are nullable (NULL = unbounded). Same COALESCE-anchored
-- time comparison as ListExternalApiAudit, for the same Postgres-typing reason.
SELECT * FROM data_source_audit
WHERE user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(source_id) AS BIGINT) = 0 OR source_id = sqlc.arg(source_id))
  AND created_at >= COALESCE(sqlc.narg(from_ts), created_at)
  AND created_at <= COALESCE(sqlc.narg(to_ts), created_at)
ORDER BY id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CountDataSourceAudit :one
SELECT COUNT(*) AS n FROM data_source_audit
WHERE user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(source_id) AS BIGINT) = 0 OR source_id = sqlc.arg(source_id))
  AND created_at >= COALESCE(sqlc.narg(from_ts), created_at)
  AND created_at <= COALESCE(sqlc.narg(to_ts), created_at);

-- name: ListOrgDataSourceAudit :many
-- Team-edition cross-user data-query audit feed (#743), mirroring
-- ListOrgExternalApiAudit. Scoped to the members of org_id; rows carry the
-- data source's owner so credential_scope ('user' / 'org') can narrow the feed
-- to queries against the organization's own sources. outcome maps onto the
-- proxy's own status vocabulary: 'ok' or 'error'. project_id narrows to the
-- sources BOUND to that project, resolved through data_source_bindings (the
-- same binding that scopes a source to a workspace's agent).
SELECT
    d.id, d.user_id, d.source_id, d.access_mode, d.database_name, d.objects,
    d.statement_summary, d.status, d.rows, d.duration_ms, d.created_at,
    COALESCE(s.owner_type, '') AS source_owner_type,
    COALESCE(s.owner_id, 0)    AS source_owner_id,
    COALESCE(s.name, '')       AS source_name
FROM data_source_audit d
JOIN org_members m ON m.user_id = d.user_id AND m.org_id = sqlc.arg(org_id)
LEFT JOIN data_sources s ON s.id = d.source_id
WHERE (CAST(sqlc.arg(member_id) AS BIGINT) = 0 OR d.user_id = sqlc.arg(member_id))
  AND (CAST(sqlc.arg(source_id) AS BIGINT) = 0 OR d.source_id = sqlc.arg(source_id))
  AND (CAST(sqlc.arg(project_id) AS BIGINT) = 0
       OR d.source_id IN (
            SELECT b.source_id FROM data_source_bindings b
            WHERE b.target_type = 'project'
              AND b.target_id = sqlc.arg(project_id)))
  AND (s.owner_type IS NULL OR s.owner_type <> 'org' OR s.owner_id = sqlc.arg(org_id))
  AND (CAST(sqlc.arg(credential_scope) AS TEXT) = ''
       OR COALESCE(s.owner_type, '') = CAST(sqlc.arg(credential_scope) AS TEXT))
  AND (CAST(sqlc.arg(outcome) AS TEXT) = ''
       OR (sqlc.arg(outcome) = 'ok' AND d.status = 'ok')
       OR (sqlc.arg(outcome) = 'error' AND d.status <> 'ok'))
  AND d.created_at >= COALESCE(sqlc.narg(from_ts), d.created_at)
  AND d.created_at <= COALESCE(sqlc.narg(to_ts), d.created_at)
ORDER BY d.created_at DESC, d.id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CountOrgDataSourceAudit :one
SELECT COUNT(*) AS n
FROM data_source_audit d
JOIN org_members m ON m.user_id = d.user_id AND m.org_id = sqlc.arg(org_id)
LEFT JOIN data_sources s ON s.id = d.source_id
WHERE (CAST(sqlc.arg(member_id) AS BIGINT) = 0 OR d.user_id = sqlc.arg(member_id))
  AND (CAST(sqlc.arg(source_id) AS BIGINT) = 0 OR d.source_id = sqlc.arg(source_id))
  AND (CAST(sqlc.arg(project_id) AS BIGINT) = 0
       OR d.source_id IN (
            SELECT b.source_id FROM data_source_bindings b
            WHERE b.target_type = 'project'
              AND b.target_id = sqlc.arg(project_id)))
  AND (s.owner_type IS NULL OR s.owner_type <> 'org' OR s.owner_id = sqlc.arg(org_id))
  AND (CAST(sqlc.arg(credential_scope) AS TEXT) = ''
       OR COALESCE(s.owner_type, '') = CAST(sqlc.arg(credential_scope) AS TEXT))
  AND (CAST(sqlc.arg(outcome) AS TEXT) = ''
       OR (sqlc.arg(outcome) = 'ok' AND d.status = 'ok')
       OR (sqlc.arg(outcome) = 'error' AND d.status <> 'ok'))
  AND d.created_at >= COALESCE(sqlc.narg(from_ts), d.created_at)
  AND d.created_at <= COALESCE(sqlc.narg(to_ts), d.created_at);

-- name: ListDataSourceNamesByIDs :many
-- id -> name map for rendering the audit feed's source column. Never selects
-- config/scope_config, so no credential material can leak through it.
SELECT id, name FROM data_sources
WHERE id IN (sqlc.slice('ids'))
ORDER BY id;

-- name: CreateDataSourceBinding :exec
-- Callers delete all bindings for the source first (DeleteBindingsForSource)
-- and re-insert, so no ON CONFLICT clause is needed.
INSERT INTO data_source_bindings (source_id, target_type, target_id)
VALUES (?, ?, ?);

-- name: DeleteBindingsForSource :exec
DELETE FROM data_source_bindings WHERE source_id = ?;

-- name: DeleteDataSourceBinding :exec
DELETE FROM data_source_bindings
WHERE source_id = ? AND target_type = ? AND target_id = ?;

-- name: CountDataSourceBinding :one
SELECT COUNT(*) AS n FROM data_source_bindings
WHERE source_id = ? AND target_type = ? AND target_id = ?;

-- name: ListBindingsForSource :many
SELECT id, source_id, target_type, target_id, created_at
FROM data_source_bindings
WHERE source_id = ?
ORDER BY target_type, target_id;

-- name: ListSourceIDsBoundToTarget :many
SELECT source_id FROM data_source_bindings
WHERE target_type = ? AND target_id = ?;
