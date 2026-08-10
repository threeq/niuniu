-- name: GetProjection :one
SELECT * FROM workspace_scene_projection WHERE workspace_id = ?;

-- name: UpsertProjection :exec
-- Cached projection per workspace. PRIMARY KEY is workspace_id so a vanilla
-- ON CONFLICT (workspace_id) DO UPDATE is sqlc-safe (no partial-index conflict
-- target, unlike workspace_scene_layers; see InsertBaseLayer header).
INSERT INTO workspace_scene_projection
  (workspace_id, digest, projected_definition, missing_credentials,
   install_failures, restart_required, hot_applied_at, recomputed_at)
VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT (workspace_id) DO UPDATE SET
  digest = excluded.digest,
  projected_definition = excluded.projected_definition,
  missing_credentials = excluded.missing_credentials,
  install_failures = excluded.install_failures,
  restart_required = excluded.restart_required,
  hot_applied_at = excluded.hot_applied_at,
  recomputed_at = CURRENT_TIMESTAMP;

-- name: SetDismissedPlugins :exec
-- Overwrites the per-workspace dismissed plugin source list. Only touches the
-- dismissed_plugins column so a concurrent UpsertProjection (recompute) does
-- not clobber it, and vice versa. Requires the projection row to already
-- exist (it does whenever the banner is showing).
UPDATE workspace_scene_projection SET dismissed_plugins = ? WHERE workspace_id = ?;

-- name: DeleteProjection :exec
DELETE FROM workspace_scene_projection WHERE workspace_id = ?;
