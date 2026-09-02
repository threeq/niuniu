-- name: ListCommentsByWorkspace :many
SELECT * FROM comments WHERE workspace_id = ? ORDER BY created_at;

-- name: GetComment :one
SELECT * FROM comments WHERE id = ?;

-- name: CreateComment :one
INSERT INTO comments (workspace_id, repo, file_path, line_number, content, side, commit_sha, blob_sha, context_lines) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: MarkCommentSent :exec
UPDATE comments SET sent_to_agent = TRUE WHERE id = ?;

-- name: ResolveComment :one
-- Review verdict, orthogonal to sent_to_agent (delivery/outbox state).
UPDATE comments SET resolved = TRUE, resolved_at = CURRENT_TIMESTAMP, resolved_by = ? WHERE id = ? RETURNING *;

-- name: UnresolveComment :one
-- Reopen: clears the audit fields so a stale reviewer/timestamp never outlives
-- the verdict it belonged to.
UPDATE comments SET resolved = FALSE, resolved_at = NULL, resolved_by = '' WHERE id = ? RETURNING *;

-- name: UpdateCommentAnchor :exec
-- Relocation result written back by the anchor resolver: a comment whose context
-- snapshot was found at a new offset moves to that line and re-pins its blob.
UPDATE comments SET line_number = ?, blob_sha = ? WHERE id = ?;
