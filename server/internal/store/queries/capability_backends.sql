-- Capability backends (video-creation capability config). These queries back
-- the capability_backends table: per-owner bindings from a capability module
-- (e.g. video-gen) to a concrete generation backend adapter (tts / image /
-- video). Deliberately separate from env_providers, which serve the agent's
-- LLM accounts; capability rows are projected into the module process as
-- NN_CAP_<CAP>_* environment variables. api_key is stored inline and is never
-- echoed back by the API layer (responses carry has_api_key instead).

-- name: ListCapabilityBackendsByOwner :many
SELECT * FROM capability_backends
WHERE module = ? AND owner_type = ? AND owner_id = ?
ORDER BY capability ASC, position ASC, name ASC, id ASC;

-- name: ListCapabilityBackendsByModule :many
-- Every owner's rows for one module (auth-off / admin view). Ordered so the
-- settings UI shows each capability's configs in resolution order.
SELECT * FROM capability_backends
WHERE module = ?
ORDER BY capability ASC, owner_type ASC, owner_id ASC, position ASC, name ASC, id ASC;

-- name: GetCapabilityBackend :one
SELECT * FROM capability_backends WHERE id = ?;

-- name: ListCapabilityBackendsForResolve :many
-- Candidate rows for resolving one workspace's capability env: enabled rows
-- from the two owner scopes that can win for that workspace -- the workspace
-- itself (override) and the owning user (global default). Picking the winner
-- per capability (workspace beats user; then lowest position) happens in
-- service.CapabilityBackendService.ResolveEnv.
SELECT * FROM capability_backends
WHERE module = ? AND enabled = 1
  AND ((owner_type = 'workspace' AND owner_id = ?) OR (owner_type = 'user' AND owner_id = ?))
ORDER BY capability ASC, position ASC, id ASC;

-- name: CreateCapabilityBackend :one
INSERT INTO capability_backends (owner_type, owner_id, module, capability, backend, name, base_url, api_key, extra_config, enabled, position)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateCapabilityBackend :one
UPDATE capability_backends
SET module = ?, capability = ?, backend = ?, name = ?, base_url = ?, api_key = ?,
    extra_config = ?, enabled = ?, position = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: DeleteCapabilityBackend :exec
DELETE FROM capability_backends WHERE id = ?;
