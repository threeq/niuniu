-- name: ListProjectsForOwners :many
SELECT * FROM projects
WHERE (owner_type = 'user' AND owner_id = ?)
   OR (owner_type = 'org'  AND owner_id IN (sqlc.slice('org_ids')))
ORDER BY created_at DESC;
