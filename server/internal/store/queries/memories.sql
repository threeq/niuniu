-- name: CreateMemory :one
INSERT INTO memories (owner_type, owner_id, project_id, workspace_id, mem_type, title, content, source, source_path, version)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
RETURNING *;

-- name: GetMemory :one
SELECT * FROM memories WHERE id = ?;

-- name: ListMemoriesForOwner :many
SELECT * FROM memories
WHERE owner_type = ? AND owner_id = ? AND deleted_at IS NULL
ORDER BY updated_at DESC;

-- name: ListMemoriesForProject :many
SELECT * FROM memories
WHERE project_id = ? AND deleted_at IS NULL
ORDER BY updated_at DESC;

-- name: ListDeletedMemoriesForOwner :many
SELECT * FROM memories
WHERE owner_type = ? AND owner_id = ? AND deleted_at IS NOT NULL
ORDER BY updated_at DESC;

-- name: CountMemoriesForOwner :one
SELECT COUNT(*) AS count FROM memories
WHERE owner_type = ? AND owner_id = ? AND deleted_at IS NULL;

-- name: UpdateMemoryContent :one
UPDATE memories
SET mem_type = ?, title = ?, content = ?, source = ?, source_path = ?, version = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: SoftDeleteMemory :exec
UPDATE memories SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: ReinforceMemory :exec
UPDATE memories
SET reinforce_count = reinforce_count + 1, last_reinforced_at = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: SupersedeMemory :exec
UPDATE memories
SET superseded_by = ?, deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: RestoreMemory :exec
UPDATE memories SET deleted_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: FlagStaleMemory :exec
-- Soft-delete a memory judged stale, recording the reason and whether the
-- removal awaits human review (pending_review=1) or was auto-applied (=0). The
-- deleted_at IS NULL guard makes it idempotent and prevents an in-flight sweep
-- from re-deleting a memory a human just restored.
UPDATE memories
SET deleted_at = CURRENT_TIMESTAMP, stale_reason = ?, pending_review = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND deleted_at IS NULL;

-- name: ResolveStaleReview :exec
-- Clear the review flag and reason (used when a queued memory is kept/restored).
UPDATE memories
SET pending_review = 0, stale_reason = '', updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: ListPendingReviewForProject :many
-- The review queue: memories soft-deleted by staleness detection but awaiting a
-- human keep/delete decision.
SELECT * FROM memories
WHERE project_id = ? AND pending_review = 1
ORDER BY updated_at DESC;

-- name: HardDeleteMemory :exec
DELETE FROM memories WHERE id = ?;

-- name: CreateMemorySweepRun :one
-- Record one automatic staleness-sweep execution for a project (user-visible log).
INSERT INTO memory_sweep_runs (project_id, trigger, scanned, auto_deleted, queued, detail)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListMemorySweepRunsForProject :many
SELECT * FROM memory_sweep_runs
WHERE project_id = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: DeleteMemorySweepRunsForProject :exec
-- Clear a project's automatic-maintenance run log.
DELETE FROM memory_sweep_runs WHERE project_id = ?;

-- name: UpdateMemorySweepRunDetail :exec
-- Backfill a run's JSON detail once the maintenance workspace finishes.
UPDATE memory_sweep_runs SET detail = ? WHERE id = ?;

-- name: InsertMemoryVersion :one
INSERT INTO memory_versions (memory_id, version, mem_type, title, content, source, source_path)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListMemoryVersions :many
SELECT * FROM memory_versions WHERE memory_id = ? ORDER BY version DESC;

-- name: GetMemoryVersion :one
SELECT * FROM memory_versions WHERE memory_id = ? AND version = ?;
