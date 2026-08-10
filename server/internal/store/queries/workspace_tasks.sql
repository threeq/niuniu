-- name: GetLatestBatchId :one
SELECT batch_id FROM workspace_tasks
WHERE workspace_id = ? AND batch_id != ''
ORDER BY created_at DESC
LIMIT 1;

-- name: ListWorkspaceTasksByBatch :many
SELECT * FROM workspace_tasks
WHERE workspace_id = ? AND batch_id = ?
AND status != 'deleted'
ORDER BY created_at ASC;

-- name: UpsertWorkspaceTask :one
INSERT INTO workspace_tasks (
    workspace_id, agent_task_id, subject, description,
    active_form, status, phase, message_id, batch_id, harness_run_id, started_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (workspace_id, agent_task_id) DO UPDATE SET
    subject = excluded.subject,
    description = excluded.description,
    active_form = excluded.active_form,
    status = excluded.status,
    phase = excluded.phase,
    message_id = excluded.message_id,
    started_at = COALESCE(excluded.started_at, workspace_tasks.started_at),
    completed_at = excluded.completed_at,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: InterruptInProgressTasks :exec
UPDATE workspace_tasks
SET status = 'interrupted', updated_at = CURRENT_TIMESTAMP
WHERE workspace_id = ? AND status = 'in_progress';

-- name: InterruptInProgressTasksByBatch :exec
UPDATE workspace_tasks
SET status = 'interrupted', updated_at = CURRENT_TIMESTAMP
WHERE workspace_id = ? AND batch_id = ? AND status = 'in_progress';

-- name: UpdateWorkspaceTaskByAgent :one
-- CAST() forces sqlc to generate string-typed params instead of interface{}
UPDATE workspace_tasks SET
    subject = CASE WHEN CAST(sqlc.arg(new_subject) AS TEXT) != '' THEN CAST(sqlc.arg(new_subject) AS TEXT) ELSE subject END,
    active_form = CASE WHEN CAST(sqlc.arg(new_active_form) AS TEXT) != '' THEN CAST(sqlc.arg(new_active_form) AS TEXT) ELSE active_form END,
    status = CAST(sqlc.arg(new_status) AS TEXT),
    started_at = CASE WHEN CAST(sqlc.arg(new_status) AS TEXT) = 'in_progress' AND started_at IS NULL THEN CURRENT_TIMESTAMP ELSE started_at END,
    completed_at = CASE WHEN CAST(sqlc.arg(new_status) AS TEXT) = 'completed' THEN CURRENT_TIMESTAMP ELSE completed_at END,
    updated_at = CURRENT_TIMESTAMP
WHERE workspace_id = sqlc.arg(workspace_id) AND agent_task_id = CAST(sqlc.arg(agent_task_id) AS TEXT)
RETURNING *;

-- name: ListInProgressTasks :many
SELECT * FROM workspace_tasks
WHERE workspace_id = ? AND status = 'in_progress';

-- name: GetLatestBatchTaskStats :one
SELECT
  COUNT(*) as total,
  SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) as completed
FROM workspace_tasks
WHERE workspace_id = ? AND batch_id = ? AND status != 'deleted';

-- name: GetInProgressTaskActiveForm :one
SELECT active_form FROM workspace_tasks
WHERE workspace_id = ? AND batch_id = ? AND status = 'in_progress'
LIMIT 1;

-- name: GetLatestBatchTaskStatsForWorkspaces :many
-- Batched form of GetLatestBatchId + GetLatestBatchTaskStats +
-- GetInProgressTaskActiveForm for the workspace sidebar. For every listed
-- workspace it picks the most recent non-empty batch (ROW_NUMBER window) and
-- returns that batch's task totals plus one in-progress active_form, in a
-- single query instead of up to three per workspace.
WITH ranked AS (
  SELECT
    workspace_tasks.workspace_id AS workspace_id,
    workspace_tasks.batch_id AS batch_id,
    ROW_NUMBER() OVER (PARTITION BY workspace_tasks.workspace_id ORDER BY workspace_tasks.created_at DESC) AS rn
  FROM workspace_tasks
  WHERE workspace_tasks.workspace_id IN (sqlc.slice('workspace_ids')) AND workspace_tasks.batch_id != ''
),
latest AS (
  SELECT ranked.workspace_id AS workspace_id, ranked.batch_id AS batch_id
  FROM ranked
  WHERE ranked.rn = 1
)
SELECT
  l.workspace_id,
  COUNT(*) AS total,
  SUM(CASE WHEN t.status = 'completed' THEN 1 ELSE 0 END) AS completed,
  MAX(CASE WHEN t.status = 'in_progress' THEN t.active_form ELSE NULL END) AS active_form
FROM latest l
JOIN workspace_tasks t
  ON t.workspace_id = l.workspace_id AND t.batch_id = l.batch_id AND t.status != 'deleted'
GROUP BY l.workspace_id;

-- name: MarkBatchTasksDeletedExcept :exec
-- Marks every non-deleted task in (workspace_id, batch_id) whose
-- agent_task_id is NOT in keep_ids as 'deleted'. Used by handleTodoWrite to
-- reconcile a TodoWrite array (which is the full desired state) against
-- previously-recorded tasks for the same batch.
UPDATE workspace_tasks
SET status = 'deleted', updated_at = CURRENT_TIMESTAMP
WHERE workspace_id = ?
  AND batch_id = ?
  AND status != 'deleted'
  AND agent_task_id NOT IN (sqlc.slice('keep_ids'));
