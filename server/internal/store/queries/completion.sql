-- name: GetCompletionSettings :one
SELECT * FROM completion_settings WHERE project_id = ?;

-- name: UpsertCompletionSettings :one
INSERT INTO completion_settings (
    project_id, autohost_enabled, max_rounds, token_budget, stop_on_decision, require_review
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id) DO UPDATE SET
    autohost_enabled = excluded.autohost_enabled,
    max_rounds = excluded.max_rounds,
    token_budget = excluded.token_budget,
    stop_on_decision = excluded.stop_on_decision,
    require_review = excluded.require_review,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: ListCompletionProfiles :many
SELECT * FROM completion_profiles WHERE project_id = ? ORDER BY name ASC;

-- name: GetCompletionProfile :one
SELECT * FROM completion_profiles WHERE id = ?;

-- name: CreateCompletionProfile :one
INSERT INTO completion_profiles (
    project_id, name, autohost_enabled, max_rounds, token_budget, stop_on_decision, require_review
) VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateCompletionProfile :one
UPDATE completion_profiles
SET name = ?, autohost_enabled = ?, max_rounds = ?, token_budget = ?,
    stop_on_decision = ?, require_review = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: DeleteCompletionProfile :exec
DELETE FROM completion_profiles WHERE id = ?;

-- name: CountIssuesByCompletionProfile :one
-- Referential guard: a profile still referenced by an issue cannot be deleted
-- (the caller rejects the delete instead of silently clearing assignments).
SELECT COUNT(*) AS n FROM issues WHERE completion_profile_id = ?;
