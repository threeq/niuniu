-- name: ListIntakeRulesByProject :many
SELECT * FROM intake_rules
WHERE project_id = ?
ORDER BY source_kind, priority DESC, id ASC;

-- name: ListEnabledIntakeRules :many
-- Rules evaluated for one source, highest priority first. The policy engine
-- stops at the first matching rule, so order is significant.
SELECT * FROM intake_rules
WHERE project_id = ? AND source_kind = ? AND enabled = 1
ORDER BY priority DESC, id ASC;

-- name: GetIntakeRule :one
SELECT * FROM intake_rules WHERE id = ?;

-- name: CreateIntakeRule :one
INSERT INTO intake_rules (project_id, source_kind, name, instruction, conditions_json, priority, enabled)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateIntakeRule :one
UPDATE intake_rules
SET name = ?, instruction = ?, conditions_json = ?, priority = ?, enabled = ?
WHERE id = ?
RETURNING *;

-- name: DeleteIntakeRule :exec
DELETE FROM intake_rules WHERE id = ?;
