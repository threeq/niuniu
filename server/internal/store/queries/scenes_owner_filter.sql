-- name: ListScenesAccessible :many
-- Scenes visible to the calling user. Combines three disjuncts:
--   1. builtin (owner_type='user', owner_id=0) -- globally visible
--   2. caller's own owner scope (owner_type=?, owner_id=?)
--   3. any org scene where the caller is a member
-- Every '?' anchors to a typed column so PG can infer parse-time types
-- (CLAUDE.md "Known PG-on-server pitfalls").
SELECT s.id, s.owner_type, s.owner_id, s.slug, s.display_name, s.description,
       s.tags, s.source, s.source_slug, s.definition, s.content_hash,
       s.enabled, s.created_at, s.updated_at
FROM scenes s
WHERE
  (s.source = 'builtin' AND s.owner_type = 'user' AND s.owner_id = 0)
  OR (s.owner_type = ? AND s.owner_id = ?)
  OR (
    s.owner_type = 'org'
    AND s.owner_id IN (
      SELECT org_id FROM org_members WHERE user_id = ?
    )
  )
ORDER BY
  CASE WHEN s.source = 'builtin' THEN 1 ELSE 0 END,
  s.display_name ASC;

-- name: ListAttachedSceneIDs :many
-- Scene IDs already attached to a workspace (base layer has scene_id NULL
-- by invariant and is excluded here).
SELECT scene_id FROM workspace_scene_layers
WHERE workspace_id = ? AND is_base = 0;
