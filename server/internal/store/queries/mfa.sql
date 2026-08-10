-- name: CreateMFA :exec
INSERT INTO user_mfa (user_id, method, secret_ciphertext, created_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP);

-- name: GetMFAByUserID :one
SELECT user_id, method, secret_ciphertext, enabled_at, confirmed_at, created_at
FROM user_mfa
WHERE user_id = ?;

-- name: EnableMFA :exec
UPDATE user_mfa
SET enabled_at = CURRENT_TIMESTAMP,
    confirmed_at = CURRENT_TIMESTAMP
WHERE user_id = ?;

-- name: DeleteMFA :exec
DELETE FROM user_mfa WHERE user_id = ?;

-- name: InsertBackupCode :exec
INSERT INTO user_mfa_backup_codes (user_id, code_hash, created_at)
VALUES (?, ?, CURRENT_TIMESTAMP);

-- name: ConsumeBackupCode :exec
UPDATE user_mfa_backup_codes
SET used_at = CURRENT_TIMESTAMP
WHERE user_id = ? AND code_hash = ? AND used_at IS NULL;

-- name: CountActiveBackupCodes :one
SELECT COUNT(*) FROM user_mfa_backup_codes
WHERE user_id = ? AND used_at IS NULL;

-- name: DeleteBackupCodesByUser :exec
DELETE FROM user_mfa_backup_codes WHERE user_id = ?;

-- name: CreateTrustedDevice :exec
INSERT INTO mfa_trusted_devices (user_id, token_hash, user_agent, expires_at, created_at)
VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP);

-- name: GetTrustedDeviceByHash :one
SELECT id, user_id, token_hash, user_agent, last_seen_at, expires_at, created_at
FROM mfa_trusted_devices
WHERE token_hash = ?;

-- name: TouchTrustedDevice :exec
UPDATE mfa_trusted_devices
SET last_seen_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: ListTrustedDevicesForUser :many
SELECT id, user_id, token_hash, user_agent, last_seen_at, expires_at, created_at
FROM mfa_trusted_devices
WHERE user_id = ?
ORDER BY last_seen_at DESC;

-- name: DeleteTrustedDevice :exec
DELETE FROM mfa_trusted_devices WHERE id = ? AND user_id = ?;

-- name: DeleteTrustedDevicesByUser :exec
DELETE FROM mfa_trusted_devices WHERE user_id = ?;

-- name: SetUserMFAEnabled :exec
UPDATE users SET mfa_enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;
