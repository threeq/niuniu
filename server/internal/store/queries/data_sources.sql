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
