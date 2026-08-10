-- name: GetScene :one
SELECT * FROM scenes WHERE id = ?;

-- name: GetScenesByIDs :many
-- Batched form of GetScene for resolving a workspace's scene layers in one
-- query (scene projection recompute) instead of one query per layer.
SELECT * FROM scenes WHERE id IN (sqlc.slice('ids'));

-- name: GetSceneByOwnerSlug :one
-- Lookup by (owner_type, owner_id, slug). 'builtin' scenes use the sentinel
-- (owner_type='user', owner_id=0) and are looked up the same way.
SELECT * FROM scenes
WHERE owner_type = ? AND owner_id = ? AND slug = ?;

-- name: CreateScene :one
INSERT INTO scenes
  (owner_type, owner_id, slug, display_name, description, tags, source, source_slug, definition, content_hash, enabled)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateScene :exec
UPDATE scenes
SET display_name = ?,
    description = ?,
    tags = ?,
    definition = ?,
    content_hash = ?,
    enabled = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteScene :exec
DELETE FROM scenes WHERE id = ?;

-- name: UpsertBuiltinScene :exec
-- Builtin seed path: insert-or-update by (owner_type, owner_id, slug) where
-- owner is the global sentinel (user, 0). User-forked copies live under
-- their own (owner_type, owner_id) tuple and do not collide.
INSERT INTO scenes
  (owner_type, owner_id, slug, display_name, description, tags, source, source_slug, definition, content_hash, enabled)
VALUES ('user', 0, ?, ?, ?, ?, 'builtin', ?, ?, ?, 1)
ON CONFLICT(owner_type, owner_id, slug) DO UPDATE SET
  display_name = excluded.display_name,
  description = excluded.description,
  tags = excluded.tags,
  source_slug = excluded.source_slug,
  definition = excluded.definition,
  content_hash = excluded.content_hash,
  updated_at = CURRENT_TIMESTAMP
WHERE scenes.source = 'builtin';

-- name: ListBuiltinScenes :many
SELECT * FROM scenes WHERE source = 'builtin' ORDER BY slug ASC;

-- name: CountScenesByOwner :one
SELECT COUNT(*) FROM scenes
WHERE owner_type = ? AND owner_id = ?;
