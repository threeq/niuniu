-- name: AppendOrgAudit :exec
INSERT INTO org_audit_log (org_id, actor_user_id, actor_label, action, target_type, target_id, payload)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListOrgAudit :many
SELECT * FROM org_audit_log
WHERE org_id = ?
ORDER BY created_at DESC
LIMIT ? OFFSET ?;

-- name: ListOrgApprovalAudit :many
-- Approval decisions for one org (#743), read from the org audit trail that
-- #741 writes with target_type = 'integration_approval'. This -- not the
-- integration_approval_requests queue -- is the tamper-proof record of "who
-- approved what, when": org_audit_log is append-only, while the queue's status
-- legitimately changes as requests are decided, escalated or invalidated.
-- member_id filters on the ACTOR (approver / canceler / escalator); 0 = any.
-- outcome maps the decision: 'ok' = approved, 'error' = denied/cancelled/
-- invalidated, 'pending' = escalated and still awaiting a decision.
-- project_id is resolved through the request the decision was made on: the
-- audit row records target_id, and the request carries the project (nullable
-- for org-level requests, which a project filter therefore excludes).
SELECT o.id, o.org_id, o.actor_user_id, o.actor_label, o.action, o.target_type,
       o.target_id, o.payload, o.created_at
FROM org_audit_log o
LEFT JOIN integration_approval_requests r ON r.id = o.target_id
WHERE o.org_id = sqlc.arg(org_id)
  AND o.target_type = 'integration_approval'
  AND (CAST(sqlc.arg(member_id) AS BIGINT) = 0 OR o.actor_user_id = sqlc.arg(member_id))
  AND (CAST(sqlc.arg(project_id) AS BIGINT) = 0 OR r.project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(outcome) AS TEXT) = ''
       OR (sqlc.arg(outcome) = 'ok' AND o.action = 'integration.approval.approved')
       OR (sqlc.arg(outcome) = 'error'
           AND o.action IN ('integration.approval.denied',
                            'integration.approval.cancelled',
                            'integration.approval.invalidated'))
       OR (sqlc.arg(outcome) = 'pending' AND o.action = 'integration.approval.escalated'))
  AND o.created_at >= COALESCE(sqlc.narg(from_ts), o.created_at)
  AND o.created_at <= COALESCE(sqlc.narg(to_ts), o.created_at)
ORDER BY o.created_at DESC, o.id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CountOrgApprovalAudit :one
SELECT COUNT(*) AS n
FROM org_audit_log o
LEFT JOIN integration_approval_requests r ON r.id = o.target_id
WHERE o.org_id = sqlc.arg(org_id)
  AND o.target_type = 'integration_approval'
  AND (CAST(sqlc.arg(member_id) AS BIGINT) = 0 OR o.actor_user_id = sqlc.arg(member_id))
  AND (CAST(sqlc.arg(project_id) AS BIGINT) = 0 OR r.project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(outcome) AS TEXT) = ''
       OR (sqlc.arg(outcome) = 'ok' AND o.action = 'integration.approval.approved')
       OR (sqlc.arg(outcome) = 'error'
           AND o.action IN ('integration.approval.denied',
                            'integration.approval.cancelled',
                            'integration.approval.invalidated'))
       OR (sqlc.arg(outcome) = 'pending' AND o.action = 'integration.approval.escalated'))
  AND o.created_at >= COALESCE(sqlc.narg(from_ts), o.created_at)
  AND o.created_at <= COALESCE(sqlc.narg(to_ts), o.created_at);
