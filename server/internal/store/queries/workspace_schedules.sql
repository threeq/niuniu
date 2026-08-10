-- name: ListSchedules :many
SELECT * FROM workspace_schedules
WHERE workspace_id = ?
ORDER BY created_at ASC;

-- name: GetSchedule :one
SELECT * FROM workspace_schedules WHERE id = ?;

-- name: CreateSchedule :one
INSERT INTO workspace_schedules (workspace_id, name, default_message, schedule_type, cron_expr, run_at, action_kind)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateSchedule :exec
UPDATE workspace_schedules
SET name = ?, default_message = ?, schedule_type = ?, cron_expr = ?, run_at = ?, action_kind = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteSchedule :exec
DELETE FROM workspace_schedules WHERE id = ?;

-- name: PruneOrphanedWorkspaceSchedules :exec
-- Delete schedules whose workspace no longer exists (avoids FK errors from scheduler).
DELETE FROM workspace_schedules WHERE workspace_id NOT IN (SELECT id FROM workspaces);

-- name: ToggleSchedule :exec
UPDATE workspace_schedules
SET enabled = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: MarkScheduleFired :exec
UPDATE workspace_schedules
SET fired_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: MarkScheduleRan :exec
UPDATE workspace_schedules
SET last_run_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DisableSchedule :exec
UPDATE workspace_schedules
SET enabled = 0, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DisableSchedulesByWorkspace :exec
UPDATE workspace_schedules
SET enabled = 0, updated_at = CURRENT_TIMESTAMP
WHERE workspace_id = ?;

-- name: ListEnabledSchedules :many
SELECT * FROM workspace_schedules
WHERE enabled = 1;

-- name: ListUnfiredOnceSchedules :many
SELECT * FROM workspace_schedules
WHERE enabled = 1 AND schedule_type = 'once' AND fired_at IS NULL;

-- name: ListAllSchedulesWithWorkspace :many
SELECT ws.*, w.name AS workspace_name, w.status AS workspace_status
FROM workspace_schedules ws
JOIN workspaces w ON w.id = ws.workspace_id
ORDER BY ws.workspace_id, ws.created_at ASC;

-- name: CountEnabledSchedulesByWorkspace :many
SELECT workspace_id, COUNT(*) AS count
FROM workspace_schedules
WHERE enabled = 1
GROUP BY workspace_id;
