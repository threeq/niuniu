-- name: ListChecklistsByIssue :many
SELECT * FROM issue_checklists WHERE issue_id = ? ORDER BY position;

-- name: CreateChecklist :one
INSERT INTO issue_checklists (issue_id, title, position)
VALUES (?, ?, COALESCE((SELECT MAX(ic.position) FROM issue_checklists ic WHERE ic.issue_id = ?), -1) + 1)
RETURNING *;

-- name: UpdateChecklist :one
UPDATE issue_checklists SET title = ?, is_completed = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? RETURNING *;

-- name: UpdateChecklistPosition :exec
UPDATE issue_checklists SET position = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: DeleteChecklist :exec
DELETE FROM issue_checklists WHERE id = ?;

-- name: GetChecklist :one
SELECT * FROM issue_checklists WHERE id = ?;

-- name: GetChecklistStats :one
SELECT
    COUNT(*) as total,
    CAST(COALESCE(SUM(CASE WHEN is_completed = 1 THEN 1 ELSE 0 END), 0) AS INTEGER) as completed
FROM issue_checklists WHERE issue_id = ?;
