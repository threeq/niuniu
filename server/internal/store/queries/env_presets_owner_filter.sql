-- name: ListEnvPresetsForOwners :many
-- The third disjunct (owner_id = 0) surfaces system defaults seeded by
-- EnvPresetService.SeedDefaults to every caller regardless of their personal
-- or org scope. owner_id 0 is the schema-level sentinel for "no real owner"
-- (real users start at id=1; the CHECK constraint forces owner_type='user'
-- so seeded rows pass schema validation while still being globally visible).
SELECT * FROM env_presets
WHERE (owner_type = 'user' AND owner_id = ?)
   OR (owner_type = 'org'  AND owner_id IN (sqlc.slice('org_ids')))
   OR (owner_type = 'user' AND owner_id = 0)
ORDER BY created_at DESC;
