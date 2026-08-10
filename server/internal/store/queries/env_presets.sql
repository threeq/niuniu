-- name: ListEnvPresets :many
SELECT * FROM env_presets ORDER BY name ASC;

-- name: GetEnvPreset :one
SELECT * FROM env_presets WHERE id = ?;

-- name: GetEnvPresetCount :one
SELECT COUNT(*) FROM env_presets;

-- name: CreateEnvPreset :one
INSERT INTO env_presets (name, description, env, owner_type, owner_id)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateEnvPreset :exec
UPDATE env_presets
SET name = ?, description = ?, env = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteEnvPreset :exec
DELETE FROM env_presets WHERE id = ?;
