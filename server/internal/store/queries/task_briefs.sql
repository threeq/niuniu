-- name: CreateTaskBrief :one
-- Append-only versions: version = MAX(version)+1 for the issue. The brief is
-- never updated in place; a scope change appends a new version.
INSERT INTO task_briefs (
    issue_id, version, goal, acceptance_json, scope_in_json, scope_out_json,
    delivery_json, provenance_json, intake_decision_id, policy_version, created_by
) VALUES (
    ?, (SELECT COALESCE(MAX(tb.version), 0) + 1 FROM task_briefs tb WHERE tb.issue_id = ?),
    ?, ?, ?, ?, ?, ?, ?, ?, ?
)
RETURNING *;

-- name: GetLatestTaskBrief :one
-- Current brief = highest version. Returns no row for issues without a brief.
SELECT * FROM task_briefs
WHERE issue_id = ?
ORDER BY version DESC
LIMIT 1;

-- name: GetTaskBriefByVersion :one
SELECT * FROM task_briefs WHERE issue_id = ? AND version = ?;

-- name: ListTaskBriefs :many
SELECT * FROM task_briefs WHERE issue_id = ? ORDER BY version DESC;
