-- name: ListProjectDefaults :many
-- Joins scenes for display purposes (slug / display_name / source).
SELECT pds.id          AS id,
       pds.project_id  AS project_id,
       pds.scene_id    AS scene_id,
       pds.position    AS position,
       pds.created_at  AS created_at,
       s.display_name  AS scene_display_name,
       s.slug          AS scene_slug,
       s.source        AS scene_source
FROM project_default_scenes pds
JOIN scenes s ON s.id = pds.scene_id
WHERE pds.project_id = ?
ORDER BY pds.position ASC;

-- name: AttachProjectDefault :one
INSERT INTO project_default_scenes (project_id, scene_id, position)
VALUES (?, ?, ?)
RETURNING *;

-- name: DetachProjectDefault :exec
DELETE FROM project_default_scenes
WHERE project_id = ? AND scene_id = ?;

-- name: ReorderProjectDefault :exec
UPDATE project_default_scenes
SET position = ?
WHERE project_id = ? AND scene_id = ?;

-- name: NextProjectDefaultPosition :one
SELECT COALESCE(MAX(position), -1) + 1 AS next_pos
FROM project_default_scenes
WHERE project_id = ?;
