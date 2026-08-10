-- name: FindIssueByExternalKey :one
-- Look up whether (provider, external_id) has already been imported into a
-- given project. Returns the niuniu issue row when present.
SELECT i.* FROM issues i
JOIN columns c ON c.id = i.column_id
WHERE c.project_id = ? AND i.external_source = ? AND i.external_id = ?;

-- name: BatchFindImportedExternalIDs :many
-- Bulk "already imported" badge query for the browse panel. The slice
-- placeholder expands into an IN list at code-gen time (sqlc.slice is
-- supported by sqlc v1.30.0; see issue_assignees.sql / labels.sql for
-- prior art in this repo).
SELECT i.external_id, i.id AS issue_id, i.column_id
FROM issues i
JOIN columns c ON c.id = i.column_id
WHERE c.project_id = ? AND i.external_source = ?
  AND i.external_id IN (sqlc.slice('external_ids'));

-- name: InsertImportedIssue :one
-- INSERT OR IGNORE so a concurrent import retry on the same external key
-- is a no-op (the SQLite engine returns no row, the PG wrapper rewrites to
-- ON CONFLICT DO NOTHING). The caller treats sql.ErrNoRows as "already
-- imported, look it up via FindIssueByExternalKey".
INSERT OR IGNORE INTO issues (
    column_id, title, description, position,
    external_source, external_id, external_url, external_snapshot_at
) VALUES (?, ?, ?, 0, ?, ?, ?, CURRENT_TIMESTAMP)
RETURNING *;

-- name: RefreshIssueSnapshot :one
-- Re-pull title/description from the upstream source. Guarded by
-- external_source != '' so a stray refresh against a niuniu-only issue
-- is a no-op (returns no row).
UPDATE issues
SET title = ?, description = ?,
    external_snapshot_at = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND external_source != ''
RETURNING *;

-- name: UpdateIssueExternalCommentsSnapshot :exec
-- v1.1: store the JSON-serialized read-only snapshot of external comments
-- pulled by RefreshFromExternal. Guarded by external_source != '' so a
-- stray write to a niuniu-only issue is a no-op.
UPDATE issues
SET external_comments_snapshot = ?,
    external_comments_snapshot_at = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND external_source != '';
