-- name: ListGlobalHarnessSpecs :many
-- harness_specs is one global library (no owner / no scope columns).
SELECT * FROM harness_specs ORDER BY category, name;

-- name: GetHarnessSpec :one
SELECT * FROM harness_specs WHERE id = ?;

-- name: GetHarnessSpecsByIDs :many
-- Batched form of GetHarnessSpec for resolving many specs at once (e.g. the
-- gate-spec briefs across a project's columns) instead of one query per spec.
SELECT * FROM harness_specs WHERE id IN (sqlc.slice('ids'));

-- name: CreateHarnessSpec :one
INSERT INTO harness_specs (
    category, name, enabled, severity, config,
    kind, target, pattern, pattern_flags, command, timeout_sec,
    expected_exit_code, extract_regex, threshold_value, threshold_op,
    file_paths, trigger_on, judge_prompt, judge_model
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateHarnessSpec :exec
UPDATE harness_specs SET
    enabled = ?, severity = ?, config = ?,
    kind = ?, target = ?, pattern = ?, pattern_flags = ?, command = ?,
    timeout_sec = ?, expected_exit_code = ?, extract_regex = ?,
    threshold_value = ?, threshold_op = ?, file_paths = ?, trigger_on = ?,
    judge_prompt = ?, judge_model = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteHarnessSpec :exec
DELETE FROM harness_specs WHERE id = ?;

-- name: ListHarnessSpecsByTrigger :many
SELECT * FROM harness_specs
WHERE enabled = 1 AND trigger_on = ?
ORDER BY category, name;

-- GetProjectIDForWorkspace is defined in workspaces.sql; reuse it from there
-- for project-scoped pre_commit and on_schedule lookup (per workspace-model
-- architecture: workspace <-> issue <-> project is 1:1).

-- name: CreateHarnessCheck :one
INSERT INTO harness_checks (workspace_id, run_id, spec_id, phase_name, status, message, details, duration_ms, cost_usd)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListHarnessChecksByRun :many
SELECT hc.*, hs.category, hs.name AS spec_name, hs.severity
FROM harness_checks hc
JOIN harness_specs hs ON hc.spec_id = hs.id
WHERE hc.run_id = ?
ORDER BY hc.created_at DESC;

-- name: ListHarnessChecksByWorkspace :many
SELECT hc.*, hs.category, hs.name AS spec_name, hs.severity
FROM harness_checks hc
JOIN harness_specs hs ON hc.spec_id = hs.id
WHERE hc.workspace_id = ?
ORDER BY hc.created_at DESC;
