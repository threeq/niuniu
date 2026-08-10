-- name: CreateLabel :one
INSERT INTO labels (project_id, name, color, description, created_by)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetLabel :one
SELECT * FROM labels WHERE id = ?;

-- name: GetLabelByProjectAndName :one
SELECT * FROM labels WHERE project_id = ? AND name = ?;

-- name: ListLabelsByProject :many
SELECT * FROM labels WHERE project_id = ? ORDER BY name;

-- name: ListLabelsByProjectWithUsage :many
SELECT
    l.*,
    (SELECT COUNT(*) FROM issue_labels il WHERE il.label_id = l.id) AS usage_count
FROM labels l
WHERE l.project_id = ?
ORDER BY l.name;

-- name: UpdateLabel :one
UPDATE labels SET name = ?, color = ?, description = ?
WHERE id = ?
RETURNING *;

-- name: DeleteLabel :exec
DELETE FROM labels WHERE id = ?;

-- name: ListLabelsByIssue :many
SELECT l.* FROM labels l
JOIN issue_labels il ON il.label_id = l.id
WHERE il.issue_id = ?
ORDER BY l.name;

-- name: ListLabelsByIssueIDs :many
SELECT il.issue_id, l.* FROM labels l
JOIN issue_labels il ON il.label_id = l.id
WHERE il.issue_id IN (sqlc.slice('issue_ids'))
ORDER BY il.issue_id, l.name;
