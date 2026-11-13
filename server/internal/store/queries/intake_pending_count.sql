-- name: CountPendingIntakeDecisionsForOwners :one
-- Global visibility count for the task-suggestion nav entry: pending,
-- still-actionable decisions across every project the caller can access.
SELECT COUNT(*) FROM intake_decisions d
JOIN projects p ON p.id = d.project_id
WHERE d.executed = 0
  AND d.consumed_at IS NULL
  AND d.decision IN ('accept', 'resume')
  AND ((p.owner_type = 'user' AND p.owner_id = ?)
       OR (p.owner_type = 'org' AND p.owner_id IN (sqlc.slice('org_ids'))));

-- name: CountPendingIntakeDecisions :one
-- Personal edition (auth disabled): count across all projects.
SELECT COUNT(*) FROM intake_decisions
WHERE executed = 0
  AND consumed_at IS NULL
  AND decision IN ('accept', 'resume');
