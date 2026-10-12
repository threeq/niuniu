-- Team-edition usage policy for ORG-owned provider credentials (issue #742).
-- An org credential is usable only inside its explicitly granted scopes; no
-- grants means unusable everywhere (default-minimal). Personal (user-owned)
-- credentials never consult this table. All comments must be pure ASCII.

-- name: GrantCredentialUsage :exec
INSERT INTO credential_usage_grants (credential_id, scope_type, scope_id, granted_by)
VALUES (?, ?, ?, ?)
ON CONFLICT (credential_id, scope_type, scope_id) DO NOTHING;

-- name: RevokeCredentialUsage :execrows
DELETE FROM credential_usage_grants
WHERE credential_id = ? AND scope_type = ? AND scope_id = ?;

-- name: ListCredentialUsageGrants :many
SELECT id, credential_id, scope_type, scope_id, granted_by, created_at
FROM credential_usage_grants
WHERE credential_id = ?
ORDER BY scope_type, scope_id;

-- name: CountCredentialUsageGrants :one
SELECT COUNT(*) FROM credential_usage_grants WHERE credential_id = ?;

-- name: DeleteCredentialUsageGrantsForCredential :exec
DELETE FROM credential_usage_grants WHERE credential_id = ?;

-- name: CredentialUsageGrantExistsForProject :one
-- Does the credential carry a project-scope grant for this project?
SELECT EXISTS(
    SELECT 1 FROM credential_usage_grants
    WHERE credential_id = ? AND scope_type = 'project' AND scope_id = ?
) AS allowed;

-- name: CredentialUsageGrantExistsForWorkspace :one
-- Does the credential carry a workspace-scope grant for this workspace?
SELECT EXISTS(
    SELECT 1 FROM credential_usage_grants
    WHERE credential_id = ? AND scope_type = 'workspace' AND scope_id = ?
) AS allowed;

-- name: CredentialUsageGrantExistsInProject :one
-- Is the credential authorized anywhere inside this project? True when a
-- project-scope grant names the project, OR a workspace-scope grant names a
-- workspace belonging to it. Gates project-level source binding (issue #742).
SELECT EXISTS(
    SELECT 1 FROM credential_usage_grants g
    WHERE g.credential_id = ? AND (
        (g.scope_type = 'project' AND g.scope_id = ?)
        OR (g.scope_type = 'workspace' AND g.scope_id IN (
            SELECT w.id FROM workspaces w
            JOIN issues i ON w.issue_id = i.id
            JOIN columns c ON i.column_id = c.id
            WHERE c.project_id = ?
        ))
    )
) AS allowed;
