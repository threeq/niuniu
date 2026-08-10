-- name: ListIssuesByColumn :many
SELECT * FROM issues WHERE column_id = ? ORDER BY position;

-- name: GetIssue :one
SELECT * FROM issues WHERE id = ?;

-- name: GetIssueDetail :one
-- Single-roundtrip detail fetch: issue row + project_id (avoids the
-- handler's separate GetColumn call) + checklist stats (avoids the
-- separate GetChecklistStats call). Two correlated subqueries hit the
-- (issue_id, position) index on issue_checklists; for a single issue
-- this is cheap and keeps SQLite/Postgres parity (no LATERAL, no
-- GROUP BY mismatch).
SELECT
    i.*,
    c.project_id AS project_id,
    (SELECT CAST(COUNT(*) AS INTEGER) FROM issue_checklists WHERE issue_id = i.id) AS checklist_total,
    (SELECT CAST(COALESCE(SUM(CASE WHEN is_completed = 1 THEN 1 ELSE 0 END), 0) AS INTEGER) FROM issue_checklists WHERE issue_id = i.id) AS checklist_completed
FROM issues i
JOIN columns c ON c.id = i.column_id
WHERE i.id = ?;

-- name: CreateIssue :one
INSERT INTO issues (column_id, title, description, priority, position, start_date, due_date, estimate_type, estimate, parent_issue_id, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: SetIssueExecFields :one
-- Executable Epic: set hierarchy + execution fields in one shot. Used by the
-- create path (post-insert extras), MCP create/update, the Epic execution
-- engine, and the UI hierarchy endpoint. Empty issue_type/exec_status must be
-- normalised to 'task'/'idle' by the caller (the CHECK rejects '').
UPDATE issues SET
    parent_issue_id = ?, issue_type = ?, exec_wave = ?, exec_status = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: SetIssueExecStatus :exec
-- Lightweight Epic exec_status transition (idle/running/done/failed/paused),
-- used by the execution engine without touching other fields.
UPDATE issues SET exec_status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: ListAttentionIssuesForOwners :many
-- Issues needing human attention (spec section 19): blocked-needs-human /
-- waiting-user-input / abandoned exec_status, scoped to the caller's accessible
-- owners (personal + member orgs) via the issue's project owner. owner_id anchors
-- to a typed column (PG parse-safe); org_ids is a slice (caller passes {-1} when empty).
SELECT i.* FROM issues i
JOIN columns col ON col.id = i.column_id
JOIN projects p ON p.id = col.project_id
WHERE i.exec_status IN ('gate_blocked','waiting_input','abandoned')
  AND ((p.owner_type = 'user' AND p.owner_id = ?)
    OR (p.owner_type = 'org'  AND p.owner_id IN (sqlc.slice('org_ids'))))
ORDER BY i.updated_at DESC;

-- name: SetIssueExecStatusWithReason :exec
-- Terminal-state transition that also records a human-readable reason
-- (blocked-needs-human / abandoned-with-reason, spec section 19). All params
-- anchor to typed columns so the query is PG parse-safe.
UPDATE issues SET exec_status = ?, exec_status_reason = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: SetIssueType :exec
-- Set issue_type ('task'|'epic'). issue_type is auto-derived from child presence
-- (an issue with children is an Epic), so this is driven by the service when a
-- parent gains/loses children, never chosen by the user.
UPDATE issues SET issue_type = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: ListIssuesByParent :many
-- Children of an Epic, ordered by wave then board position. Drives Epic
-- progress aggregation and wave-based execution.
SELECT * FROM issues WHERE parent_issue_id = ? ORDER BY exec_wave, position;

-- name: UpdateIssue :one
UPDATE issues SET
    title = ?, description = ?, priority = ?,
    start_date = ?, due_date = ?, estimate_type = ?, estimate = ?, actual_time = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: MoveIssue :exec
UPDATE issues SET column_id = ?, position = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: DeleteIssue :exec
DELETE FROM issues WHERE id = ?;

-- name: UpdateIssueLifecycle :one
UPDATE issues SET lifecycle_status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? RETURNING *;

-- name: ListIssuesByProject :many
SELECT i.* FROM issues i
JOIN columns c ON i.column_id = c.id
WHERE c.project_id = ?
ORDER BY c.position, i.position;

-- name: GetProjectNameByIssueID :one
SELECT p.name FROM projects p
JOIN columns c ON c.project_id = p.id
JOIN issues i ON i.column_id = c.id
WHERE i.id = ?;

-- name: GetProjectAndLifecycleByIssueID :one
SELECT p.name AS project_name, p.owner_type AS project_owner_type, p.owner_id AS project_owner_id, p.default_cli_type AS project_default_cli_type, i.lifecycle_status, i.issue_type, i.parent_issue_id
FROM issues i
JOIN columns c ON i.column_id = c.id
JOIN projects p ON c.project_id = p.id
WHERE i.id = ?;

-- name: GetProjectAndLifecycleByIssueIDs :many
-- Batched form of GetProjectAndLifecycleByIssueID for the workspace sidebar,
-- which resolves project/lifecycle for every listed workspace at once instead
-- of one query per workspace.
SELECT i.id AS issue_id, p.name AS project_name, p.owner_type AS project_owner_type, p.owner_id AS project_owner_id, p.default_cli_type AS project_default_cli_type, i.lifecycle_status, i.issue_type, i.parent_issue_id
FROM issues i
JOIN columns c ON i.column_id = c.id
JOIN projects p ON c.project_id = p.id
WHERE i.id IN (sqlc.slice('issue_ids'));

-- name: ListAvailableIssuesForWorkspace :many
SELECT i.*, p.name AS project_name, c.project_id FROM issues i
JOIN columns c ON i.column_id = c.id
JOIN projects p ON c.project_id = p.id
WHERE (i.lifecycle_status IS NULL OR i.lifecycle_status != 'completed')
AND i.id NOT IN (SELECT w.issue_id FROM workspaces w WHERE w.issue_id IS NOT NULL AND w.is_archived = 0)
ORDER BY p.name, i.id;

-- name: LockIssueForUpdate :one
-- Acquires a row-level lock on the issue within the current transaction to
-- prevent concurrent StartRun calls from racing past the pre-check and
-- hitting the unique index. On PostgreSQL the *store.DB wrapper appends
-- FOR UPDATE at call time; on SQLite db.SetMaxOpenConns(1) already serialises
-- writers, so the plain SELECT is a no-op lock equivalent.
SELECT id FROM issues WHERE id = ?;

-- (GetIssueProjectRepositories was removed; service.GetIssueDefaultRepos
-- now resolves issue -> column -> project_id and reuses ListProjectRepositories,
-- which already returns project_default_branch alongside repo fields. Avoids
-- a sqlc-on-Windows truncation quirk that mangled the trailing ORDER BY when
-- this query coexisted with a multi-line SELECT in the same file. ASCII-only
-- comment per the bug above.)

-- name: GetIssueGoalCondition :one
SELECT goal_condition FROM issues WHERE id = ?;

-- name: UpdateIssueGoalCondition :exec
UPDATE issues SET goal_condition = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: UpdateIssuePriority :exec
-- Lightweight single-field update used by the batch priority endpoint.
UPDATE issues SET priority = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;
