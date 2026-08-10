-- name: AddOrgMember :exec
INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, ?);

-- name: GetOrgMember :one
SELECT * FROM org_members WHERE org_id = ? AND user_id = ?;

-- name: ListOrgMembers :many
SELECT m.*, u.username, u.display_name
FROM org_members m
JOIN users u ON u.id = m.user_id
WHERE m.org_id = ?
ORDER BY u.username;

-- name: UpdateOrgMemberRole :exec
UPDATE org_members SET role = ? WHERE org_id = ? AND user_id = ?;

-- name: RemoveOrgMember :exec
DELETE FROM org_members WHERE org_id = ? AND user_id = ?;

-- name: CountOrgOwners :one
SELECT COUNT(*) FROM org_members WHERE org_id = ? AND role = 'owner';

-- name: ListOrgIDsForUser :many
SELECT org_id FROM org_members WHERE user_id = ?;

-- name: GetOrgFallbackIdentity :one
-- Resolve a stable user identity for an org when no session user and no
-- workspace creator are available (MCP credential-tool auth fallback). Prefer
-- an owner, then an admin, then any member; deterministic tie-break by join
-- time then user id so the choice stays stable across calls.
SELECT user_id FROM org_members
WHERE org_id = ?
ORDER BY CASE role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END, created_at, user_id
LIMIT 1;
