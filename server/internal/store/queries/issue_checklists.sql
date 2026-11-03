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

-- name: ListChecklistsByIssueAndKind :many
SELECT * FROM issue_checklists WHERE issue_id = ? AND kind = ? ORDER BY position;

-- name: CreateChecklistWithKind :one
-- Materializes a Brief acceptance item (kind='acceptance', brief_id set) at the
-- next position. Manual checklist rows keep using CreateChecklist.
INSERT INTO issue_checklists (issue_id, title, position, kind, brief_id)
VALUES (?, ?, COALESCE((SELECT MAX(ic.position) FROM issue_checklists ic WHERE ic.issue_id = ?), -1) + 1, ?, ?)
RETURNING *;

-- name: DeleteAcceptanceChecklistsForIssue :exec
-- Regenerating acceptance items removes only the Brief-materialized rows; manual
-- kind='task' rows are never touched.
DELETE FROM issue_checklists WHERE issue_id = ? AND kind = 'acceptance';

-- name: DeleteAcceptanceChecklistsForBrief :exec
DELETE FROM issue_checklists WHERE brief_id = ?;

-- name: GetChecklistStatsByKind :one
SELECT
    COUNT(*) as total,
    CAST(COALESCE(SUM(CASE WHEN is_completed = 1 THEN 1 ELSE 0 END), 0) AS INTEGER) as completed
FROM issue_checklists WHERE issue_id = ? AND kind = ?;
