-- name: CreateOrganization :one
INSERT INTO organizations (slug, name, description, created_by)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetOrganization :one
SELECT * FROM organizations WHERE id = ?;

-- name: GetOrganizationBySlug :one
SELECT * FROM organizations WHERE slug = ?;

-- name: ListOrganizationsForUser :many
SELECT o.*, m.role
FROM organizations o
JOIN org_members m ON m.org_id = o.id
WHERE m.user_id = ?
ORDER BY o.name;

-- name: ListAllOrganizations :many
-- Admin-only: every organization, with the caller's own membership role
-- (NULL when the caller is not a member). Powers the global-admin overview.
SELECT o.*, m.role
FROM organizations o
LEFT JOIN org_members m ON m.org_id = o.id AND m.user_id = ?
ORDER BY o.name;

-- name: UpdateOrganization :exec
UPDATE organizations
SET name = ?, description = ?, slug = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteOrganization :exec
DELETE FROM organizations WHERE id = ?;
