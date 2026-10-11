-- Continuous integration-source health (Epic #726 / #744).
-- Probe scheduling, health-state writes, contract-drift state, and the
-- probe-run audit feed shared by the Provider and DataSource lines.

-- name: ListProvidersForProbe :many
-- Every provider, regardless of enabled/owner -- the sweep decides what to
-- probe; a disabled provider is still probed so it can auto-recover.
SELECT * FROM external_providers ORDER BY id;

-- name: ListDataSourcesForProbe :many
-- Every data source, regardless of owner/enabled -- the sweep probes them all
-- so a recovered upstream re-enables the source automatically.
SELECT * FROM data_sources ORDER BY id;

-- name: UpdateProviderProbeResult :exec
-- One probe verdict. enabled + auto_disabled_at carry the caller-decided state
-- (unchanged on a normal probe; flipped by the auto-disable / recovery logic).
UPDATE external_providers
SET health_status = sqlc.arg(health_status),
    consecutive_failures = sqlc.arg(consecutive_failures),
    last_probe_at = CURRENT_TIMESTAMP,
    last_probe_error = sqlc.arg(last_probe_error),
    enabled = sqlc.arg(enabled),
    auto_disabled_at = sqlc.narg(auto_disabled_at),
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: UpdateProviderDriftState :exec
-- Contract-drift bookkeeping. upstream_spec_digest is the baseline the next
-- probe compares against; contract_drift = 1 until the user acknowledges.
UPDATE external_providers
SET contract_drift = sqlc.arg(contract_drift),
    upstream_spec_digest = sqlc.arg(upstream_spec_digest),
    last_drift_at = sqlc.narg(last_drift_at),
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: ReenableProvider :exec
-- Recovery path: clear the auto-disable and failure streak so the next probe
-- starts from a clean slate.
UPDATE external_providers
SET enabled = 1,
    auto_disabled_at = NULL,
    consecutive_failures = 0,
    health_status = 'unknown',
    last_probe_error = '',
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: SetExternalProviderEnabled :exec
UPDATE external_providers
SET enabled = sqlc.arg(enabled), updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: UpdateDataSourceProbeResult :exec
UPDATE data_sources
SET health_status = sqlc.arg(health_status),
    consecutive_failures = sqlc.arg(consecutive_failures),
    last_probe_at = CURRENT_TIMESTAMP,
    last_probe_error = sqlc.arg(last_probe_error),
    enabled = sqlc.arg(enabled),
    auto_disabled_at = sqlc.narg(auto_disabled_at),
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: ReenableDataSource :exec
UPDATE data_sources
SET enabled = 1,
    auto_disabled_at = NULL,
    consecutive_failures = 0,
    health_status = 'unknown',
    last_probe_error = '',
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: SetDataSourceEnabled :exec
UPDATE data_sources
SET enabled = sqlc.arg(enabled), updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: CreateIntegrationProbeRun :exec
INSERT INTO integration_probe_runs
  (subject_kind, subject_id, owner_type, owner_id, status, message, latency_ms,
   contract_digest, upstream_digest, drift, consecutive_failures, auto_disabled)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListIntegrationProbeRuns :many
-- subject_id = 0 means "any subject of this kind".
SELECT * FROM integration_probe_runs
WHERE subject_kind = sqlc.arg(subject_kind)
  AND (CAST(sqlc.arg(subject_id) AS BIGINT) = 0 OR subject_id = sqlc.arg(subject_id))
ORDER BY id DESC
LIMIT sqlc.arg(lim);

-- name: ListProbeCredentialOwnersForProvider :many
-- Distinct credential owners for a provider. The health sweep probes the
-- provider once per user owner (the proxy resolves credentials per user);
-- owner_type/owner_id let the run record which owner was probed.
SELECT DISTINCT owner_type, owner_id
FROM external_provider_credentials
WHERE provider = sqlc.arg(provider)
ORDER BY owner_type, owner_id;
