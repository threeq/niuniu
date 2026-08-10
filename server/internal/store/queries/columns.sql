-- name: ListColumnsByProject :many
SELECT * FROM columns WHERE project_id = ? ORDER BY position;

-- name: GetColumn :one
SELECT * FROM columns WHERE id = ?;

-- name: GetColumnsByIDs :many
-- Batched form of GetColumn for callers resolving many columns at once (e.g. the
-- attention-issues list, which maps each issue to its column/project for deep
-- links) instead of one query per row.
SELECT * FROM columns WHERE id IN (sqlc.slice('ids'));

-- name: CreateColumn :one
INSERT INTO columns (project_id, name, position, lifecycle_mapping) VALUES (?, ?, ?, ?) RETURNING *;

-- name: UpdateColumn :one
UPDATE columns SET name = ? WHERE id = ? RETURNING *;

-- name: UpdateColumnLifecycleMapping :one
UPDATE columns SET lifecycle_mapping = ? WHERE id = ? RETURNING *;

-- name: UpdateColumnPosition :exec
UPDATE columns SET position = ? WHERE id = ?;

-- name: DeleteColumn :exec
DELETE FROM columns WHERE id = ?;

-- name: UpdateColumnExtension :one
UPDATE columns
SET reviewer_agent = ?,
    phase_prompt   = ?,
    auto_advance   = ?
WHERE id = ?
RETURNING *;
