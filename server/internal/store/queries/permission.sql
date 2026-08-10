-- name: InsertPermissionRequest :one
INSERT INTO agent_permission_requests
    (workspace_id, owner_type, owner_id, session_id, tool_name, tool_input, status, requested_at, expires_at)
VALUES
    (?, ?, ?, ?, ?, ?, 'pending', CURRENT_TIMESTAMP, ?)
RETURNING id;

-- name: GetPermissionRequest :one
SELECT * FROM agent_permission_requests WHERE id = ?;

-- name: ListPendingPermissionRequestsByWorkspace :many
SELECT * FROM agent_permission_requests
 WHERE workspace_id = ? AND status = 'pending'
 ORDER BY requested_at ASC;

-- name: MarkPermissionRequestAllowed :execrows
UPDATE agent_permission_requests
   SET status = 'allowed',
       decision_source = ?,
       decided_by = ?,
       matcher_used = ?,
       decided_at = CURRENT_TIMESTAMP
 WHERE id = ? AND status = 'pending';

-- name: MarkPermissionRequestDenied :execrows
UPDATE agent_permission_requests
   SET status = 'denied',
       decision_source = ?,
       decided_by = ?,
       deny_message = ?,
       decided_at = CURRENT_TIMESTAMP
 WHERE id = ? AND status = 'pending';

-- name: MarkPermissionRequestTimeout :execrows
UPDATE agent_permission_requests
   SET status = 'timeout',
       decision_source = 'timeout',
       decided_at = CURRENT_TIMESTAMP
 WHERE id = ? AND status = 'pending';

-- name: MarkPermissionRequestCancelled :execrows
UPDATE agent_permission_requests
   SET status = 'cancelled',
       decision_source = ?,
       decided_at = CURRENT_TIMESTAMP
 WHERE id = ? AND status = 'pending';

-- name: CancelPendingPermissionRequestsByWorkspace :many
UPDATE agent_permission_requests
   SET status = 'cancelled',
       decision_source = ?,
       decided_at = CURRENT_TIMESTAMP
 WHERE workspace_id = ? AND status = 'pending'
 RETURNING id, owner_type, owner_id;

-- name: CancelAllPendingPermissionRequests :many
UPDATE agent_permission_requests
   SET status = 'cancelled',
       decision_source = 'session_end',
       decided_at = CURRENT_TIMESTAMP
 WHERE status = 'pending'
 RETURNING id, workspace_id;

-- name: ListPermissionAllowlistByWorkspace :many
SELECT * FROM agent_permission_allowlist
 WHERE workspace_id = ?
 ORDER BY tool_name ASC, created_at DESC;

-- name: ListPermissionAllowlistByWorkspaceAndTool :many
SELECT * FROM agent_permission_allowlist
 WHERE workspace_id = ? AND tool_name = ?;

-- name: InsertPermissionAllowlist :exec
INSERT INTO agent_permission_allowlist
    (workspace_id, tool_name, matcher_kind, matcher_value, created_by)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (workspace_id, tool_name, matcher_kind, matcher_value) DO NOTHING;

-- name: DeletePermissionAllowlist :execrows
DELETE FROM agent_permission_allowlist WHERE id = ?;

-- name: GetPermissionAllowlistEntryWorkspaceID :one
SELECT workspace_id FROM agent_permission_allowlist WHERE id = ?;

-- name: InsertPermissionRequestAllowed :one
-- Single-write path used by allowlist hits - atomically inserts a row in
-- the terminal 'allowed' state. Avoids the two-write race in recordAllowlistHit.
INSERT INTO agent_permission_requests
    (workspace_id, owner_type, owner_id, session_id, tool_name, tool_input, status, decision_source, matcher_used, requested_at, decided_at, expires_at)
VALUES
    (?, ?, ?, ?, ?, ?, 'allowed', 'allowlist', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?)
RETURNING id;
