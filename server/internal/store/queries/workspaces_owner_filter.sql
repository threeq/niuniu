-- name: ListWorkspacesForOwners :many
-- Workspaces accessible to caller (personal + member orgs).
-- The ? = 0 sentinel avoids the 42P18 parameter-typing issue on Postgres
-- while keeping a single cross-dialect query: when the creator filter is 0
-- (no filter), ? = 0 evaluates to TRUE and the entire AND-group is a no-op.
-- When the filter is set (cid > 0), ? = 0 is FALSE, and the remaining
-- branches (created_by = ? and the legacy-NULL fallback) are tested.
SELECT * FROM workspaces
WHERE is_archived = 0
  AND ((owner_type = 'user' AND owner_id = ?)
    OR (owner_type = 'org'  AND owner_id IN (sqlc.slice('org_ids'))))
  AND (
        ? = 0
     OR created_by = ?
     OR (
          created_by IS NULL
          AND owner_type = 'user'
          AND owner_id = ?
        )
  )
ORDER BY created_at DESC;

-- name: ListWorkspaceCreatorsForOwners :many
-- Distinct creators within the (owner_user_id, org_ids) scope; caller
-- pre-narrows scope via authz to avoid cross-org identity leaks.
-- Excludes NULL created_by (picker has no "unknown" option).
SELECT DISTINCT u.id, u.username, u.display_name
FROM workspaces w
JOIN users u ON u.id = w.created_by
WHERE w.is_archived = 0
  AND w.created_by IS NOT NULL
  AND ((w.owner_type = 'user' AND w.owner_id = ?)
    OR (w.owner_type = 'org'  AND w.owner_id IN (sqlc.slice('org_ids'))))
ORDER BY u.display_name, u.username;