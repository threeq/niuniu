-- name: ListColumnGateSpecs :many
SELECT * FROM column_gate_specs WHERE column_id = ? ORDER BY position;

-- name: ListColumnGateSpecsByColumns :many
-- Batched form of ListColumnGateSpecs for listing many columns' gate bindings at
-- once (the project columns endpoint). Caller groups rows by column_id; ordering
-- keeps each column's bindings in position order.
SELECT * FROM column_gate_specs
WHERE column_id IN (sqlc.slice('column_ids'))
ORDER BY column_id, position;

-- name: DeleteColumnGateSpecs :exec
DELETE FROM column_gate_specs WHERE column_id = ?;

-- name: CountSpecFloorBindings :one
-- How many columns bind this spec as a project floor (applicability='always').
-- Used to protect a floor's severity from being silently downgraded (spec section 18).
SELECT CAST(COUNT(*) AS INTEGER) FROM column_gate_specs WHERE spec_id = ? AND applicability = 'always';

-- name: InsertColumnGateSpec :exec
-- applicability is if_routed (column-level gate) or always (project floor); the
-- caller passes it explicitly so a re-bind never silently resets a floor to if_routed.
INSERT INTO column_gate_specs (column_id, spec_id, position, applicability) VALUES (?, ?, ?, ?);
