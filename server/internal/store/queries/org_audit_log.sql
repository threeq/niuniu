-- name: AppendOrgAudit :exec
INSERT INTO org_audit_log (org_id, actor_user_id, actor_label, action, target_type, target_id, payload)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListOrgAudit :many
SELECT * FROM org_audit_log
WHERE org_id = ?
ORDER BY created_at DESC
LIMIT ? OFFSET ?;
