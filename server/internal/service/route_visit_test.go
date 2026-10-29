package service

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Regression: the visit-counter upsert wrote `SET visit_count = visit_count + 1`
// with an unqualified right-hand side. PostgreSQL rejects that inside
// ON CONFLICT DO UPDATE as ambiguous (SQLSTATE 42702), while SQLite is
// permissive — so on every PG deployment the counter silently stayed at 0
// (recordRouteVisit logs a warning and returns 0,0) and the column-reentry cap
// that consumes it never tripped. Table-qualifying the existing row fixes PG and
// keeps SQLite working. Runs on both drivers via the shared harness.
func TestRecordRouteVisitUpsertDualDriver(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		db, q := pgtest.SetupDBForDriver(t, drv)
		svc := &EpicExecutionService{db: store.Wrap(db), q: q}
		ctx := context.Background()

		reentry, total := svc.recordRouteVisit(ctx, 42, 7)
		if reentry != 1 || total != 1 {
			t.Fatalf("first visit: reentry=%d total=%d, want 1/1", reentry, total)
		}

		reentry, total = svc.recordRouteVisit(ctx, 42, 7)
		if reentry != 2 || total != 2 {
			t.Fatalf("re-entry: reentry=%d total=%d, want 2/2", reentry, total)
		}

		// A different column on the same issue: its re-entry restarts at 1
		// while the issue's total routing steps keep accumulating.
		reentry, total = svc.recordRouteVisit(ctx, 42, 8)
		if reentry != 1 || total != 3 {
			t.Fatalf("new column: reentry=%d total=%d, want 1/3", reentry, total)
		}
	})
}
