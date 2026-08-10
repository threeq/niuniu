package service

import (
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// cronDue: due iff the schedule has fired since the newest scheduled run (only
// scheduled runs count). Uses a daily 03:00 cron with fixed timestamps.
func TestCronDue(t *testing.T) {
	sched, err := cron.ParseStandard("0 3 * * *") // every day at 03:00
	require.NoError(t, err)
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.Local) // noon

	require.True(t, cronDue(sched, nil, now), "never run -> due")
	require.True(t, cronDue(sched, []store.MemorySweepRun{{Trigger: "manual", CreatedAt: now.Add(-time.Hour)}}, now),
		"only a manual run -> still due")
	// Last scheduled run at 04:00 today; the next fire (tomorrow 03:00) is after
	// noon -> not due yet.
	require.False(t, cronDue(sched, []store.MemorySweepRun{{Trigger: "schedule", CreatedAt: time.Date(2026, 6, 17, 4, 0, 0, 0, time.Local)}}, now),
		"already run after the last scheduled time -> not due")
	// Last scheduled run two days ago; 03:00 has since elapsed -> due.
	require.True(t, cronDue(sched, []store.MemorySweepRun{{Trigger: "schedule", CreatedAt: now.Add(-48 * time.Hour)}}, now),
		"schedule elapsed since last run -> due")
	// newest-first ordering: the first scheduled row decides.
	runs := []store.MemorySweepRun{
		{Trigger: "manual", CreatedAt: now},
		{Trigger: "schedule", CreatedAt: time.Date(2026, 6, 17, 4, 0, 0, 0, time.Local)},
		{Trigger: "schedule", CreatedAt: now.Add(-30 * 24 * time.Hour)},
	}
	require.False(t, cronDue(sched, runs, now), "newest scheduled run is recent -> not due")
}

// Automatic maintenance is OFF by default (empty schedule); enabling stores a
// concrete cron, disabling clears it. MemoryMaintEnabled tracks that switch.
func TestProjectSweepCron_DefaultOffAndToggle(t *testing.T) {
	svc, q, _, ctx := newEvolveSvc(t)
	pid := newEvolveProject(t, q, ctx)

	got, err := svc.GetProjectSweepCron(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, "", got, "new project defaults OFF (empty schedule)")

	enabled, err := svc.MemoryMaintEnabled(ctx, pid)
	require.NoError(t, err)
	require.False(t, enabled, "disabled by default")

	// Enable with an explicit schedule.
	require.NoError(t, svc.SetProjectSweepCron(ctx, pid, "0 4 * * 2"))
	got, err = svc.GetProjectSweepCron(ctx, pid)
	require.NoError(t, err)
	require.Equal(t, "0 4 * * 2", got)
	enabled, err = svc.MemoryMaintEnabled(ctx, pid)
	require.NoError(t, err)
	require.True(t, enabled, "non-empty schedule -> enabled")

	// Disable by clearing the schedule.
	require.NoError(t, svc.SetProjectSweepCron(ctx, pid, ""))
	enabled, err = svc.MemoryMaintEnabled(ctx, pid)
	require.NoError(t, err)
	require.False(t, enabled, "cleared schedule -> disabled")
}
