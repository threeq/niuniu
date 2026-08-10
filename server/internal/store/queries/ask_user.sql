-- name: InsertAskUserRequest :one
INSERT INTO agent_ask_user_requests
    (workspace_id, owner_type, owner_id, session_id, questions_json, status, requested_at, expires_at)
VALUES
    (?, ?, ?, ?, ?, 'pending', CURRENT_TIMESTAMP, ?)
RETURNING id;

-- name: GetAskUserRequest :one
SELECT * FROM agent_ask_user_requests WHERE id = ?;

-- name: ListPendingAskUserRequestsByWorkspace :many
SELECT * FROM agent_ask_user_requests
 WHERE workspace_id = ? AND status = 'pending'
 ORDER BY requested_at ASC;

-- name: MarkAskUserRequestAnswered :execrows
UPDATE agent_ask_user_requests
   SET status = 'answered',
       decision_source = ?,
       decided_by = ?,
       answers_json = ?,
       decided_at = CURRENT_TIMESTAMP
 WHERE id = ? AND status = 'pending';

-- name: MarkAskUserRequestTimeout :execrows
UPDATE agent_ask_user_requests
   SET status = 'timeout',
       decision_source = 'timeout',
       decided_at = CURRENT_TIMESTAMP
 WHERE id = ? AND status = 'pending';

-- name: MarkAskUserRequestCancelled :execrows
UPDATE agent_ask_user_requests
   SET status = 'cancelled',
       decision_source = ?,
       decided_at = CURRENT_TIMESTAMP
 WHERE id = ? AND status = 'pending';

-- name: CancelPendingAskUserRequestsByWorkspace :many
UPDATE agent_ask_user_requests
   SET status = 'cancelled',
       decision_source = ?,
       decided_at = CURRENT_TIMESTAMP
 WHERE workspace_id = ? AND status = 'pending'
 RETURNING id, owner_type, owner_id;

-- name: CancelAllPendingAskUserRequests :many
UPDATE agent_ask_user_requests
   SET status = 'cancelled',
       decision_source = 'session_end',
       decided_at = CURRENT_TIMESTAMP
 WHERE status = 'pending'
 RETURNING id, workspace_id;
