-- name: CreateKnowledgeBase :one
INSERT INTO knowledge_bases (owner_type, owner_id, name, description, source_kind, source_addr, source_config)
VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: GetKnowledgeBase :one
SELECT * FROM knowledge_bases WHERE id = ?;

-- name: GetKnowledgeBaseByOwnerAndName :one
-- KB names are unique per owner (UNIQUE(owner_type, owner_id, name)). Scope the
-- lookup by owner so one owner's KB never blocks another from reusing the name.
SELECT * FROM knowledge_bases WHERE owner_type = ? AND owner_id = ? AND name = ?;

-- name: ListKnowledgeBasesForOwner :many
SELECT * FROM knowledge_bases WHERE owner_type = ? AND owner_id = ? ORDER BY created_at DESC;

-- name: UpdateKnowledgeBase :one
UPDATE knowledge_bases
SET name = ?, description = ?, source_kind = ?, source_addr = ?, source_config = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: DeleteKnowledgeBase :exec
DELETE FROM knowledge_bases WHERE id = ?;

-- name: UpsertKBDocument :one
-- Idempotent per (kb_id, rel_path): re-ingesting a changed file updates the hash
-- and chunk bookkeeping in place rather than creating a duplicate row.
INSERT INTO kb_documents (kb_id, rel_path, uri, content_hash, chunk_count, byte_size)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(kb_id, rel_path) DO UPDATE SET
    uri = excluded.uri,
    content_hash = excluded.content_hash,
    chunk_count = excluded.chunk_count,
    byte_size = excluded.byte_size,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: GetKBDocumentByPath :one
SELECT * FROM kb_documents WHERE kb_id = ? AND rel_path = ?;

-- name: ListKBDocuments :many
SELECT * FROM kb_documents WHERE kb_id = ? ORDER BY rel_path ASC;

-- name: DeleteKBDocumentsForKB :exec
DELETE FROM kb_documents WHERE kb_id = ?;

-- name: CreateKBBinding :exec
INSERT INTO kb_bindings (kb_id, target_type, target_id)
VALUES (?, ?, ?)
ON CONFLICT(kb_id, target_type, target_id) DO NOTHING;

-- name: DeleteKBBinding :exec
DELETE FROM kb_bindings WHERE kb_id = ? AND target_type = ? AND target_id = ?;

-- name: ListKBBindingsForKB :many
SELECT * FROM kb_bindings WHERE kb_id = ? ORDER BY id ASC;

-- name: ListKBBindingsForTarget :many
SELECT * FROM kb_bindings WHERE target_type = ? AND target_id = ? ORDER BY id ASC;

-- name: ListKnowledgeBasesForProject :many
-- KBs visible to a workspace: owned by the given owner AND bound (kb_bindings)
-- to the project. A KB with no project binding is intentionally NOT returned
-- (unbound is invisible). owner_type/owner_id enforce tenant isolation on top of
-- the binding so a stray cross-owner binding can never leak a KB. A disabled KB
-- (status = 'disabled', set by the management UI start/stop toggle) is excluded
-- so disabling actually stops both kb_search and the agent direct-read mount.
SELECT kb.* FROM knowledge_bases kb
JOIN kb_bindings b ON b.kb_id = kb.id
WHERE b.target_type = 'project'
  AND b.target_id = ?
  AND kb.owner_type = ?
  AND kb.owner_id = ?
  AND kb.status <> 'disabled'
ORDER BY kb.created_at DESC;
