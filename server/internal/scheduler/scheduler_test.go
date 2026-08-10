package scheduler

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// newSchedulerForTest builds an in-memory scheduler with a seeded workspace.
// proxy is nil — OnRateLimited / Register never dereference it.
func newSchedulerForTest(t *testing.T) (*Scheduler, *store.Queries, int64) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	store.Migrate(db)
	q := store.New(db)

	res, err := db.Exec(`INSERT INTO workspaces (path) VALUES ('/tmp/ws')`)
	if err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	wsID, _ := res.LastInsertId()

	s := New(q, nil)
	// Cancel any one-shot timers Register installed so they don't fire after
	// the test (the in-memory DB is closed by then).
	t.Cleanup(func() {
		s.mu.Lock()
		for id, tm := range s.timers {
			tm.Stop()
			delete(s.timers, id)
		}
		s.mu.Unlock()
	})
	return s, q, wsID
}

func TestOnRateLimited_CreatesOneShotSchedule(t *testing.T) {
	s, q, wsID := newSchedulerForTest(t)
	ctx := context.Background()

	reset := time.Now().Add(1 * time.Hour)
	s.OnRateLimited(ctx, wsID, reset.Unix())

	scheds, err := q.ListSchedules(ctx, wsID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(scheds) != 1 {
		t.Fatalf("expected 1 auto-resume schedule, got %d", len(scheds))
	}
	got := scheds[0]
	if got.Name != rateLimitScheduleName {
		t.Errorf("name: want %q, got %q", rateLimitScheduleName, got.Name)
	}
	if got.ScheduleType != "once" {
		t.Errorf("schedule_type: want once, got %q", got.ScheduleType)
	}
	if got.Enabled != 1 {
		t.Errorf("expected enabled schedule, got enabled=%d", got.Enabled)
	}
	if !got.RunAt.Valid {
		t.Fatal("run_at must be set")
	}
	wantRunAt := reset.Add(rateLimitResumeBuffer)
	if diff := got.RunAt.Time.Sub(wantRunAt); diff < -2*time.Second || diff > 2*time.Second {
		t.Errorf("run_at: want ~%v (reset+buffer), got %v", wantRunAt, got.RunAt.Time)
	}
}

func TestOnAutohostWait_CreatesOneShotResumeWithMessage(t *testing.T) {
	s, q, wsID := newSchedulerForTest(t)
	ctx := context.Background()

	runAt := time.Now().Add(5 * time.Minute)
	s.OnAutohostWait(ctx, wsID, runAt.Unix(), "继续推进当前任务")

	scheds, err := q.ListSchedules(ctx, wsID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(scheds) != 1 {
		t.Fatalf("expected 1 autohost-wait schedule, got %d", len(scheds))
	}
	got := scheds[0]
	if got.Name != autohostWaitScheduleName {
		t.Errorf("name: want %q, got %q", autohostWaitScheduleName, got.Name)
	}
	if got.ScheduleType != "once" {
		t.Errorf("schedule_type: want once, got %q", got.ScheduleType)
	}
	if got.DefaultMessage != "继续推进当前任务" {
		t.Errorf("default_message: want the continue prompt, got %q", got.DefaultMessage)
	}
	if !got.RunAt.Valid || got.RunAt.Time.Sub(runAt) < -2*time.Second || got.RunAt.Time.Sub(runAt) > 2*time.Second {
		t.Errorf("run_at: want ~%v, got %v (valid=%v)", runAt, got.RunAt.Time, got.RunAt.Valid)
	}
}

// TestCancelAutoResume_RemovesUnfiredAutoResumeOnly verifies that a user takeover
// deletes the workspace's pending rate-limit and autohost-wait one-shot resumes
// (so a later resume can't double-drive a queue the user already took over) while
// leaving the user's own schedules untouched.
func TestCancelAutoResume_RemovesUnfiredAutoResumeOnly(t *testing.T) {
	s, q, wsID := newSchedulerForTest(t)
	ctx := context.Background()

	// Two auto-resume schedules (the gate-holders) + one user schedule (keep).
	s.OnRateLimited(ctx, wsID, time.Now().Add(1*time.Hour).Unix())
	s.OnAutohostWait(ctx, wsID, time.Now().Add(5*time.Minute).Unix(), "继续")
	if _, err := q.CreateSchedule(ctx, store.CreateScheduleParams{
		WorkspaceID:  wsID,
		Name:         "用户的定时任务",
		ScheduleType: "once",
		RunAt:        sql.NullTime{Time: time.Now().Add(2 * time.Hour), Valid: true},
		ActionKind:   "agent_message",
	}); err != nil {
		t.Fatalf("seed user schedule: %v", err)
	}

	s.CancelAutoResume(ctx, wsID)

	scheds, err := q.ListSchedules(ctx, wsID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(scheds) != 1 {
		t.Fatalf("expected only the user schedule to survive, got %d schedules", len(scheds))
	}
	for _, sc := range scheds {
		if sc.Name == rateLimitScheduleName || sc.Name == autohostWaitScheduleName {
			t.Fatalf("auto-resume schedule %q should have been cancelled", sc.Name)
		}
	}
}

func TestOnAutohostWait_IdempotentWhilePending(t *testing.T) {
	s, q, wsID := newSchedulerForTest(t)
	ctx := context.Background()

	runAt := time.Now().Add(5 * time.Minute)
	s.OnAutohostWait(ctx, wsID, runAt.Unix(), "msg")
	s.OnAutohostWait(ctx, wsID, runAt.Add(1*time.Minute).Unix(), "msg")

	scheds, err := q.ListSchedules(ctx, wsID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(scheds) != 1 {
		t.Fatalf("expected exactly 1 autohost-wait schedule while one is pending, got %d", len(scheds))
	}
}

func TestOnRateLimited_IdempotentWithinWindow(t *testing.T) {
	s, q, wsID := newSchedulerForTest(t)
	ctx := context.Background()

	reset := time.Now().Add(1 * time.Hour)
	// Two rejects in the same window — second must not create a duplicate.
	s.OnRateLimited(ctx, wsID, reset.Unix())
	s.OnRateLimited(ctx, wsID, reset.Add(1*time.Minute).Unix())

	scheds, err := q.ListSchedules(ctx, wsID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(scheds) != 1 {
		t.Fatalf("expected exactly 1 schedule after duplicate rejects, got %d", len(scheds))
	}
}

func TestOnRateLimited_PrunesStaleFiredSchedules(t *testing.T) {
	s, q, wsID := newSchedulerForTest(t)
	ctx := context.Background()

	// Seed a previously-fired auto-resume schedule (an earlier rate-limit
	// window that already resumed). It should be deleted when a new one is
	// created so the schedule list doesn't accumulate spent rows.
	old, err := q.CreateSchedule(ctx, store.CreateScheduleParams{
		WorkspaceID:  wsID,
		Name:         rateLimitScheduleName,
		ScheduleType: "once",
		RunAt:        sql.NullTime{Time: time.Now().Add(-2 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("seed old schedule: %v", err)
	}
	if err := q.MarkScheduleFired(ctx, old.ID); err != nil {
		t.Fatalf("mark fired: %v", err)
	}

	s.OnRateLimited(ctx, wsID, time.Now().Add(1*time.Hour).Unix())

	scheds, err := q.ListSchedules(ctx, wsID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(scheds) != 1 {
		t.Fatalf("expected 1 schedule (old pruned, new created), got %d", len(scheds))
	}
	if scheds[0].ID == old.ID {
		t.Fatalf("stale fired schedule %d was not pruned", old.ID)
	}
	if scheds[0].FiredAt.Valid {
		t.Errorf("the surviving schedule should be the fresh unfired one")
	}
}

// A non-auto-resume schedule (user-created, same workspace) must never be
// touched by the housekeeping prune.
func TestOnRateLimited_LeavesUserSchedulesAlone(t *testing.T) {
	s, q, wsID := newSchedulerForTest(t)
	ctx := context.Background()

	user, err := q.CreateSchedule(ctx, store.CreateScheduleParams{
		WorkspaceID:    wsID,
		Name:           "user nightly",
		DefaultMessage: "go",
		ScheduleType:   "cron",
		CronExpr:       "0 0 * * *",
	})
	if err != nil {
		t.Fatalf("seed user schedule: %v", err)
	}

	s.OnRateLimited(ctx, wsID, time.Now().Add(1*time.Hour).Unix())

	scheds, err := q.ListSchedules(ctx, wsID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(scheds) != 2 {
		t.Fatalf("expected 2 schedules (user + auto-resume), got %d", len(scheds))
	}
	found := false
	for _, sc := range scheds {
		if sc.ID == user.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("user schedule was deleted by auto-resume housekeeping")
	}
}

func TestOnRateLimited_PastResetNoSchedule(t *testing.T) {
	s, q, wsID := newSchedulerForTest(t)
	ctx := context.Background()

	// Reset already elapsed — buffer keeps runAt in the past, so nothing fires.
	s.OnRateLimited(ctx, wsID, time.Now().Add(-1*time.Hour).Unix())

	scheds, err := q.ListSchedules(ctx, wsID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(scheds) != 0 {
		t.Fatalf("expected no schedule for past reset, got %d", len(scheds))
	}
}

func TestNormalizeActionKindNotApplicable(t *testing.T) {
	// normalizeActionKind lives in the api package; here we assert the
	// scheduler's branch key is the agreed constant value.
	if discoveryReportRelPath(time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)) != ".niuniu/reports/2026-05-31-discovery.md" {
		t.Fatalf("unexpected discovery report path: %s", discoveryReportRelPath(time.Now()))
	}
}

func TestBuildDiscoveryPrompt(t *testing.T) {
	p := buildDiscoveryPrompt("", ".niuniu/reports/2026-05-31-discovery.md")
	if !strings.Contains(p, "自主发现") {
		t.Errorf("prompt missing discovery header: %s", p)
	}
	if !strings.Contains(p, ".niuniu/reports/2026-05-31-discovery.md") {
		t.Errorf("prompt missing report path")
	}
	// Extra focus is woven in when present.
	withExtra := buildDiscoveryPrompt("focus on the auth module", "r.md")
	if !strings.Contains(withExtra, "focus on the auth module") {
		t.Errorf("prompt did not include extra focus")
	}
	// Empty extra must not add the "extra focus" section.
	if strings.Contains(p, "额外关注") {
		t.Errorf("empty extra should not add focus section")
	}
}

func TestAppendScheduleLogWritesFile(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	sched := store.WorkspaceSchedule{ID: 5, Name: "nightly", ScheduleType: "cron", ActionKind: "autonomous_discovery"}

	appendScheduleLog(dir, sched, "triggered", "ran a discovery pass\nwith a newline", log)
	appendScheduleLog(dir, sched, "triggered", "second run", log)

	logPath := filepath.Join(dir, ".niuniu", "reports", "schedule-log.md")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("schedule-log.md not written: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "nightly") || !strings.Contains(content, "autonomous_discovery") {
		t.Errorf("log missing schedule metadata: %s", content)
	}
	if strings.Count(content, "triggered") != 2 {
		t.Errorf("expected 2 appended lines, got: %s", content)
	}
	// Newlines in the message must be flattened to keep one line per run.
	if strings.Contains(content, "ran a discovery pass\nwith") {
		t.Errorf("message newline was not flattened")
	}
}

func TestAppendScheduleLogEmptyPathNoop(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	// Must not panic or create anything when wsPath is empty.
	appendScheduleLog("", store.WorkspaceSchedule{ID: 1}, "triggered", "x", log)
}
