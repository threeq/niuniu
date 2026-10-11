-- Sandbox operation functions (Epic #726 / #739).
-- Registry, rollback chain, per-call audit and the idempotency ledger.

-- name: CreateIntegrationFunction :one
INSERT INTO integration_functions (
    owner_type, owner_id, provider_id, provider_name, name, description,
    runtime, code, code_hash, entry, limits_json, egress_json, idempotent,
    enabled, version, status, observation_limit, created_by
) VALUES (
    sqlc.arg(owner_type), sqlc.arg(owner_id), sqlc.arg(provider_id),
    sqlc.arg(provider_name), sqlc.arg(name), sqlc.arg(description),
    sqlc.arg(runtime), sqlc.arg(code), sqlc.arg(code_hash), sqlc.arg(entry),
    sqlc.arg(limits_json), sqlc.arg(egress_json), sqlc.arg(idempotent),
    sqlc.arg(enabled), sqlc.arg(version), sqlc.arg(status),
    sqlc.arg(observation_limit), sqlc.arg(created_by)
)
RETURNING *;

-- name: GetIntegrationFunction :one
SELECT * FROM integration_functions WHERE id = sqlc.arg(id);

-- name: GetIntegrationFunctionByName :one
SELECT * FROM integration_functions
WHERE provider_id = sqlc.arg(provider_id) AND name = sqlc.arg(name);

-- name: ListIntegrationFunctionsByOwner :many
SELECT * FROM integration_functions
WHERE owner_type = sqlc.arg(owner_type) AND owner_id = sqlc.arg(owner_id)
ORDER BY provider_name, name;

-- name: ListIntegrationFunctionsByProvider :many
SELECT * FROM integration_functions
WHERE provider_id = sqlc.arg(provider_id)
ORDER BY name;

-- name: DeleteIntegrationFunction :exec
DELETE FROM integration_functions WHERE id = sqlc.arg(id);

-- name: DeleteIntegrationFunctionsByProvider :exec
-- Removing a source removes the functions that belong to it: a function has no
-- meaning without the provider whose operations it reaches.
DELETE FROM integration_functions WHERE provider_id = sqlc.arg(provider_id);

-- name: UpdateIntegrationFunctionEnabled :exec
UPDATE integration_functions
SET enabled = sqlc.arg(enabled), status = sqlc.arg(status),
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: ReplaceIntegrationFunctionBody :exec
-- A republish replaces the whole body; version is bumped by the caller.
UPDATE integration_functions
SET description = sqlc.arg(description),
    runtime = sqlc.arg(runtime),
    code = sqlc.arg(code),
    code_hash = sqlc.arg(code_hash),
    entry = sqlc.arg(entry),
    limits_json = sqlc.arg(limits_json),
    egress_json = sqlc.arg(egress_json),
    idempotent = sqlc.arg(idempotent),
    version = sqlc.arg(version),
    status = sqlc.arg(status),
    observation_calls = sqlc.arg(observation_calls),
    observation_limit = sqlc.arg(observation_limit),
    enabled = sqlc.arg(enabled),
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: RecordFunctionObservationCall :exec
-- Advances the first-run observation counter and flips to 'active' when the
-- window closes.
--
-- The status transition is conditional on the row still being 'observing': a
-- call that was already in flight when the user disabled the function must not
-- flip it back on as a side effect of its own bookkeeping.
UPDATE integration_functions
SET observation_calls = observation_calls + 1,
    status = CASE WHEN status = sqlc.arg(observing_status)
                  THEN sqlc.arg(next_status)
                  ELSE status END,
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: DeleteIntegrationFunctionVersions :exec
DELETE FROM integration_function_versions WHERE function_id = sqlc.arg(function_id);

-- name: DeleteFunctionIdempotencyForFunction :exec
-- The ledger is implementation state, not an audit record: the audit table is
-- kept forever, but a deleted function's reservations have no reader.
DELETE FROM function_idempotency WHERE function_id = sqlc.arg(function_id);

-- --- versions (rollback chain) -------------------------------------------

-- name: CreateIntegrationFunctionVersion :one
INSERT INTO integration_function_versions (
    function_id, owner_type, owner_id, provider_id, name, version, code,
    code_hash, egress_json, limits_json, idempotent, scope_json, active,
    published_by, source_draft_id
) VALUES (
    sqlc.arg(function_id), sqlc.arg(owner_type), sqlc.arg(owner_id),
    sqlc.arg(provider_id), sqlc.arg(name), sqlc.arg(version), sqlc.arg(code),
    sqlc.arg(code_hash), sqlc.arg(egress_json), sqlc.arg(limits_json),
    sqlc.arg(idempotent), sqlc.arg(scope_json), sqlc.arg(active),
    sqlc.arg(published_by), sqlc.arg(source_draft_id)
)
RETURNING *;

-- name: GetIntegrationFunctionVersion :one
SELECT * FROM integration_function_versions WHERE id = sqlc.arg(id);

-- name: LatestIntegrationFunctionVersion :one
-- Returns sql.ErrNoRows when the function has no versions yet; callers treat
-- that as "next version is 1".
SELECT version FROM integration_function_versions
WHERE function_id = sqlc.arg(function_id)
ORDER BY version DESC
LIMIT 1;

-- name: ListIntegrationFunctionVersions :many
SELECT * FROM integration_function_versions
WHERE function_id = sqlc.arg(function_id)
ORDER BY version DESC
LIMIT sqlc.arg(limit_count);

-- name: GetActiveIntegrationFunctionVersion :one
SELECT * FROM integration_function_versions
WHERE function_id = sqlc.arg(function_id) AND active = 1;

-- name: DeactivateIntegrationFunctionVersions :exec
UPDATE integration_function_versions
SET active = 0
WHERE function_id = sqlc.arg(function_id);

-- name: ActivateIntegrationFunctionVersion :exec
UPDATE integration_function_versions
SET active = 1
WHERE id = sqlc.arg(id);

-- --- per-call audit -------------------------------------------------------

-- name: CreateFunctionCallAudit :one
INSERT INTO function_call_audit (
    owner_type, owner_id, user_id, function_id, provider_id, function_name,
    provider_name, workspace_id, caller, status, error_code, error_message,
    duration_ms, egress_calls, egress_ops, input_sha256, input_bytes,
    output_bytes, peak_memory_bytes, idempotency_key, replayed, observation,
    fence_backend, code_hash
) VALUES (
    sqlc.arg(owner_type), sqlc.arg(owner_id), sqlc.arg(user_id),
    sqlc.arg(function_id), sqlc.arg(provider_id), sqlc.arg(function_name),
    sqlc.arg(provider_name), sqlc.arg(workspace_id), sqlc.arg(caller),
    sqlc.arg(status), sqlc.arg(error_code), sqlc.arg(error_message),
    sqlc.arg(duration_ms), sqlc.arg(egress_calls), sqlc.arg(egress_ops),
    sqlc.arg(input_sha256), sqlc.arg(input_bytes), sqlc.arg(output_bytes),
    sqlc.arg(peak_memory_bytes), sqlc.arg(idempotency_key), sqlc.arg(replayed),
    sqlc.arg(observation), sqlc.arg(fence_backend), sqlc.arg(code_hash)
)
RETURNING *;

-- name: ListFunctionCallAudit :many
SELECT * FROM function_call_audit
WHERE owner_type = sqlc.arg(owner_type) AND owner_id = sqlc.arg(owner_id)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit_count);

