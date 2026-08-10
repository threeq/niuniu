-- name: EnqueueWorkspaceStart :exec
-- Append an issue to an owner's start-overflow queue.
INSERT INTO workspace_start_queue (owner_type, owner_id, issue_id, status)
VALUES (?, ?, ?, 'queued');

-- name: CountQueuedForOwner :one
-- Number of issues currently queued for an owner (used as the queue position).
SELECT COUNT(*) FROM workspace_start_queue
WHERE owner_type = ? AND owner_id = ? AND status = 'queued';

-- name: CountQueuedForIssue :one
-- Whether a specific issue is already queued (keeps re-dispatch idempotent).
SELECT COUNT(*) FROM workspace_start_queue
WHERE issue_id = ? AND status = 'queued';

-- name: DequeueNextForOwner :one
-- Oldest queued entry for an owner, FIFO.
SELECT id, issue_id FROM workspace_start_queue
WHERE owner_type = ? AND owner_id = ? AND status = 'queued'
ORDER BY enqueued_at, id
LIMIT 1;

-- name: ListOwnersWithQueuedStarts :many
-- Distinct owners that currently have at least one queued start entry. Drives a
-- global drain when the concurrency cap is raised or removed, so freed capacity
-- is used immediately instead of waiting for the next workspace-completion event.
SELECT DISTINCT owner_type, owner_id FROM workspace_start_queue
WHERE status = 'queued';

-- name: MarkStartQueueStarted :exec
UPDATE workspace_start_queue
SET status = 'started', started_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: MarkStartQueueCanceled :exec
UPDATE workspace_start_queue
SET status = 'canceled'
WHERE id = ?;
