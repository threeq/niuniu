-- Integration draft pipeline (Epic #726 / #734). A draft is a persisted but NOT
-- live candidate configuration; versions are the rollback chain.

-- name: CreateIntegrationDraft :one
INSERT INTO integration_drafts (
    owner_type, owner_id, kind, target_name, target_id, status,
    payload, origin_json, advisory_json, doc_ops_json, doc_host,
    lint_json, agent_authored, created_by
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetIntegrationDraft :one
SELECT * FROM integration_drafts WHERE id = ?;

-- name: ListIntegrationDrafts :many
SELECT * FROM integration_drafts
WHERE owner_type = ? AND owner_id = ?
ORDER BY id DESC
LIMIT ?;

-- name: UpdateIntegrationDraftLint :exec
UPDATE integration_drafts
SET status = ?, lint_json = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: UpdateIntegrationDraftConfirm :exec
UPDATE integration_drafts
SET status = 'confirmed', confirm_json = ?, ack_json = ?,
    second_confirmed = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: ResetIntegrationDraftConfirm :exec
UPDATE integration_drafts
SET status = 'draft', confirm_json = '', ack_json = '[]', second_confirmed = 0,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: MarkIntegrationDraftPublished :exec
UPDATE integration_drafts
SET status = 'published', target_id = ?, published_at = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteIntegrationDraft :exec
DELETE FROM integration_drafts WHERE id = ?;

-- name: CreateIntegrationVersion :one
INSERT INTO integration_versions (
    owner_type, owner_id, kind, target_name, target_id, version,
    payload, scope_json, digest, active, published_by, source_draft_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetIntegrationVersion :one
SELECT * FROM integration_versions WHERE id = ?;

-- name: ListIntegrationVersions :many
SELECT * FROM integration_versions
WHERE owner_type = ? AND owner_id = ? AND kind = ? AND target_name = ?
ORDER BY version DESC
LIMIT ?;

-- name: LatestIntegrationVersion :one
-- The highest version number for a target, or sql.ErrNoRows when none exist.
-- Returns the column itself (not MAX(version)) so the type stays int64 -
-- a rollback can leave a lower version active, so "active" is not "latest".
SELECT version FROM integration_versions
WHERE owner_type = ? AND owner_id = ? AND kind = ? AND target_name = ?
ORDER BY version DESC
LIMIT 1;

-- name: DeactivateIntegrationVersions :exec
UPDATE integration_versions
SET active = 0
WHERE owner_type = ? AND owner_id = ? AND kind = ? AND target_name = ?;

-- name: ActivateIntegrationVersion :exec
UPDATE integration_versions SET active = 1 WHERE id = ?;
