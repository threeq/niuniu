-- name: GetEmailPollState :one
SELECT * FROM email_source_poll_state WHERE binding_id = ? AND mailbox = ?;

-- name: UpsertEmailPollState :one
INSERT INTO email_source_poll_state (project_id, binding_id, mailbox, uid_validity, last_uid)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(binding_id, mailbox) DO UPDATE SET
    uid_validity = excluded.uid_validity,
    last_uid = excluded.last_uid,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: UpdateEmailPollCursor :execrows
UPDATE email_source_poll_state SET
    uid_validity = ?, last_uid = ?, backoff_until = NULL,
    last_error = '', updated_at = CURRENT_TIMESTAMP
WHERE binding_id = ? AND mailbox = ?;

-- name: SetEmailPollBackoff :execrows
UPDATE email_source_poll_state SET
    backoff_until = ?, last_error = ?, updated_at = CURRENT_TIMESTAMP
WHERE binding_id = ? AND mailbox = ?;

-- name: ListEmailPollStatesByProject :many
SELECT * FROM email_source_poll_state WHERE project_id = ? ORDER BY binding_id, mailbox;
