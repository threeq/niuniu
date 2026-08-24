-- name: UpsertProviderTokenHourly :exec
-- Additive per-hour accumulation at subscription-platform grain. Mirrors
-- UpsertWorkspaceTokenHourly, but keyed on the provider plus the CONSUMING
-- owner (shared seeded providers have owner_id = 0, so keying on the provider
-- row's owner would collapse every user into one series).
INSERT INTO provider_token_hourly (
    provider_id, owner_type, owner_id, bucket_hour,
    input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, interaction_count
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
ON CONFLICT(provider_id, owner_type, owner_id, bucket_hour) DO UPDATE SET
    input_tokens          = provider_token_hourly.input_tokens + excluded.input_tokens,
    output_tokens         = provider_token_hourly.output_tokens + excluded.output_tokens,
    cache_creation_tokens = provider_token_hourly.cache_creation_tokens + excluded.cache_creation_tokens,
    cache_read_tokens     = provider_token_hourly.cache_read_tokens + excluded.cache_read_tokens,
    interaction_count     = provider_token_hourly.interaction_count + 1;

-- name: ListProviderTokenHourly :many
-- Hourly series for ONE platform in [from, to), for the drill-down chart.
SELECT bucket_hour, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, interaction_count
FROM provider_token_hourly
WHERE provider_id = ? AND owner_type = ? AND owner_id = ?
  AND bucket_hour >= ? AND bucket_hour < ?
ORDER BY bucket_hour;

-- name: ListOwnerProviderTokenHourly :many
-- Hourly series broken down BY platform for one owner: the main chart's data,
-- one row per (hour, provider). provider_name is joined in so the UI does not
-- need a second round trip to label the series.
SELECT
    h.bucket_hour,
    h.provider_id,
    p.name     AS provider_name,
    p.platform AS provider_platform,
    CAST(COALESCE(SUM(h.input_tokens),0)          AS BIGINT) AS input_tokens,
    CAST(COALESCE(SUM(h.output_tokens),0)         AS BIGINT) AS output_tokens,
    CAST(COALESCE(SUM(h.cache_creation_tokens),0) AS BIGINT) AS cache_creation_tokens,
    CAST(COALESCE(SUM(h.cache_read_tokens),0)     AS BIGINT) AS cache_read_tokens,
    CAST(COALESCE(SUM(h.interaction_count),0)     AS BIGINT) AS interaction_count
FROM provider_token_hourly h
JOIN env_providers p ON p.id = h.provider_id
WHERE h.owner_type = ? AND h.owner_id = ?
  AND h.bucket_hour >= ? AND h.bucket_hour < ?
GROUP BY h.bucket_hour, h.provider_id, p.name, p.platform
ORDER BY h.bucket_hour;

-- name: SumOwnerProviderTokens :many
-- Per-platform TOTALS over the window (no hour dimension): drives the summary
-- table so the UI does not have to re-sum the hourly series client-side.
SELECT
    h.provider_id,
    p.name     AS provider_name,
    p.platform AS provider_platform,
    CAST(COALESCE(SUM(h.input_tokens),0)          AS BIGINT) AS input_tokens,
    CAST(COALESCE(SUM(h.output_tokens),0)         AS BIGINT) AS output_tokens,
    CAST(COALESCE(SUM(h.cache_creation_tokens),0) AS BIGINT) AS cache_creation_tokens,
    CAST(COALESCE(SUM(h.cache_read_tokens),0)     AS BIGINT) AS cache_read_tokens,
    CAST(COALESCE(SUM(h.interaction_count),0)     AS BIGINT) AS interaction_count
FROM provider_token_hourly h
JOIN env_providers p ON p.id = h.provider_id
WHERE h.owner_type = ? AND h.owner_id = ?
  AND h.bucket_hour >= ? AND h.bucket_hour < ?
GROUP BY h.provider_id, p.name, p.platform
ORDER BY SUM(h.input_tokens + h.output_tokens) DESC;

-- name: PruneProviderTokenHourly :exec
DELETE FROM provider_token_hourly WHERE bucket_hour < ?;

-- name: CreateProviderRateLimitEvent :one
-- Opens a rate-limit episode (resumed_at stays NULL until the provider is
-- actually picked for a spawn again).
INSERT INTO provider_rate_limit_events (
    provider_id, workspace_id, owner_type, owner_id, triggered_at, reset_at, detail
) VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetOpenProviderRateLimitEvent :one
-- The provider's most recent still-open episode (never resumed). Used to avoid
-- opening a duplicate row when a platform emits several 429 lines in a burst,
-- and to find the row to close on resume.
SELECT * FROM provider_rate_limit_events
WHERE provider_id = ? AND resumed_at IS NULL
ORDER BY triggered_at DESC
LIMIT 1;

-- name: ExtendProviderRateLimitEvent :exec
-- Pushes an open episode's reset_at later. A platform can re-report a 429 with
-- a further-out reset while the provider is still sidelined; that is the SAME
-- episode with a longer quota window, so the row is extended instead of a
-- second row being opened (which would double-count the throttle tally).
UPDATE provider_rate_limit_events
SET reset_at = ?, detail = ?
WHERE id = ?;

-- name: ResumeProviderRateLimitEvents :exec
-- Closes every open episode for a provider: the provider is back in service,
-- so resumed_at is the real "unblocked and used again" timestamp. Plural
-- because a burst that raced past the dedup guard can leave more than one row
-- open; closing all of them keeps "open episode" meaning "currently limited".
UPDATE provider_rate_limit_events
SET resumed_at = ?, cleared_manually = ?
WHERE provider_id = ? AND resumed_at IS NULL;

-- name: ListOwnerProviderRateLimitEvents :many
-- Event log for the owner's platforms, newest first, for the UI table.
SELECT
    e.id,
    e.provider_id,
    p.name     AS provider_name,
    p.platform AS provider_platform,
    e.workspace_id,
    e.triggered_at,
    e.reset_at,
    e.resumed_at,
    e.cleared_manually,
    e.detail
FROM provider_rate_limit_events e
JOIN env_providers p ON p.id = e.provider_id
WHERE e.owner_type = ? AND e.owner_id = ?
  AND e.triggered_at >= ? AND e.triggered_at < ?
ORDER BY e.triggered_at DESC
LIMIT ?;

-- name: ListOwnerProviderRateLimitSpans :many
-- Raw (provider, triggered_at, resumed_at) spans over the window, unlimited, so
-- the service can aggregate per-platform throttle counts and blocked duration.
-- Deliberately NOT computed in SQL: date arithmetic is dialect-specific
-- (strftime vs EXTRACT) and this store targets both SQLite and PostgreSQL.
-- An open span (resumed_at NULL) counts toward the tally but contributes no
-- duration, so "blocked so far" is honest rather than an invented end time.
SELECT provider_id, triggered_at, resumed_at
FROM provider_rate_limit_events
WHERE owner_type = ? AND owner_id = ?
  AND triggered_at >= ? AND triggered_at < ?;

-- name: PruneProviderRateLimitEvents :exec
DELETE FROM provider_rate_limit_events WHERE triggered_at < ?;
