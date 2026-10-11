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
