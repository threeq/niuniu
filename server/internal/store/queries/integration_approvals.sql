-- name: CreateIntegrationApproval :one
INSERT INTO integration_approval_requests (
    owner_type, owner_id, project_id, action, subject_type, subject_id,
    subject_label, diff, summary, requested_by, required_role, status, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?)
RETURNING *;

-- name: GetIntegrationApproval :one
SELECT * FROM integration_approval_requests WHERE id = ?;

-- name: ListIntegrationApprovalsByOwner :many
SELECT * FROM integration_approval_requests
WHERE owner_type = ? AND owner_id = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: ListIntegrationApprovalsByOwnerStatus :many
SELECT * FROM integration_approval_requests
WHERE owner_type = ? AND owner_id = ? AND status = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: ListPendingIntegrationApprovalsByOwner :many
SELECT * FROM integration_approval_requests
WHERE owner_type = 'org' AND owner_id = ? AND status = 'pending'
ORDER BY created_at ASC;

-- name: ListPendingIntegrationApprovalsByRequester :many
SELECT * FROM integration_approval_requests
WHERE requested_by = ? AND status = 'pending'
ORDER BY created_at DESC;

-- name: ListExpiredPendingIntegrationApprovals :many
SELECT * FROM integration_approval_requests
WHERE status = 'pending' AND expires_at < ?
ORDER BY expires_at ASC
LIMIT ?;

-- name: DecideIntegrationApproval :execrows
UPDATE integration_approval_requests
SET status = ?, decided_by = ?, decided_at = CURRENT_TIMESTAMP,
    decision_note = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'pending';

-- name: InvalidateIntegrationApprovalsByRequesterInOwner :execrows
UPDATE integration_approval_requests
SET status = 'invalidated', updated_at = CURRENT_TIMESTAMP
WHERE owner_type = 'org' AND owner_id = ? AND requested_by = ? AND status = 'pending';

-- name: EscalateIntegrationApproval :execrows
UPDATE integration_approval_requests
SET required_role = ?, escalation_count = escalation_count + 1,
    escalated_at = CURRENT_TIMESTAMP, expires_at = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'pending';

-- name: CancelIntegrationApproval :execrows
UPDATE integration_approval_requests
SET status = 'cancelled', updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = 'pending';
