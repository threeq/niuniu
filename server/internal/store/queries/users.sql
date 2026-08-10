-- name: CreateUser :one
INSERT INTO users (username, password_hash, display_name, role)
VALUES (?, ?, ?, ?)
RETURNING id, username, password_hash, display_name, email, role,
          locked_until, lockout_count, require_password_change, password_changed_at,
          mfa_enabled,
          created_at, updated_at;

-- name: GetUserByUsername :one
SELECT id, username, password_hash, display_name, email, role,
       locked_until, lockout_count, require_password_change, password_changed_at,
       mfa_enabled,
       created_at, updated_at
FROM users WHERE username = ?;

-- name: GetUserByID :one
SELECT id, username, password_hash, display_name, email, role,
       locked_until, lockout_count, require_password_change, password_changed_at,
       mfa_enabled,
       created_at, updated_at
FROM users WHERE id = ?;

-- name: ListUsers :many
SELECT id, username, display_name, role, created_at, updated_at
FROM users ORDER BY created_at;

-- name: UpdateUser :exec
UPDATE users SET display_name = ?, role = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = ?;

-- name: CountUsers :one
SELECT COUNT(*) FROM users;

-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (user_id, token_hash, expires_at)
VALUES (?, ?, ?)
RETURNING id, user_id, token_hash, expires_at, created_at;

-- name: GetRefreshToken :one
SELECT id, user_id, token_hash, expires_at, created_at
FROM refresh_tokens WHERE token_hash = ?;

-- name: DeleteRefreshToken :exec
DELETE FROM refresh_tokens WHERE token_hash = ?;

-- name: DeleteUserRefreshTokens :exec
DELETE FROM refresh_tokens WHERE user_id = ?;

-- name: DeleteExpiredRefreshTokens :exec
DELETE FROM refresh_tokens WHERE expires_at < CURRENT_TIMESTAMP;

-- name: SearchUsersForOrg :many
SELECT u.id, u.username, u.display_name,
       COUNT(*) OVER () AS total,
       CASE WHEN LOWER(u.username) LIKE LOWER(?) THEN 0 ELSE 1 END AS prefix_priority
FROM users u
LEFT JOIN org_members om
  ON om.user_id = u.id AND om.org_id = ?
WHERE om.user_id IS NULL
  AND LOWER(u.username || ' ' || u.display_name) LIKE LOWER(?)
ORDER BY
  prefix_priority ASC,
  u.username ASC
LIMIT 20;
