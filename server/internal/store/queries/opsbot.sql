-- Queries for the Ops Bot data model (design 2026-11-06 section 11).
-- Keep every comment ASCII-only: sqlc v1.31 silently truncates the next
-- query when a multi-byte character appears in a `--` comment.

-- name: CreateOpsBot :one
INSERT INTO ops_bots (
    owner_type, owner_id, role, pack_id, pack_version, account_ref, display_name,
    persona_json, credential_refs_json, knowledge_scope_json, goals_json,
    cadence_json, approval_policy_json, budget_json, director_bot_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetOpsBot :one
SELECT * FROM ops_bots WHERE id = ?;

-- name: ListOpsBotsByOwner :many
SELECT * FROM ops_bots WHERE owner_type = ? AND owner_id = ?
ORDER BY role DESC, id ASC;

-- name: GetOpsBotByAccount :one
SELECT * FROM ops_bots
WHERE owner_type = ? AND owner_id = ? AND pack_id = ? AND account_ref = ?;

-- name: UpdateOpsBotStatus :one
UPDATE ops_bots SET status = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: UpdateOpsBotConfig :one
UPDATE ops_bots SET
    display_name = ?, persona_json = ?, credential_refs_json = ?,
    knowledge_scope_json = ?, goals_json = ?, cadence_json = ?,
    approval_policy_json = ?, budget_json = ?, pack_version = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: SetOpsBotProvision :one
UPDATE ops_bots SET epic_issue_id = ?, control_workspace_id = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: CreateOpsPublishRecord :one
INSERT INTO ops_publish_records (
    issue_id, bot_id, pack_id, account_ref, content_version, idempotency_key, status
) VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: GetOpsPublishRecord :one
SELECT * FROM ops_publish_records WHERE bot_id = ? AND idempotency_key = ?;

-- name: UpdateOpsPublishRecord :one
UPDATE ops_publish_records SET
    status = ?, receipt = ?, url = ?, screenshot_path = ?,
    published_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: UpsertOpsMetricsDaily :one
INSERT INTO ops_metrics_daily (bot_id, date, metrics_json, source)
VALUES (?, ?, ?, ?)
ON CONFLICT(bot_id, date) DO UPDATE SET
    metrics_json = excluded.metrics_json, source = excluded.source
RETURNING *;

-- name: ListOpsMetricsDaily :many
SELECT * FROM ops_metrics_daily
WHERE bot_id = ? AND date >= ? AND date <= ? ORDER BY date ASC;

-- name: UpsertOpsEngagement :one
INSERT INTO ops_engagements (
    bot_id, issue_id, platform_engagement_id, kind, author, text
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(bot_id, platform_engagement_id) DO UPDATE SET
    text = excluded.text, updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: GetOpsEngagement :one
SELECT * FROM ops_engagements WHERE id = ?;

-- name: ListOpsEngagementsByStatus :many
SELECT * FROM ops_engagements
WHERE bot_id = ? AND status = ? ORDER BY created_at ASC LIMIT ?;

-- name: ListOpsEngagementsByStatusGlobal :many
SELECT * FROM ops_engagements
WHERE status = ? ORDER BY updated_at ASC LIMIT ?;

-- name: UpdateOpsEngagementStatus :one
UPDATE ops_engagements SET
    status = ?, category = ?, risk_level = ?, suggested_reply = ?,
    approval_request_id = ?, reply_text = ?, receipt = ?,
    decided_by = ?, decided_at = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = ? RETURNING *;

-- name: CountOpsEngagementsByBot :one
SELECT COUNT(*) FROM ops_engagements
WHERE bot_id = ? AND created_at >= ? AND created_at < ?;

-- name: UpsertOpsBrowserSession :one
INSERT INTO ops_browser_sessions (bot_id, backend, profile_dir)
VALUES (?, ?, ?)
ON CONFLICT(bot_id, backend) DO UPDATE SET
    profile_dir = excluded.profile_dir, updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: GetOpsBrowserSession :one
SELECT * FROM ops_browser_sessions WHERE bot_id = ? AND backend = ?;

-- name: AcquireOpsBrowserLease :execrows
-- Cutoff is a caller-supplied Go time, not CURRENT_TIMESTAMP: the SQLite
-- driver writes time.Time in local formatting, so a UTC server comparison
-- would never reclaim leases outside UTC.
UPDATE ops_browser_sessions SET
    lease_owner = ?, lease_expires_at = ?, updated_at = CURRENT_TIMESTAMP
WHERE bot_id = ? AND backend = ?
  AND (lease_owner = '' OR lease_expires_at IS NULL OR lease_expires_at < sqlc.arg(now));

-- name: ReleaseOpsBrowserLease :execrows
UPDATE ops_browser_sessions SET
    lease_owner = '', lease_expires_at = NULL, updated_at = CURRENT_TIMESTAMP
WHERE bot_id = ? AND backend = ? AND lease_owner = ?;

-- name: UpdateOpsBrowserSessionState :one
UPDATE ops_browser_sessions SET
    login_status = ?, last_login_at = ?, health = ?, risk_state = ?,
    last_error = ?, updated_at = CURRENT_TIMESTAMP
WHERE bot_id = ? AND backend = ? RETURNING *;

-- name: SumOpsAccountCost :one
SELECT CAST(COALESCE(SUM(e.cost_usd), 0) AS REAL)
FROM issue_exec_events e
JOIN issues i ON i.id = e.issue_id
WHERE i.parent_issue_id = ? AND e.created_at >= ? AND e.created_at < ?;
