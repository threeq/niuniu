-- name: ListWorkspaces :many
SELECT * FROM workspaces WHERE is_temporary = 0 AND is_archived = 0 ORDER BY created_at DESC;

-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = ?;

-- name: GetWorkspaceCliType :one
SELECT cli_type FROM workspaces WHERE id = ?;

-- name: GetWorkspaceEnvProviderID :one
-- 0 means no direct provider binding (NULL COALESCE'd); callers fall back to
-- scene-declared providers/presets.
SELECT COALESCE(env_provider_id, 0) AS env_provider_id FROM workspaces WHERE id = ?;

-- name: SetWorkspaceEnvProvider :exec
UPDATE workspaces SET env_provider_id = ? WHERE id = ?;

-- name: GetWorkspacesByIssue :many
SELECT * FROM workspaces WHERE issue_id = ? ORDER BY is_archived ASC, created_at DESC;

-- name: ListProjectWorkspacesForCleanup :many
-- Live (non-archived, not mid-delete) workspaces bound to an issue in a project,
-- with the issue status pair and the last-activity signal, for auto-cleanup.
-- last_activity_at is NULL when the workspace has had no AI/user interaction; the
-- service falls back to updated_at in that case.
SELECT
    w.id               AS workspace_id,
    i.id               AS issue_id,
    w.agent_status     AS agent_status,
    w.session_status   AS session_status,
    w.updated_at       AS updated_at,
    i.lifecycle_status AS lifecycle_status,
    i.exec_status      AS exec_status,
    st.last_activity_at AS last_activity_at
FROM workspaces w
JOIN issues  i ON w.issue_id  = i.id
JOIN columns c ON i.column_id = c.id
LEFT JOIN workspace_stats st ON st.workspace_id = w.id
WHERE c.project_id = ?
  AND w.is_archived = 0
  AND w.status <> 'deleting';

-- name: ListProjectPlansWithWorkspace :many
-- One row per project issue that has a backing workspace, paired with its primary
-- workspace id (non-archived newest, else archived newest), in a single statement.
-- Replaces an N+1 (ListIssuesByProject then GetWorkspacesByIssue per issue) on the
-- IM inbound / classifier / issues-list hot path. Order matches ListIssuesByProject
-- (c.position, i.position) so the /use index numbering stays stable.
SELECT i.id, i.title, i.column_id, w.id AS workspace_id
FROM issues i
JOIN columns c ON c.id = i.column_id
JOIN workspaces w ON w.id = (
    SELECT w2.id FROM workspaces w2 WHERE w2.issue_id = i.id
    ORDER BY w2.is_archived ASC, w2.created_at DESC LIMIT 1
)
WHERE c.project_id = ?
ORDER BY c.position, i.position;

-- name: CreateWorkspace :one
-- Default empty cli_type to 'claude' so callers that omit it (existing tests
-- and back-compat code paths) still satisfy the CHECK (cli_type IN
-- ('claude','codex')) constraint. Explicit 'claude' or 'codex' bypasses the
-- COALESCE branch and hits the CHECK directly.
-- CAST(... AS TEXT) anchors the parameter type for sqlc generation and for
-- the PG parser so this query does not trip SQLSTATE 42P18 on Postgres
-- (see CLAUDE.md "Known PG-on-server pitfalls").
-- sqlc.arg(cli_type) is safe here because ConvertPlaceholders (in
-- store/pgplaceholder.go) recognizes the `?N` numbered form sqlc emits and
-- rewrites it to `$N` without leaking the trailing digit. See the comment
-- on ConvertPlaceholders for the failure mode that motivated that fix.
--
-- codex_sandbox_mode/codex_approval_policy are written as literals here
-- because legacy SQLite installs added these columns via ALTER TABLE with
-- the OLD defaults ('workspace-write'/'on-failure'). SQLite does not
-- support changing a column's DEFAULT after the fact, so relying on the
-- schema DEFAULT would silently give new codex workspaces the old (and
-- now wrong) sandbox/approval combo until the next server restart fires
-- the post-startup UPDATE in addWorkspaceCodexSandboxColumns. Writing the
-- literals here removes that startup-window race so newly created codex
-- workspaces immediately get the correct danger-full-access/never combo.
-- Claude workspaces ignore these columns, so the literals are harmless
-- there.
INSERT INTO workspaces (issue_id, name, path, status, owner_type, owner_id, created_by, cli_type, codex_sandbox_mode, codex_approval_policy, language)
VALUES (?, ?, ?, ?, ?, ?, ?, COALESCE(NULLIF(CAST(sqlc.arg(cli_type) AS TEXT), ''), 'claude'), 'danger-full-access', 'never', CAST(sqlc.arg(language) AS TEXT))
RETURNING *;

-- name: UpdateWorkspaceName :exec
UPDATE workspaces SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: UpdateWorkspaceStatus :exec
UPDATE workspaces SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: UpdateAgentStatus :exec
UPDATE workspaces SET agent_pid = ?, agent_status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: DeleteWorkspace :exec
DELETE FROM workspaces WHERE id = ?;

-- name: MarkWorkspaceDeleting :execrows
-- Atomic guard for async batch delete: flips status to 'deleting' only when the
-- workspace is not already being deleted. The affected-row count is the dedup
-- gate -- 0 rows means a concurrent (or repeated) request already claimed it, so
-- the caller skips it instead of starting a second cleanup goroutine.
-- status != 'deleting' anchors the parameter against the typed status column so
-- PG infers its type at parse time (no SQLSTATE 42P18).
UPDATE workspaces SET status = 'deleting', updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status != 'deleting';

-- name: UpdateWorkspacePath :one
UPDATE workspaces SET path = ? WHERE id = ? RETURNING *;

-- name: SetWorkspaceStudio :exec
-- is_studio = ? anchors the parameter against the typed is_studio column so PG
-- can infer its type at parse time (no SQLSTATE 42P18).
UPDATE workspaces SET is_studio = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: UpdateWorkspaceStatusConditional :exec
UPDATE workspaces SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status = ?;

-- name: UpdateSessionColumns :exec
UPDATE workspaces
SET session_id = ?, session_status = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: GetProjectIDForWorkspace :one
SELECT c.project_id FROM workspaces w
JOIN issues i ON w.issue_id = i.id
JOIN columns c ON i.column_id = c.id
WHERE w.id = ?;

-- name: CountSchedulesByWorkspace :many
SELECT workspace_id, COUNT(*) AS count
FROM workspace_schedules
WHERE enabled = 1
GROUP BY workspace_id;

-- name: ArchiveWorkspace :exec
UPDATE workspaces SET is_archived = 1, archived_at = CURRENT_TIMESTAMP, path = '', updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: ListArchivedWorkspaces :many
SELECT * FROM workspaces WHERE is_archived = 1 ORDER BY archived_at DESC;

-- name: ListArchivedWorkspacesWithMeta :many
SELECT w.*,
  COALESCE(i.title, '') AS issue_title,
  COALESCE(p.name, '') AS project_name
FROM workspaces w
LEFT JOIN issues i ON w.issue_id = i.id
LEFT JOIN columns c ON i.column_id = c.id
LEFT JOIN projects p ON c.project_id = p.id
WHERE w.is_archived = 1
ORDER BY w.archived_at DESC;

-- name: UpdateWorkspaceSessionUser :exec
UPDATE workspaces SET current_session_user_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: GetWorkspaceAlertableUserIDs :many
-- Returns the deduplicated, NULL-dropped union of:
--   - workspaces.created_by (if non-NULL)
--   - issue_assignees.user_id where issue_id matches the workspace's linked issue
-- Used by emit sites of workspace-targeted toast events to populate
-- extra.should_alert_user_ids.
SELECT DISTINCT user_id FROM (
    SELECT created_by AS user_id FROM workspaces w
    WHERE w.id = sqlc.arg(workspace_id) AND created_by IS NOT NULL
    UNION
    SELECT ia.user_id FROM issue_assignees ia
    JOIN workspaces w ON w.issue_id = ia.issue_id
    WHERE w.id = sqlc.arg(workspace_id)
) sub;

-- name: UpdateWorkspaceMcpServers :exec
UPDATE workspaces
SET mcp_servers = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: UpdateWorkspaceStrictMCP :exec
UPDATE workspaces
SET strict_mcp_config = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?;
