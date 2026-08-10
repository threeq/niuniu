-- name: InsertIssueExecEvent :exec
-- Append one execution-timeline event for an issue (spec section 23.7).
INSERT INTO issue_exec_events (issue_id, workspace_id, kind, summary, detail_json, cost_usd)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListIssueExecEvents :many
-- Full execution timeline for an issue, oldest first. issue_id anchors the param.
SELECT id, issue_id, workspace_id, kind, summary, detail_json, cost_usd, created_at
FROM issue_exec_events
WHERE issue_id = ?
ORDER BY created_at ASC, id ASC;

-- name: SumIssueExecCost :one
-- Cumulative recorded cost for an issue (cost chip on the card). CAST keeps the
-- result a concrete float on both SQLite (REAL affinity) and Postgres.
SELECT CAST(COALESCE(SUM(cost_usd), 0) AS REAL) FROM issue_exec_events WHERE issue_id = ?;
