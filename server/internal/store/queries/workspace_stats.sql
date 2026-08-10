-- name: UpsertWorkspaceStatsAI :exec
-- Accumulate one AI ("done") interaction. owner_* set on first insert only.
INSERT INTO workspace_stats (
    workspace_id, owner_type, owner_id,
    ai_message_count, interaction_count, total_turns, total_duration_ms,
    input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
    last_activity_at, updated_at
) VALUES (?, ?, ?, 1, 1, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(workspace_id) DO UPDATE SET
    ai_message_count      = workspace_stats.ai_message_count + 1,
    interaction_count     = workspace_stats.interaction_count + 1,
    total_turns           = workspace_stats.total_turns + excluded.total_turns,
    total_duration_ms     = workspace_stats.total_duration_ms + excluded.total_duration_ms,
    input_tokens          = workspace_stats.input_tokens + excluded.input_tokens,
    output_tokens         = workspace_stats.output_tokens + excluded.output_tokens,
    cache_creation_tokens = workspace_stats.cache_creation_tokens + excluded.cache_creation_tokens,
    cache_read_tokens     = workspace_stats.cache_read_tokens + excluded.cache_read_tokens,
    last_activity_at      = CURRENT_TIMESTAMP,
    updated_at            = CURRENT_TIMESTAMP;

-- name: IncrWorkspaceStatsUserMessage :exec
INSERT INTO workspace_stats (workspace_id, owner_type, owner_id, user_message_count, last_activity_at, updated_at)
VALUES (?, ?, ?, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(workspace_id) DO UPDATE SET
    user_message_count = workspace_stats.user_message_count + 1,
    last_activity_at   = CURRENT_TIMESTAMP,
    updated_at         = CURRENT_TIMESTAMP;

-- name: GetWorkspaceStats :one
SELECT * FROM workspace_stats WHERE workspace_id = ?;

-- name: ListWorkspaceStatsForWorkspaces :many
SELECT * FROM workspace_stats
WHERE workspace_id IN (sqlc.slice('workspace_ids'));
