-- name: CreateIntakePolicyVersion :one
-- Version numbers are per (project, source) and monotonic. Computing MAX+1 in
-- the INSERT keeps it race-free for the single-writer local database; the
-- unique (project_id, source_kind, version) constraint is the backstop.
INSERT INTO intake_policy_versions (project_id, source_kind, version, snapshot_json)
VALUES (?, ?, (SELECT COALESCE(MAX(v.version), 0) + 1 FROM intake_policy_versions v WHERE v.project_id = ? AND v.source_kind = ?), ?)
RETURNING *;

-- name: GetLatestIntakePolicyVersion :one
SELECT * FROM intake_policy_versions
WHERE project_id = ? AND source_kind = ?
ORDER BY version DESC
LIMIT 1;

-- name: GetIntakePolicyVersion :one
SELECT * FROM intake_policy_versions
WHERE project_id = ? AND source_kind = ? AND version = ?;

-- name: ListIntakePolicyVersions :many
SELECT * FROM intake_policy_versions
WHERE project_id = ? AND source_kind = ?
ORDER BY version DESC;
