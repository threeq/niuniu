-- name: ListLayersForWorkspace :many
SELECT * FROM workspace_scene_layers
WHERE workspace_id = ?
ORDER BY position ASC;

-- name: GetBaseLayer :one
SELECT * FROM workspace_scene_layers
WHERE workspace_id = ? AND is_base = 1
LIMIT 1;

-- name: InsertBaseLayer :exec
-- Insert a base layer row. Callers must check for existence first via
-- GetBaseLayer or use UpdateBaseLayer for re-saves.
--
-- NOTE: a single-statement ON CONFLICT(workspace_id) WHERE is_base = 1 ...
-- DO UPDATE upsert was attempted (partial-index conflict target supported
-- by SQLite 3.24+ and PG), but sqlc v1.30.0's query parser fails on the
-- partial-WHERE clause. Splitting into InsertBaseLayer + UpdateBaseLayer
-- per plan Sec 1.8 fallback. Service code (BasePromotion) is responsible for
-- the SELECT-then-insert-or-update sequence inside a tx.
INSERT INTO workspace_scene_layers
  (workspace_id, scene_id, position, is_base, base_definition)
VALUES (?, NULL, 0, 1, ?);

-- name: UpdateBaseLayer :exec
UPDATE workspace_scene_layers
SET base_definition = ?, updated_at = CURRENT_TIMESTAMP
WHERE workspace_id = ? AND is_base = 1;

-- name: AttachLayer :one
INSERT INTO workspace_scene_layers
  (workspace_id, scene_id, position, is_base, base_definition)
VALUES (?, ?, ?, 0, '')
RETURNING id, workspace_id, scene_id, position, is_base, base_definition,
          created_at, updated_at;

-- name: UpdateLayerPosition :exec
UPDATE workspace_scene_layers
SET position = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND is_base = 0;

-- name: DetachLayer :exec
DELETE FROM workspace_scene_layers WHERE id = ? AND is_base = 0;

-- name: NextLayerPosition :one
SELECT COALESCE(MAX(position), 0) + 1 AS next_pos
FROM workspace_scene_layers
WHERE workspace_id = ?;
