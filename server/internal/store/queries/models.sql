-- Model library, usage aggregation and budget queries.
-- See docs/superpowers/specs/2026-11-19-shared-model-library-design.md.

-- name: ListModelsForOwner :many
SELECT * FROM models
WHERE (owner_type = 'user' AND owner_id = 0)
   OR (owner_type = ? AND owner_id = ?)
ORDER BY name ASC;

-- name: GetModel :one
SELECT * FROM models WHERE id = ?;

-- name: GetModelByName :one
SELECT * FROM models WHERE owner_type = ? AND owner_id = ? AND name = ?;

-- name: CreateModel :one
INSERT INTO models (owner_type, owner_id, name, display_name, enabled, source)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateModel :one
UPDATE models SET display_name = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: DeleteModel :execrows
DELETE FROM models WHERE id = ? AND owner_type = ? AND owner_id = ?;

-- name: UpsertModelUsageHourly :exec
INSERT INTO model_usage_hourly (
    owner_type, owner_id, project_id, workspace_id, agent_cli,
    model_name, provider_id, bucket_hour,
    input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
    interaction_count, estimated
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(owner_type, owner_id, project_id, workspace_id, agent_cli, model_name, provider_id, bucket_hour)
DO UPDATE SET
    input_tokens = model_usage_hourly.input_tokens + excluded.input_tokens,
    output_tokens = model_usage_hourly.output_tokens + excluded.output_tokens,
    cache_creation_tokens = model_usage_hourly.cache_creation_tokens + excluded.cache_creation_tokens,
    cache_read_tokens = model_usage_hourly.cache_read_tokens + excluded.cache_read_tokens,
    interaction_count = model_usage_hourly.interaction_count + excluded.interaction_count,
    estimated = CASE WHEN excluded.estimated = 1 THEN 1 ELSE model_usage_hourly.estimated END;

-- name: SumModelUsageByOwner :one
SELECT CAST(COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS BIGINT)
FROM model_usage_hourly
WHERE owner_type = ? AND owner_id = ? AND bucket_hour >= ? AND bucket_hour < ?;

-- name: SumModelUsageByProject :one
SELECT CAST(COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS BIGINT)
FROM model_usage_hourly
WHERE project_id = ? AND bucket_hour >= ? AND bucket_hour < ?;

-- name: ListModelUsageByModel :many
SELECT model_name,
       CAST(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) AS BIGINT) AS total_tokens,
       CAST(SUM(interaction_count) AS INTEGER) AS requests
FROM model_usage_hourly
WHERE owner_type = ? AND owner_id = ? AND bucket_hour >= ? AND bucket_hour < ?
GROUP BY model_name ORDER BY total_tokens DESC;

-- name: ListModelUsageByProvider :many
SELECT provider_id,
       CAST(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) AS BIGINT) AS total_tokens,
       CAST(SUM(interaction_count) AS INTEGER) AS requests
FROM model_usage_hourly
WHERE owner_type = ? AND owner_id = ? AND bucket_hour >= ? AND bucket_hour < ?
GROUP BY provider_id ORDER BY total_tokens DESC;

-- name: ListModelUsageByProject :many
SELECT project_id,
       CAST(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) AS BIGINT) AS total_tokens,
       CAST(SUM(interaction_count) AS INTEGER) AS requests
FROM model_usage_hourly
WHERE owner_type = ? AND owner_id = ? AND bucket_hour >= ? AND bucket_hour < ?
GROUP BY project_id ORDER BY total_tokens DESC;

-- name: ListModelUsageByWorkspace :many
SELECT workspace_id,
       CAST(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) AS BIGINT) AS total_tokens,
       CAST(SUM(interaction_count) AS INTEGER) AS requests
FROM model_usage_hourly
WHERE owner_type = ? AND owner_id = ? AND bucket_hour >= ? AND bucket_hour < ?
GROUP BY workspace_id ORDER BY total_tokens DESC;

-- name: ListModelUsageByCLI :many
SELECT agent_cli,
       CAST(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) AS BIGINT) AS total_tokens,
       CAST(SUM(interaction_count) AS INTEGER) AS requests
FROM model_usage_hourly
WHERE owner_type = ? AND owner_id = ? AND bucket_hour >= ? AND bucket_hour < ?
GROUP BY agent_cli ORDER BY total_tokens DESC;

-- name: GetModelBudget :one
SELECT * FROM model_budget_settings
WHERE owner_type = ? AND owner_id = ? AND project_id = ?;

-- name: UpsertModelBudget :one
INSERT INTO model_budget_settings (owner_type, owner_id, project_id, monthly_tokens, enabled)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(owner_type, owner_id, project_id) DO UPDATE SET
    monthly_tokens = excluded.monthly_tokens,
    enabled = excluded.enabled,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: DeleteModelBudget :execrows
DELETE FROM model_budget_settings
WHERE owner_type = ? AND owner_id = ? AND project_id = ?;

-- name: ListModelBudgetsForOwner :many
SELECT * FROM model_budget_settings
WHERE owner_type = ? AND owner_id = ? ORDER BY project_id ASC;
