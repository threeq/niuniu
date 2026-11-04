package store

import "context"

// ReserveIntakeDailySlotParams claims one daily inferred-intake slot.
type ReserveIntakeDailySlotParams struct {
	ProjectID  int64
	SourceKind string
	Day        string // calendar day in the trigger's timezone, YYYY-MM-DD
	Cap        int64
}

// ReserveIntakeDailySlot atomically claims one slot for (project, source, day).
//
// It is one statement on purpose: a separate "count then insert" leaves a
// window where concurrent inferred events all read used < cap and all execute,
// overshooting daily_cap. The upsert's DO UPDATE ... WHERE fails the update once
// the cap is reached, and the zero affected-row count is the "cap exhausted"
// signal. The INSERT arm always wins the first slot because callers only reach
// here with Cap > 0.
func (q *Queries) ReserveIntakeDailySlot(ctx context.Context, arg ReserveIntakeDailySlotParams) (bool, error) {
	const query = `
INSERT INTO intake_daily_usage (project_id, source_kind, day, used)
VALUES (?, ?, ?, 1)
ON CONFLICT (project_id, source_kind, day)
DO UPDATE SET used = intake_daily_usage.used + 1
WHERE intake_daily_usage.used < ?`
	res, err := q.db.ExecContext(ctx, query, arg.ProjectID, arg.SourceKind, arg.Day, arg.Cap)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ReleaseIntakeDailySlot returns a slot claimed by ReserveIntakeDailySlot when
// the pipeline failed before anything was executed.
func (q *Queries) ReleaseIntakeDailySlot(ctx context.Context, projectID int64, sourceKind, day string) error {
	_, err := q.db.ExecContext(ctx,
		`UPDATE intake_daily_usage
		 SET used = CASE WHEN used > 0 THEN used - 1 ELSE 0 END
		 WHERE project_id = ? AND source_kind = ? AND day = ?`,
		projectID, sourceKind, day)
	return err
}
