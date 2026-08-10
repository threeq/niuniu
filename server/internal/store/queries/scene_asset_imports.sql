-- name: ListImportsForWorkspace :many
SELECT * FROM scene_asset_imports
WHERE workspace_id = ?
ORDER BY imported_at ASC;

-- name: ListImportsForScene :many
SELECT * FROM scene_asset_imports
WHERE scene_id = ?
ORDER BY imported_at ASC;

-- name: GetImportByAsset :one
SELECT * FROM scene_asset_imports
WHERE workspace_id = ? AND scene_id = ? AND asset_kind = ? AND asset_id = ?
LIMIT 1;

-- name: CreateImport :exec
INSERT INTO scene_asset_imports
  (workspace_id, scene_id, asset_kind, asset_id)
VALUES (?, ?, ?, ?);

-- name: DeleteImportsForWorkspaceScene :exec
DELETE FROM scene_asset_imports
WHERE workspace_id = ? AND scene_id = ?;
