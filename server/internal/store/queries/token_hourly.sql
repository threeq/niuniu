-- name: UpsertWorkspaceTokenHourly :exec
INSERT INTO workspace_token_hourly (
    workspace_id, bucket_hour,
    input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, interaction_count
) VALUES (?, ?, ?, ?, ?, ?, 1)
ON CONFLICT(workspace_id, bucket_hour) DO UPDATE SET
    input_tokens          = workspace_token_hourly.input_tokens + excluded.input_tokens,
    output_tokens         = workspace_token_hourly.output_tokens + excluded.output_tokens,
    cache_creation_tokens = workspace_token_hourly.cache_creation_tokens + excluded.cache_creation_tokens,
    cache_read_tokens     = workspace_token_hourly.cache_read_tokens + excluded.cache_read_tokens,
    interaction_count     = workspace_token_hourly.interaction_count + 1;

-- name: ListWorkspaceTokenHourly :many
SELECT bucket_hour, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, interaction_count
FROM workspace_token_hourly
WHERE workspace_id = ? AND bucket_hour >= ? AND bucket_hour < ?
ORDER BY bucket_hour;

-- name: ListOwnerTokenHourly :many
SELECT
    h.bucket_hour,
    CAST(COALESCE(SUM(h.input_tokens),0)          AS BIGINT) AS input_tokens,
    CAST(COALESCE(SUM(h.output_tokens),0)         AS BIGINT) AS output_tokens,
    CAST(COALESCE(SUM(h.cache_creation_tokens),0) AS BIGINT) AS cache_creation_tokens,
    CAST(COALESCE(SUM(h.cache_read_tokens),0)     AS BIGINT) AS cache_read_tokens,
    CAST(COALESCE(SUM(h.interaction_count),0)     AS BIGINT) AS interaction_count
FROM workspace_token_hourly h
JOIN workspaces w ON w.id = h.workspace_id
WHERE w.owner_type = ? AND w.owner_id = ?
  AND h.bucket_hour >= ? AND h.bucket_hour < ?
GROUP BY h.bucket_hour
ORDER BY h.bucket_hour;

-- name: PruneWorkspaceTokenHourly :exec
DELETE FROM workspace_token_hourly WHERE bucket_hour < ?;
