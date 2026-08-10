package store

import (
	"context"
	"testing"
)

// TestGetLatestBatchTaskStatsForWorkspaces exercises the window-function batch
// query that replaced the per-workspace GetLatestBatchId + GetLatestBatchTaskStats
// + GetInProgressTaskActiveForm trio in the sidebar. It verifies latest-batch
// selection, deleted-task exclusion from totals, completed counting, and the
// in-progress active_form projection.
func TestGetLatestBatchTaskStatsForWorkspaces(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	q := New(db)
	ctx := context.Background()

	exec := func(sqlStr string, args ...any) {
		t.Helper()
		if _, err := db.Exec(sqlStr, args...); err != nil {
			t.Fatalf("exec %q: %v", sqlStr, err)
		}
	}

	// Parent workspaces (ids 1,2,3 via AUTOINCREMENT insert order).
	for i := 0; i < 3; i++ {
		exec("INSERT INTO workspaces (path) VALUES (?)", "/tmp/ws")
	}

	ins := `INSERT INTO workspace_tasks (workspace_id, agent_task_id, subject, active_form, status, batch_id, created_at) VALUES (?,?,?,?,?,?,?)`
	// ws1: older batch b1 (ignored) then latest batch b2 with
	// 3 non-deleted tasks (1 completed, 1 in_progress, 1 pending) + 1 deleted.
	exec(ins, 1, "t0", "s0", "af0", "completed", "b1", "2026-01-01 00:00:00")
	exec(ins, 1, "t1", "s1", "", "completed", "b2", "2026-01-02 00:00:00")
	exec(ins, 1, "t2", "s2", "doing X", "in_progress", "b2", "2026-01-02 00:00:01")
	exec(ins, 1, "t3", "s3", "", "pending", "b2", "2026-01-02 00:00:02")
	exec(ins, 1, "t4", "s4", "", "deleted", "b2", "2026-01-02 00:00:03")
	// ws2: single batch b3, 2 completed tasks, no in_progress.
	exec(ins, 2, "t5", "s5", "", "completed", "b3", "2026-01-03 00:00:00")
	exec(ins, 2, "t6", "s6", "", "completed", "b3", "2026-01-03 00:00:01")
	// ws3: no tasks -> must be absent from the result set.

	rows, err := q.GetLatestBatchTaskStatsForWorkspaces(ctx, []int64{1, 2, 3})
	if err != nil {
		t.Fatalf("GetLatestBatchTaskStatsForWorkspaces: %v", err)
	}
	byWs := make(map[int64]GetLatestBatchTaskStatsForWorkspacesRow, len(rows))
	for _, r := range rows {
		byWs[r.WorkspaceID] = r
	}

	activeForm := func(v interface{}) string {
		switch s := v.(type) {
		case string:
			return s
		case []byte:
			return string(s)
		default:
			return ""
		}
	}

	w1, ok := byWs[1]
	if !ok {
		t.Fatal("ws1 missing from results")
	}
	if w1.Total != 3 {
		t.Errorf("ws1 total = %d, want 3 (deleted excluded)", w1.Total)
	}
	if !w1.Completed.Valid || w1.Completed.Float64 != 1 {
		t.Errorf("ws1 completed = %v, want 1", w1.Completed)
	}
	if af := activeForm(w1.ActiveForm); af != "doing X" {
		t.Errorf("ws1 active_form = %q, want \"doing X\"", af)
	}

	w2, ok := byWs[2]
	if !ok {
		t.Fatal("ws2 missing from results")
	}
	if w2.Total != 2 {
		t.Errorf("ws2 total = %d, want 2", w2.Total)
	}
	if !w2.Completed.Valid || w2.Completed.Float64 != 2 {
		t.Errorf("ws2 completed = %v, want 2", w2.Completed)
	}
	if af := activeForm(w2.ActiveForm); af != "" {
		t.Errorf("ws2 active_form = %q, want empty", af)
	}

	if _, ok := byWs[3]; ok {
		t.Error("ws3 (no tasks) should be absent from results")
	}
}
