-- name: GetIntakeSetting :one
SELECT * FROM intake_settings WHERE project_id = ? AND source_kind = ?;

-- name: ListIntakeSettingsByProject :many
SELECT * FROM intake_settings WHERE project_id = ? ORDER BY source_kind;

-- name: UpsertIntakeSetting :one
-- One settings row per (project, source). Writing settings is also what creates
-- a policy version, but that snapshot is taken by the caller in one transaction.
INSERT INTO intake_settings (project_id, source_kind, mode, daily_cap, allow_list_json, bot_list_json)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id, source_kind) DO UPDATE SET
    mode = excluded.mode,
    daily_cap = excluded.daily_cap,
    allow_list_json = excluded.allow_list_json,
    bot_list_json = excluded.bot_list_json,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: DeleteIntakeSetting :exec
DELETE FROM intake_settings WHERE project_id = ? AND source_kind = ?;

-- name: GetIntakeProjectState :one
SELECT * FROM intake_project_state WHERE project_id = ?;

-- name: UpsertIntakeProjectState :one
INSERT INTO intake_project_state (project_id, paused)
VALUES (?, ?)
ON CONFLICT(project_id) DO UPDATE SET
    paused = excluded.paused,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;
