-- name: GetServerSetting :one
SELECT value FROM server_settings WHERE key = ?;

-- name: UpsertServerSetting :exec
INSERT INTO server_settings (key, value, updated_by, updated_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(key) DO UPDATE
SET value = excluded.value,
    updated_by = excluded.updated_by,
    updated_at = CURRENT_TIMESTAMP;
