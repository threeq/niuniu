-- name: ListQuickActionsForOwners :many
-- The owner_id = 0 disjunct surfaces system defaults seeded by
-- QuickActionService.SeedDefaults to every caller (same sentinel as env_presets).
SELECT * FROM quick_actions
WHERE (owner_type = 'user' AND owner_id = ?)
   OR (owner_type = 'org'  AND owner_id IN (sqlc.slice('org_ids')))
   OR (owner_type = 'user' AND owner_id = 0)
ORDER BY position, id;
