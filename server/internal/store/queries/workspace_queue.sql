-- name: ListQueueItems :many
SELECT * FROM workspace_queue
WHERE workspace_id = ?
ORDER BY position ASC;

-- name: GetQueueItem :one
SELECT * FROM workspace_queue WHERE id = ?;

-- name: CreateQueueItem :one
INSERT INTO workspace_queue (workspace_id, content, position, source, attachments)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetMaxQueuePosition :one
SELECT CAST(COALESCE(MAX(position), 0) AS REAL) AS max_position
FROM workspace_queue
WHERE workspace_id = ?;

-- name: GetMinQueuePosition :one
SELECT CAST(COALESCE(MIN(position), 0) AS REAL) AS min_position
FROM workspace_queue
WHERE workspace_id = ?;

-- name: UpdateQueueItemContent :exec
UPDATE workspace_queue
SET content = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND workspace_id = ?;

-- name: UpdateQueueItemPosition :exec
UPDATE workspace_queue
SET position = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND workspace_id = ?;

-- name: DeleteQueueItem :exec
DELETE FROM workspace_queue WHERE id = ? AND workspace_id = ?;

-- name: DequeueMessage :one
DELETE FROM workspace_queue
WHERE id = (
    SELECT wq.id FROM workspace_queue wq
    WHERE wq.workspace_id = ?
    ORDER BY wq.position ASC
    LIMIT 1
)
RETURNING *;

-- name: CountQueueItems :one
SELECT COUNT(*) AS count FROM workspace_queue
WHERE workspace_id = ?;

-- name: HasRetryItem :one
SELECT COUNT(*) AS count FROM workspace_queue
WHERE workspace_id = ? AND source = 'retry';
