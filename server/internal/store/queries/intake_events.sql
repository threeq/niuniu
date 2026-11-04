-- name: CreateIntakeEvent :one
-- INSERT OR IGNORE so redelivering the same external event is a no-op: the
-- SQLite engine returns no row and the PG wrapper rewrites to ON CONFLICT DO
-- NOTHING. The caller treats sql.ErrNoRows as "duplicate" and resolves the
-- existing row with GetIntakeEventByKey.
INSERT OR IGNORE INTO intake_events (
    project_id, source_kind, source_ref, event_ext_id, event_kind,
    actor_ext_id, actor_name, thread_ext_id, payload_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetIntakeEventByKey :one
SELECT * FROM intake_events
WHERE source_kind = ? AND source_ref = ? AND event_ext_id = ?;

-- name: GetIntakeEvent :one
SELECT * FROM intake_events WHERE id = ?;

-- name: ListIntakeEventsByProject :many
SELECT * FROM intake_events
WHERE project_id = ?
ORDER BY received_at DESC, id DESC
LIMIT ?;
