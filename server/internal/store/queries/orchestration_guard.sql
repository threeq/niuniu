-- name: CountActiveWorkspacesForOwner :one
-- Count an owner's CONCURRENTLY-ACTIVE workspaces - the ones actually consuming
-- agent capacity. A workspace holds a slot while it is being created, while its
-- status is 'running', or whenever its agent process is alive (agent_status
-- 'running'). A finished workspace (needs_review / attention with an idle
-- agent) frees its slot: it consumes no compute, and counting it deadlocked
-- the start queue forever for an owner whose agents had all finished - every
-- new start_workspace queued, nothing ever completed, so nothing drained.
-- Completed and archived workspaces never count. Uses workspace.status (not the
-- linked issue's exec_status) because start_workspace-dispatched children do not
-- flip exec_status to 'running' on the create hook.
SELECT COUNT(*)
FROM workspaces
WHERE owner_type = ? AND owner_id = ? AND is_archived = 0 AND status <> 'completed'
  AND (status IN ('created', 'running') OR agent_status = 'running');

-- name: SumEpicSubtreeCostUSD :one
-- Cumulative cost of one orchestration tree: the epic issue's workspace plus all
-- of its child issues' workspaces. Both '?' anchor the typed issues.id /
-- issues.parent_issue_id columns so PostgreSQL can infer the parameter type.
SELECT CAST(COALESCE(SUM(wc.cost_usd), 0.0) AS REAL) AS total_cost_usd
FROM workspace_costs wc
WHERE wc.workspace_id IN (
    SELECT w.id FROM workspaces w
    JOIN issues i ON w.issue_id = i.id
    WHERE i.id = ? OR i.parent_issue_id = ?
);
