-- name: ListActivitiesByIssue :many
SELECT * FROM issue_activities WHERE issue_id = ? ORDER BY created_at ASC;

-- name: CreateActivity :exec
INSERT INTO issue_activities (issue_id, action, field, old_value, new_value, author) VALUES (?, ?, ?, ?, ?, ?);
