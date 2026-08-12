-- name: ListProjects :many
SELECT * FROM projects WHERE status = ? ORDER BY created_at DESC;

-- name: GetProject :one
SELECT * FROM projects WHERE id = ?;

-- name: SetProjectEnvProvider :exec
UPDATE projects SET env_provider_id = ? WHERE id = ?;

-- name: GetProjectByOwnerAndName :one
-- Project names are unique per owner (idx_projects_owner_name_unique), not
-- globally. Scope the lookup by owner so one owner's project never blocks
-- another owner from reusing the same name.
SELECT * FROM projects WHERE owner_type = ? AND owner_id = ? AND name = ?;

-- name: CreateProject :one
-- default_cli_type: empty string falls back to 'claude' so callers that omit it
-- still satisfy the closed set. sqlc.arg cast mirrors CreateWorkspace's pattern
-- (avoids PG 42P18 on an untyped ?).
INSERT INTO projects (name, description, status, owner_type, owner_id, color, default_cli_type)
VALUES (?, ?, 'active', ?, ?, ?, COALESCE(NULLIF(CAST(sqlc.arg(default_cli_type) AS TEXT), ''), 'claude')) RETURNING *;

-- name: UpdateProjectDefaultCliType :one
-- Mirrors UpdateProjectColor's dedicated-update pattern. Validated against the
-- closed set (ValidCliTypes) in the service layer.
UPDATE projects SET default_cli_type = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? RETURNING *;

-- name: UpdateProject :one
UPDATE projects SET name = ?, description = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? RETURNING *;

-- name: UpdateProjectMemorySweepCron :exec
UPDATE projects SET memory_sweep_cron = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: UpdateProjectCleanupPolicy :exec
-- Store a project's workspace auto-cleanup policy. Values are validated at the
-- service layer (cleanup_statuses is a comma-separated subset of the known set).
UPDATE projects
SET cleanup_enabled = ?, cleanup_inactive_days = ?, cleanup_statuses = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = ?;

-- name: GetIssueStatsByProject :many
SELECT c.project_id, c.name AS column_name, CAST(COUNT(i.id) AS INTEGER) AS issue_count
FROM columns c
LEFT JOIN issues i ON i.column_id = c.id
GROUP BY c.project_id, c.id
ORDER BY c.project_id, c.position;

-- name: GetWorkspaceStatsByProject :many
SELECT c.project_id, w.status, CAST(COUNT(w.id) AS INTEGER) AS ws_count
FROM workspaces w
JOIN issues i ON w.issue_id = i.id
JOIN columns c ON i.column_id = c.id
GROUP BY c.project_id, w.status
ORDER BY c.project_id, w.status;

-- name: UpdateProjectStatus :one
UPDATE projects SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? RETURNING *;

-- name: UpdateProjectColor :one
-- color: pass sql.NullString{Valid: false} to clear (NULL); pass {String: "emerald", Valid: true} to set.
-- Two-query partial update pattern (avoids COALESCE + PG 42P18 on untyped ?).
UPDATE projects SET color = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? RETURNING *;
