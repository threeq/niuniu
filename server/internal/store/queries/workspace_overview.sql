-- name: AggregateWorkspaceCosts :many
-- Per-workspace cost roll-up. The two placeholder timestamps gate the
-- "today" and "week" CASE columns respectively. Use plain `?` only;
-- sqlc.arg(name) emits `?N` which ConvertPlaceholders mis-rewrites on
-- Postgres. See store/dbwrap.go for the rewriter.
SELECT
    workspace_id,
    COALESCE(SUM(cost_usd), 0)                                              AS total_cost,
    COALESCE(SUM(CASE WHEN created_at >= ? THEN cost_usd ELSE 0 END), 0)    AS today_cost,
    COALESCE(SUM(CASE WHEN created_at >= ? THEN cost_usd ELSE 0 END), 0)    AS week_cost,
    MAX(created_at)                                                         AS last_cost_at
FROM workspace_costs
GROUP BY workspace_id;

-- name: AggregateWorkspaceMessages :many
-- Per-workspace message count + last-message timestamp.
SELECT
    workspace_id,
    COUNT(*)         AS message_count,
    MAX(created_at)  AS last_message_at
FROM agent_messages
GROUP BY workspace_id;

-- name: AggregateWorkspaceMessagesForWorkspaces :many
-- Message count + last timestamp for a caller-scoped workspace set.
SELECT
    workspace_id,
    COUNT(*)         AS message_count,
    MAX(created_at)  AS last_message_at
FROM agent_messages
WHERE workspace_id IN (sqlc.slice('workspace_ids'))
GROUP BY workspace_id;
