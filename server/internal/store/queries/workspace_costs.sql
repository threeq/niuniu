-- name: CreateWorkspaceCost :exec
INSERT INTO workspace_costs (workspace_id, session_id, message_id, cost_usd, num_turns, duration_ms, harness_run_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP);

-- name: ListWorkspaceCosts :many
SELECT * FROM workspace_costs
WHERE workspace_id = ?
ORDER BY created_at ASC;

-- name: GetWorkspaceCostSummary :one
SELECT
    COUNT(*) AS total_interactions,
    CAST(COALESCE(SUM(cost_usd), 0.0) AS REAL) AS total_cost_usd,
    CAST(COALESCE(SUM(num_turns), 0) AS INTEGER) AS total_turns,
    CAST(COALESCE(SUM(duration_ms), 0) AS INTEGER) AS total_duration_ms
FROM workspace_costs
WHERE workspace_id = ?;