-- name: ListFunctionCallAuditForFunction :many
SELECT * FROM function_call_audit
WHERE function_id = sqlc.arg(function_id)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit_count);

-- name: ListObservationFunctionCalls :many
-- The first-run observation trail: everything the window has seen, newest
-- first, so an operator can review what the function actually reached.
SELECT * FROM function_call_audit
WHERE function_id = sqlc.arg(function_id) AND observation = 1
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit_count);

-- name: CountFunctionCallsSince :one
SELECT COUNT(*) FROM function_call_audit
WHERE function_id = sqlc.arg(function_id) AND created_at >= sqlc.arg(since);

-- --- idempotency ledger ---------------------------------------------------

-- name: CreateFunctionIdempotency :one
INSERT INTO function_idempotency (
    function_id, idem_key, call_audit_id, status, output_json
) VALUES (
    sqlc.arg(function_id), sqlc.arg(idem_key), sqlc.arg(call_audit_id),
    sqlc.arg(status), sqlc.arg(output_json)
)
RETURNING *;

-- name: GetFunctionIdempotency :one
SELECT * FROM function_idempotency
WHERE function_id = sqlc.arg(function_id) AND idem_key = sqlc.arg(idem_key);

-- name: FinalizeFunctionIdempotency :exec
-- Settles a reserved idempotency key once the call has actually finished.
-- A reservation left in 'running' by a crash is retryable after the staleness
-- window the service applies.
UPDATE function_idempotency
SET status = sqlc.arg(status),
    output_json = sqlc.arg(output_json),
    call_audit_id = sqlc.arg(call_audit_id)
WHERE function_id = sqlc.arg(function_id) AND idem_key = sqlc.arg(idem_key);

-- --- unified activity feed (Epic #726 / #728 + #739) -----------------------

-- name: ListFunctionCallAuditByUser :many
-- Caller-scoped feed of sandbox function calls, newest first. Mirrors
-- ListExternalApiAudit: provider_id = 0 means "any", and COALESCE anchors the
-- nullable time bounds to the created_at column so Postgres can infer their
-- type at parse time (and makes the NULL case a no-op comparison).
SELECT * FROM function_call_audit
WHERE user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(provider_id) AS BIGINT) = 0 OR provider_id = sqlc.arg(provider_id))
  AND created_at >= COALESCE(sqlc.narg(from_ts), created_at)
  AND created_at <= COALESCE(sqlc.narg(to_ts), created_at)
ORDER BY id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CountFunctionCallAuditByUser :one
SELECT COUNT(*) AS n FROM function_call_audit
WHERE user_id = sqlc.arg(user_id)
  AND (CAST(sqlc.arg(provider_id) AS BIGINT) = 0 OR provider_id = sqlc.arg(provider_id))
  AND created_at >= COALESCE(sqlc.narg(from_ts), created_at)
  AND created_at <= COALESCE(sqlc.narg(to_ts), created_at);
