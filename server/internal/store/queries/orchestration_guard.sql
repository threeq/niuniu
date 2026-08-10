-- name: CountActiveWorkspacesForOwner :one
-- Count an owner's concurrently-active workspaces: non-archived workspaces that
-- have not yet completed. Drives the per-owner concurrent-workspace cap. A
-- completed or archived workspace frees a slot. Uses workspace.status (not the
-- linked issue's exec_status) because start_workspace-dispatched children do not
-- flip exec_status to 'running' on the create hook.
SELECT COUNT(*)
FROM workspaces
WHERE owner_type = ? AND owner_id = ? AND is_archived = 0 AND status <> 'completed';

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
