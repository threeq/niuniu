-- name: CreateScheduleRun :one
INSERT INTO schedule_runs (schedule_id, workspace_id, status, message)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: ListScheduleRuns :many
-- before_id is a "no filter" sentinel: 0 means "no upper bound, return latest".
-- The CAST(... AS BIGINT) anchors the parameter's parse-time type so Postgres
-- can unify the two uses (= 0 and id < ?) without SQLSTATE 42P18. SQLite is
-- fine with CAST(x AS BIGINT) because BIGINT collapses to INTEGER affinity.
SELECT * FROM schedule_runs
WHERE schedule_id = sqlc.arg(schedule_id)
  AND (CAST(sqlc.arg(before_id) AS BIGINT) = 0 OR id < sqlc.arg(before_id))
ORDER BY created_at DESC
LIMIT sqlc.arg(limit);
