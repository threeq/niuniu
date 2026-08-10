-- name: RecordLoginAttempt :exec
INSERT INTO login_attempts (user_id, username, ip, user_agent, success, reason, created_at)
VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP);

-- name: CountFailedLoginsByUser :one
SELECT COUNT(*) FROM login_attempts
WHERE user_id = ?
  AND success = 0
  AND created_at >= ?;

-- name: CountFailedLoginsByIP :one
SELECT COUNT(*) FROM login_attempts
WHERE ip = ?
  AND success = 0
  AND created_at >= ?;

-- name: ListLoginAttemptsForUser :many
SELECT id, user_id, username, ip, user_agent, success, reason, created_at
FROM login_attempts
WHERE user_id = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: LockUserAccount :exec
UPDATE users
SET locked_until = ?,
    lockout_count = lockout_count + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: UnlockUserAccount :exec
UPDATE users
SET locked_until = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: GetIPLockout :one
SELECT ip, fail_count, locked_until, updated_at
FROM ip_lockouts
WHERE ip = ?;

-- name: UpsertIPLockout :exec
INSERT INTO ip_lockouts (ip, fail_count, locked_until, updated_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT (ip) DO UPDATE
SET fail_count = EXCLUDED.fail_count,
    locked_until = EXCLUDED.locked_until,
    updated_at = CURRENT_TIMESTAMP;

-- name: ResetIPLockoutFailCount :exec
UPDATE ip_lockouts
SET fail_count = 0,
    locked_until = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE ip = ?;

-- name: UpdateUserPasswordWithTimestamp :exec
UPDATE users
SET password_hash = ?,
    password_changed_at = CURRENT_TIMESTAMP,
    require_password_change = 0,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: SetRequirePasswordChange :exec
UPDATE users
SET require_password_change = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteOldLoginAttempts :exec
DELETE FROM login_attempts WHERE created_at < ?;
