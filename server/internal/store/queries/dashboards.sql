-- name: CreateSavedQuery :one
INSERT INTO saved_queries
  (owner_type, owner_id, source_id, workspace_id, name, operation, chart_spec, snapshot)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: GetSavedQuery :one
SELECT * FROM saved_queries WHERE id = ?;

-- name: ListSavedQueriesForOwners :many
SELECT * FROM saved_queries
WHERE (owner_type = 'user' AND owner_id = ?)
   OR (owner_type = 'org'  AND owner_id IN (sqlc.slice('org_ids')))
ORDER BY created_at DESC;

-- name: DeleteSavedQuery :exec
DELETE FROM saved_queries WHERE id = ?;

-- name: CreateDashboard :one
INSERT INTO dashboards (owner_type, owner_id, name, layout)
VALUES (?, ?, ?, ?)
RETURNING id;

-- name: GetDashboard :one
SELECT * FROM dashboards WHERE id = ?;

-- name: ListDashboardsForOwners :many
SELECT * FROM dashboards
WHERE (owner_type = 'user' AND owner_id = ?)
   OR (owner_type = 'org'  AND owner_id IN (sqlc.slice('org_ids')))
ORDER BY created_at DESC;

-- name: CountDashboardsForOwners :one
SELECT COUNT(*) FROM dashboards
WHERE (owner_type = 'user' AND owner_id = ?)
   OR (owner_type = 'org'  AND owner_id IN (sqlc.slice('org_ids')));

-- name: UpdateDashboard :exec
UPDATE dashboards
SET name = ?, layout = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteDashboard :exec
DELETE FROM dashboards WHERE id = ?;

-- name: CreatePanel :one
INSERT INTO dashboard_panels
  (dashboard_id, saved_query_id, title, viz_type, chart_spec, grid_x, grid_y, grid_w, grid_h, refresh_interval_sec)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: GetPanel :one
SELECT * FROM dashboard_panels WHERE id = ?;

-- name: ListPanelsByDashboard :many
SELECT * FROM dashboard_panels
WHERE dashboard_id = ?
ORDER BY created_at ASC;

-- name: DeletePanel :exec
DELETE FROM dashboard_panels WHERE id = ?;

-- name: MovePanelToDashboard :exec
UPDATE dashboard_panels SET dashboard_id = ? WHERE id = ?;

-- name: ListPanelsForWorkspace :many
-- Panels pinned from a given workspace (saved_queries.workspace_id), across all
-- of the caller's accessible dashboards, with the owning dashboard's name for
-- context. Owner-filtered like ListDashboardsForOwners.
SELECT p.*, d.name AS dashboard_name,
       sq.source_id    AS sq_source_id,
       sq.workspace_id AS sq_workspace_id
FROM dashboard_panels p
JOIN saved_queries sq ON sq.id = p.saved_query_id
JOIN dashboards d ON d.id = p.dashboard_id
WHERE sq.workspace_id = ?
  AND ( (d.owner_type = 'user' AND d.owner_id = ?)
        OR (d.owner_type = 'org' AND d.owner_id IN (sqlc.slice('org_ids'))) )
ORDER BY p.created_at DESC;

-- name: GetPanelWithQuery :one
-- Join saved_queries so panel data + back-link can be served in one read:
-- sq_workspace_id (origin workspace for click-to-jump), sq_source_id,
-- sq_operation (to re-run the read-only query through the data proxy gate),
-- and sq_owner_type/sq_owner_id (so audit/confirm attribute to the saved
-- query's real owner, not the calling user).
SELECT p.*,
       sq.workspace_id AS sq_workspace_id,
       sq.source_id    AS sq_source_id,
       sq.operation    AS sq_operation,
       sq.snapshot     AS sq_snapshot,
       sq.owner_type   AS sq_owner_type,
       sq.owner_id     AS sq_owner_id
FROM dashboard_panels p
JOIN saved_queries sq ON sq.id = p.saved_query_id
WHERE p.id = ?;

-- name: ListPanelsWithQueryByDashboard :many
SELECT p.*,
       sq.workspace_id AS sq_workspace_id,
       sq.source_id    AS sq_source_id,
       sq.operation    AS sq_operation,
       sq.snapshot     AS sq_snapshot
FROM dashboard_panels p
JOIN saved_queries sq ON sq.id = p.saved_query_id
WHERE p.dashboard_id = ?
ORDER BY p.created_at ASC;
