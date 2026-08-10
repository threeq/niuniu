-- name: CreateExternalCredential :one
INSERT INTO external_provider_credentials (
    owner_type, owner_id, user_id, provider, alias, config, last_verified_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateExternalCredentialConfig :one
UPDATE external_provider_credentials
SET config = ?, last_verified_at = NULL, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND owner_type = ? AND owner_id = ? AND user_id = ?
RETURNING *;

-- name: RenameExternalCredential :one
UPDATE external_provider_credentials
SET alias = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND owner_type = ? AND owner_id = ? AND user_id = ?
RETURNING *;

-- name: GetExternalCredentialByID :one
SELECT * FROM external_provider_credentials
WHERE id = ? AND owner_type = ? AND owner_id = ? AND user_id = ?;

-- name: ListExternalCredentialsForUser :many
SELECT * FROM external_provider_credentials
WHERE owner_type = ? AND owner_id = ? AND user_id = ?
ORDER BY provider, alias;

-- name: ListExternalCredentialsForUserByProvider :many
SELECT * FROM external_provider_credentials
WHERE owner_type = ? AND owner_id = ? AND user_id = ? AND provider = ?
ORDER BY alias;

-- name: DeleteExternalCredentialByID :exec
DELETE FROM external_provider_credentials
WHERE id = ? AND owner_type = ? AND owner_id = ? AND user_id = ?;

-- name: TouchCredentialVerifiedAtByID :exec
UPDATE external_provider_credentials
SET last_verified_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND owner_type = ? AND owner_id = ? AND user_id = ?;

-- name: CountSourcesUsingCredential :one
SELECT COUNT(*) FROM project_external_sources WHERE credential_id = ?;

-- name: ListSourcesUsingCredential :many
SELECT pes.*, p.name AS project_name
FROM project_external_sources pes
JOIN projects p ON p.id = pes.project_id
WHERE pes.credential_id = ?
ORDER BY p.name, pes.source_key;