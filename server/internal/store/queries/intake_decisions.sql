-- name: CreateIntakeDecision :one
INSERT INTO intake_decisions (
    event_id, project_id, mode_at_decision, strength, decision, executed,
    reason_code, reason_text, matched_rule_ids_json, policy_version,
    issue_id, workspace_id, would_do_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetIntakeDecision :one
SELECT * FROM intake_decisions WHERE id = ?;

-- name: ListPendingIntakeDecisions :many
-- Calibration queue: decisions recorded but not executed (observe mode) for
-- the requested decision outcome, newest first.
SELECT * FROM intake_decisions
WHERE project_id = ? AND executed = 0 AND decision = ?
ORDER BY created_at DESC, id DESC;

-- name: ListIntakeDecisionsByProject :many
SELECT * FROM intake_decisions
WHERE project_id = ?
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: ListIntakeDecisionsByEvent :many
SELECT * FROM intake_decisions WHERE event_id = ? ORDER BY created_at DESC, id DESC;

-- name: SetIntakeDecisionExecuted :one
-- Acceptance: mark a dry-run decision as executed and record the issue or
-- workspace it produced. The decision must still be pending, so concurrent
-- accepts cannot both create a task.
UPDATE intake_decisions
SET executed = 1, issue_id = ?, workspace_id = ?
WHERE id = ? AND executed = 0
RETURNING *;

-- name: SetIntakeDecisionOutcome :one
-- Ignoring (or otherwise re-labeling) a pending decision with its reason.
UPDATE intake_decisions
SET decision = ?, reason_code = ?, reason_text = ?
WHERE id = ?
RETURNING *;

-- name: CountExecutedIntakeDecisionsSince :one
-- Daily-cap accounting: executed decisions for one project+source since a
-- caller-supplied timestamp.
SELECT COUNT(*) AS n
FROM intake_decisions d
JOIN intake_events e ON e.id = d.event_id
WHERE d.project_id = ? AND e.source_kind = ? AND d.executed = 1
  AND d.created_at >= ?;
