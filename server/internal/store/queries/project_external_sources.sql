-- name: AddProjectExternalSource :one
INSERT INTO project_external_sources (project_id, provider, source_key, credential_id, config)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(project_id, provider, source_key) DO UPDATE SET
    credential_id = excluded.credential_id,
    config = excluded.config
RETURNING *;

-- name: ListProjectExternalSources :many
SELECT * FROM project_external_sources
WHERE project_id = ?
ORDER BY provider, source_key;

-- name: ListProjectExternalSourcesForOwners :many
SELECT pes.* FROM project_external_sources pes
JOIN projects p ON p.id = pes.project_id
WHERE (p.owner_type = 'user' AND p.owner_id = ?)
   OR (p.owner_type = 'org'  AND p.owner_id IN (sqlc.slice('org_ids')))
ORDER BY pes.provider, pes.source_key;

-- name: GetProjectExternalSource :one
SELECT * FROM project_external_sources
WHERE id = ?;

-- name: UpdateProjectExternalSourceConfig :one
UPDATE project_external_sources
SET config = ?
WHERE id = ?
RETURNING *;

-- name: GetProjectExternalSourceByKey :one
SELECT * FROM project_external_sources
WHERE project_id = ? AND provider = ? AND source_key = ?;

-- name: DeleteProjectExternalSource :exec
DELETE FROM project_external_sources WHERE id = ?;