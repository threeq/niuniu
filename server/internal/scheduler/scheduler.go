package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// HarnessGate is the scheduler's hook into the harness subsystem. Implemented
// by service.HarnessService; injected via SetHarnessGate so the scheduler
// package does not import service (which would create a cycle).
//
// RunForWorkspace evaluates every enabled global spec with the given trigger
// against the workspace. Results are persisted to harness_checks; blocked=true
// when any error-severity spec failed.
type HarnessGate interface {
	RunForWorkspace(ctx context.Context, workspaceID int64, triggerOn string) (results []harness.GateResult, blocked bool, err error)
}

// Scheduler manages cron-based and one-time scheduled tasks for workspaces.
type Scheduler struct {
	cron        *cron.Cron
	q           *store.Queries
	db          *sql.DB // raw DB for queries not covered by sqlc (e.g. triggered_by checks)
	proxy       *agentproxy.AgentProxy
	notifyHub   *notify.NotificationHub
	harnessGate HarnessGate            // optional; on_schedule pre-flight gate
	allowRun    func() bool            // license gate; nil = always allowed
	entries     map[int64]cron.EntryID // scheduleID → cron entry
	timers      map[int64]*time.Timer  // scheduleID → one-shot timer
	mu          sync.Mutex
}

// New creates a new Scheduler.
func New(q *store.Queries, proxy *agentproxy.AgentProxy) *Scheduler {
	return &Scheduler{
		cron:    cron.New(),
		q:       q,
		proxy:   proxy,
		entries: make(map[int64]cron.EntryID),
		timers:  make(map[int64]*time.Timer),
	}
}

// SetDB wires the raw DB connection for triggered_by membership checks (spec §5.11).
func (s *Scheduler) SetDB(db *sql.DB) {
	s.db = db
}

// Start begins the cron scheduler.
func (s *Scheduler) Start() {
	s.cron.Start()
	slog.Info("scheduler: started")
}

// Stop gracefully stops the cron scheduler and cancels all pending timers.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
	s.mu.Lock()
	for id, t := range s.timers {
		t.Stop()
		delete(s.timers, id)
	}
	s.mu.Unlock()
	slog.Info("scheduler: stopped")
}

// RecoverOnStartup loads all enabled schedules and registers them.
// For "once" schedules with a past run_at and no fired_at, it triggers immediately.
func (s *Scheduler) RecoverOnStartup(ctx context.Context) error {
	schedules, err := s.q.ListEnabledSchedules(ctx)
	if err != nil {
		return fmt.Errorf("scheduler: list enabled schedules: %w", err)
	}

	// Track past-due once schedules to avoid double-registration
	pastDueOnce := make(map[int64]bool)
	unfired, _ := s.q.ListUnfiredOnceSchedules(ctx)
	for _, sched := range unfired {
		if sched.RunAt.Valid && sched.RunAt.Time.Before(time.Now()) {
			pastDueOnce[sched.ID] = true
		}
	}

	for _, sched := range schedules {
		// Skip past-due once schedules — they'll be triggered directly below
		if pastDueOnce[sched.ID] {
			continue
		}
		if err := s.Register(sched); err != nil {
			slog.Error("scheduler: recover register failed",
				"scheduleID", sched.ID, "err", err)
		}
	}

	// Trigger past-due unfired once schedules immediately
	for _, sched := range unfired {
		if pastDueOnce[sched.ID] {
			slog.Info("scheduler: triggering past-due once schedule immediately",
				"scheduleID", sched.ID, "workspaceID", sched.WorkspaceID)
			go s.trigger(sched.ID, sched.WorkspaceID)
		}
	}

	slog.Info("scheduler: recovery complete",
		"total", len(schedules), "pastDueOnce", len(unfired))
	return nil
}

// Register adds or re-registers a schedule. For "cron" type it uses cron.AddFunc.
// For "once" type it uses time.AfterFunc.
func (s *Scheduler) Register(sched store.WorkspaceSchedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove existing entry if re-registering.
	s.unregisterLocked(sched.ID)

	if sched.ScheduleType == "cron" {
		entryID, err := s.cron.AddFunc(sched.CronExpr, func() {
			s.trigger(sched.ID, sched.WorkspaceID)
		})
		if err != nil {
			return fmt.Errorf("scheduler: add cron func for schedule %d: %w", sched.ID, err)
		}
		s.entries[sched.ID] = entryID
		slog.Info("scheduler: registered cron schedule",
			"scheduleID", sched.ID, "expr", sched.CronExpr, "workspaceID", sched.WorkspaceID)
	} else if sched.ScheduleType == "once" {
		if !sched.RunAt.Valid {
			return fmt.Errorf("scheduler: once schedule %d has no run_at", sched.ID)
		}
		// Already fired — skip.
		if sched.FiredAt.Valid {
			return nil
		}
		delay := time.Until(sched.RunAt.Time)
		if delay < 0 {
			delay = 0
		}
		schedID := sched.ID
		wsID := sched.WorkspaceID
		timer := time.AfterFunc(delay, func() {
			s.trigger(schedID, wsID)
		})
		s.timers[sched.ID] = timer
		slog.Info("scheduler: registered once schedule",
			"scheduleID", sched.ID, "runAt", sched.RunAt.Time, "delay", delay,
			"workspaceID", sched.WorkspaceID)
	}

	return nil
}

// Unregister removes a schedule from the cron scheduler.
func (s *Scheduler) Unregister(scheduleID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unregisterLocked(scheduleID)
}

// unregisterLocked removes a schedule entry. Caller must hold s.mu.
func (s *Scheduler) unregisterLocked(scheduleID int64) {
	if entryID, ok := s.entries[scheduleID]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, scheduleID)
		slog.Info("scheduler: unregistered cron entry", "scheduleID", scheduleID)
	}
	if timer, ok := s.timers[scheduleID]; ok {
		timer.Stop()
		delete(s.timers, scheduleID)
		slog.Info("scheduler: cancelled once timer", "scheduleID", scheduleID)
	}
}

// OnScheduleChanged is a callback for handlers — it re-registers or unregisters
// a schedule based on its current state. After applying the registration
// change, it emits a workspace_bg_task notify (Task 6) so the sidebar's
// bg_tasks.cron_count stays accurate.
func (s *Scheduler) OnScheduleChanged(ctx context.Context, scheduleID int64, deleted bool) {
	if deleted {
		// Look up workspace_id BEFORE Unregister so we can emit the notify
		// with correct workspace scope. If the row's already gone (delete-then-
		// event race), best-effort: still call Unregister and skip emit.
		var workspaceID int64
		if sched, err := s.q.GetSchedule(ctx, scheduleID); err == nil {
			workspaceID = sched.WorkspaceID
		}
		s.Unregister(scheduleID)
		if workspaceID != 0 {
			s.emitBgTaskNotify(ctx, workspaceID)
		}
		return
	}

	sched, err := s.q.GetSchedule(ctx, scheduleID)
	if err != nil {
		slog.Error("scheduler: get schedule on change", "scheduleID", scheduleID, "err", err)
		return
	}

	if sched.Enabled == 0 {
		s.Unregister(scheduleID)
		s.emitBgTaskNotify(ctx, sched.WorkspaceID)
		return
	}

	if err := s.Register(sched); err != nil {
		slog.Error("scheduler: re-register on change", "scheduleID", scheduleID, "err", err)
		return
	}
	s.emitBgTaskNotify(ctx, sched.WorkspaceID)
}

// TriggerNow immediately fires a schedule (for manual trigger endpoint).
func (s *Scheduler) TriggerNow(ctx context.Context, scheduleID int64) error {
	sched, err := s.q.GetSchedule(ctx, scheduleID)
	if err != nil {
		return fmt.Errorf("schedule not found: %w", err)
	}
	go s.trigger(sched.ID, sched.WorkspaceID)
	return nil
}

// SetHarnessGate installs an optional pre-flight gate evaluated before each
// scheduled trigger fires. The scheduler does not import service directly; the
// caller wires service.HarnessService here. Nil disables the gate.
func (s *Scheduler) SetHarnessGate(gate HarnessGate) {
	s.harnessGate = gate
}

// SetLicenseGate injects the license run gate. When it returns false, scheduled
// triggers are skipped (recorded as "skipped") instead of firing. nil (the
// default) means always-allowed, so tests and personal mode are unaffected.
func (s *Scheduler) SetLicenseGate(f func() bool) { s.allowRun = f }

// SetNotifyHub sets the notification hub for broadcasting.
func (s *Scheduler) SetNotifyHub(hub *notify.NotificationHub) {
	s.notifyHub = hub
}

// recordRun writes a schedule_runs record. Best-effort — logs on failure.
// actorUserID 0 means unknown/system; it is stored as NULL in actor_user_id.
func (s *Scheduler) recordRun(scheduleID, workspaceID int64, status, message string, actorUserID int64) {
	ctx := context.Background()
	if s.db != nil {
		// Use raw SQL to stamp actor_user_id (column added via addOwnerModel migration).
		var nullActor interface{}
		if actorUserID != 0 {
			nullActor = actorUserID
		}
		_, err := s.db.ExecContext(ctx, store.ConvertPlaceholders(
			`INSERT INTO schedule_runs (schedule_id, workspace_id, status, message, actor_user_id)
			 VALUES (?, ?, ?, ?, ?)`),
			scheduleID, workspaceID, status, message, nullActor)
		if err != nil {
			slog.Error("scheduler: failed to record run",
				"scheduleID", scheduleID, "status", status, "err", err)
		}
		return
	}
	_, err := s.q.CreateScheduleRun(ctx, store.CreateScheduleRunParams{
		ScheduleID:  scheduleID,
		WorkspaceID: workspaceID,
		Status:      status,
		Message:     message,
	})
	if err != nil {
		slog.Error("scheduler: failed to record run",
			"scheduleID", scheduleID, "status", status, "err", err)
	}
}

// trigger is the core function called when a schedule fires.
func (s *Scheduler) trigger(scheduleID, workspaceID int64) {
	ctx := context.Background()
	log := slog.With("scheduleID", scheduleID, "workspaceID", workspaceID)

	hub := s.proxy.GetHub()

	// Read triggered_by early so it can be stamped on all schedule_run rows.
	var triggeredBy int64
	if s.db != nil {
		_ = s.db.QueryRowContext(ctx,
			`SELECT triggered_by FROM workspace_schedules WHERE id = ?`, scheduleID).Scan(&triggeredBy)
	}

	// Check session status — if running, skip.
	session := s.proxy.GetSession(workspaceID)
	if session != nil && session.Status() == agentproxy.StatusRunning {
		log.Info("scheduler: skipping trigger — session is running")
		s.recordRun(scheduleID, workspaceID, "skipped", "session is running", triggeredBy)
		return
	}

	if s.allowRun != nil && !s.allowRun() {
		log.Info("scheduler: skipping trigger — license locked")
		s.recordRun(scheduleID, workspaceID, "skipped", "license locked", triggeredBy)
		return
	}

	// Reload schedule to get current type.
	sched, err := s.q.GetSchedule(ctx, scheduleID)
	if err != nil {
		log.Error("scheduler: get schedule for trigger", "err", err)
		s.recordRun(scheduleID, workspaceID, "failed", "schedule not found: "+err.Error(), triggeredBy)
		return
	}

	// #243: autonomous-discovery schedules inject a self-directed survey+report
	// prompt on idle ticks instead of skipping when no message is queued. This
	// stays strictly per-workspace (no global background sweep — forbidden by
	// docs/architecture/workspace-model.md): it is still one workspace_schedules
	// row firing against its own workspace.
	autonomousDiscovery := sched.ActionKind == "autonomous_discovery"

	// Spec §5.11: verify the triggering user is still a member of the workspace
	// owner's org. If not, disable the schedule and write an audit entry.
	if s.db != nil {
		if disabled := s.checkAndMaybeDisableSchedule(ctx, log, scheduleID, sched, workspaceID); disabled {
			s.emitBgTaskNotify(ctx, workspaceID)
			return
		}
	}

	// Pre-flight harness gate (per workspace-model architecture: on_schedule
	// specs fire from the scheduler against the workspace's project). Runs
	// BEFORE dequeue so a blocking failure does not consume a queued message.
	if s.harnessGate != nil {
		results, blocked, gErr := s.harnessGate.RunForWorkspace(ctx, workspaceID, "on_schedule")
		if gErr != nil {
			log.Warn("scheduler: on_schedule harness gate failed to run", "err", gErr)
			// Soft fail: do not block the schedule on harness infrastructure errors.
		} else if blocked {
			msg := "blocked by on_schedule harness gate"
			for _, r := range results {
				if r.Status == "fail" {
					msg = fmt.Sprintf("blocked: spec %d %s", r.SpecID, truncateMessage(r.Message, 120))
					break
				}
			}
			log.Info("scheduler: on_schedule trigger blocked by harness gate", "results", len(results))
			s.recordRun(scheduleID, workspaceID, "blocked", msg, triggeredBy)
			return
		}
	}

	// Atomic dequeue — if empty, fall back to default_message.
	msg, err := s.q.DequeueMessage(ctx, workspaceID)
	var messageContent string
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Error("scheduler: dequeue message", "err", err)
			s.recordRun(scheduleID, workspaceID, "failed", "dequeue error: "+err.Error(), triggeredBy)
			return
		}
		// Queue is empty — try default_message. Autonomous-discovery schedules
		// do not need a default message: they synthesize one below.
		if sched.DefaultMessage == "" && !autonomousDiscovery {
			log.Info("scheduler: skipping trigger — no queued messages and no default message")
			s.recordRun(scheduleID, workspaceID, "skipped", "no queued messages", triggeredBy)
			return
		}
		messageContent = sched.DefaultMessage
		log.Info("scheduler: using default message", "content", sched.DefaultMessage)
	} else {
		messageContent = msg.Content
	}

	// For "once": mark fired_at AFTER successful dequeue but BEFORE session start.
	if sched.ScheduleType == "once" {
		if err := s.q.MarkScheduleFired(ctx, scheduleID); err != nil {
			log.Error("scheduler: mark schedule fired — aborting trigger", "err", err)
			s.recordRun(scheduleID, workspaceID, "failed", "mark fired failed: "+err.Error(), triggeredBy)
			return
		}
	}

	// Publish SSE events via SessionHub (reaches frontend /ws/sse)
	label := sched.Name
	if label == "" {
		label = fmt.Sprintf("schedule %d", scheduleID)
	}
	hub.Broadcast(workspaceID, event.NewOutputEvent(event.EventQueueUpdate, "", "", "system", workspaceID))
	hub.Broadcast(workspaceID, event.NewOutputEvent(event.EventScheduleTrigger, label, "", "system", workspaceID))

	// Broadcast via NotificationHub for WebSocket push
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicQueue, Action: "changed", ID: workspaceID})
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicSchedule, Action: "triggered", ID: workspaceID})
	}

	// Get workspace for workDir.
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		log.Error("scheduler: get workspace", "err", err)
		s.recordRun(scheduleID, workspaceID, "failed", "get workspace failed: "+err.Error(), triggeredBy)
		return
	}

	// #243: for autonomous discovery, wrap whatever message we have (queued,
	// default, or empty) in a self-directed survey+report prompt that tells the
	// agent to write its findings to an on-disk report under the workspace.
	if autonomousDiscovery {
		reportRel := discoveryReportRelPath(time.Now())
		ensureReportsDir(ws.Path, log)
		messageContent = buildDiscoveryPrompt(messageContent, reportRel)
	}

	queued, _, err := s.proxy.Deliver(ctx, workspaceID, ws.Path, messageContent, "")
	if err != nil {
		log.Error("scheduler: deliver message", "err", err)
		s.recordRun(scheduleID, workspaceID, "failed", "deliver failed: "+err.Error(), triggeredBy)
		return
	}

	log.Info("scheduler: trigger executed", "queued", queued, "content", messageContent)

	// Update last_run_at.
	if err := s.q.MarkScheduleRan(ctx, scheduleID); err != nil {
		log.Error("scheduler: mark schedule ran", "err", err)
	}

	s.recordRun(scheduleID, workspaceID, "triggered", "", triggeredBy)

	// #243: persist a human-readable run summary to disk under the workspace so
	// progress survives disconnects and is reviewable without the DB.
	appendScheduleLog(ws.Path, sched, "triggered", messageContent, log)

	// Disable "once" schedules after execution.
	if sched.ScheduleType == "once" {
		if err := s.q.DisableSchedule(ctx, scheduleID); err != nil {
			log.Error("scheduler: disable once schedule", "err", err)
		}
		s.Unregister(scheduleID)
	}
}

// checkAndMaybeDisableSchedule verifies that the schedule's triggered_by user is
// still a member of the workspace owner's org (spec §5.11). Returns true (disabled)
// and skips execution if the user is no longer a member.
//
// triggered_by == 0 means the schedule was created before this check was added;
// such schedules are allowed to fire without a membership check.
func (s *Scheduler) checkAndMaybeDisableSchedule(ctx context.Context, log *slog.Logger, scheduleID int64, sched store.WorkspaceSchedule, workspaceID int64) (disabled bool) {
	// Read triggered_by from the DB directly (not in the sqlc model yet).
	var triggeredBy int64
	err := s.db.QueryRowContext(ctx,
		`SELECT triggered_by FROM workspace_schedules WHERE id = ?`, scheduleID).Scan(&triggeredBy)
	if err != nil {
		// Column may not exist on very old DBs (migration pending); skip check.
		log.Debug("scheduler: triggered_by read failed; skipping membership check", "err", err)
		return false
	}
	if triggeredBy == 0 {
		// No user associated — created before this feature was added; allow.
		return false
	}

	// Look up workspace owner.
	var ownerType string
	var ownerID int64
	err = s.db.QueryRowContext(ctx,
		`SELECT owner_type, owner_id FROM workspaces WHERE id = ?`, workspaceID).Scan(&ownerType, &ownerID)
	if err != nil {
		log.Warn("scheduler: cannot load workspace owner for membership check", "err", err)
		return false
	}

	// Check membership.
	isMember := true
	switch ownerType {
	case "org":
		var dummy int64
		err = s.db.QueryRowContext(ctx,
			`SELECT 1 FROM org_members WHERE org_id = ? AND user_id = ?`, ownerID, triggeredBy).Scan(&dummy)
		if err == sql.ErrNoRows {
			isMember = false
		} else if err != nil {
			log.Warn("scheduler: membership check query failed", "err", err)
			return false
		}
	case "user":
		// Personal workspace — check that the user still exists.
		var dummy int64
		err = s.db.QueryRowContext(ctx,
			`SELECT 1 FROM users WHERE id = ?`, triggeredBy).Scan(&dummy)
		if err == sql.ErrNoRows {
			isMember = false
		} else if err != nil {
			log.Warn("scheduler: user existence check failed", "err", err)
			return false
		}
	}

	if isMember {
		return false
	}

	log.Info("scheduler: disabling schedule — triggering user no longer member",
		"scheduleID", scheduleID, "triggeredBy", triggeredBy, "ownerType", ownerType, "ownerID", ownerID)

	// Disable the schedule.
	if err := s.q.DisableSchedule(ctx, scheduleID); err != nil {
		log.Error("scheduler: disable orphaned schedule", "err", err)
	}
	s.Unregister(scheduleID)

	// Write audit entry (only for org owners; personal workspaces have no audit log).
	if ownerType == "org" {
		payload := fmt.Sprintf(`{"scheduleId":%d,"triggeredBy":%d,"reason":"triggering user no longer member"}`,
			scheduleID, triggeredBy)
		_, _ = s.db.ExecContext(ctx, store.ConvertPlaceholders(
			`INSERT INTO org_audit_log (org_id, actor_user_id, actor_label, action, target_type, target_id, payload)
			 VALUES (?, NULL, 'scheduler', 'schedule.disabled', 'schedule', ?, ?)`),
			ownerID, scheduleID, payload)
	}

	s.recordRun(scheduleID, workspaceID, "skipped", "triggering user no longer member of workspace owner org", triggeredBy)
	return true
}

// emitBgTaskNotify broadcasts a workspace_bg_task signal scoped to the workspace
// whose schedule state changed. Owner metadata is read from the workspace row
// so the notify hub fan-out filter respects multi-tenant isolation. Skip emit
// rather than broadcast cross-tenant on lookup failure.
func (s *Scheduler) emitBgTaskNotify(ctx context.Context, workspaceID int64) {
	if s.notifyHub == nil || workspaceID == 0 {
		return
	}
	w, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		slog.Warn("scheduler: emitBgTaskNotify GetWorkspace failed",
			"workspace_id", workspaceID, "err", err)
		return
	}
	if w.OwnerType == "" {
		slog.Warn("scheduler: emitBgTaskNotify skipping — empty owner_type",
			"workspace_id", workspaceID)
		return
	}
	s.notifyHub.Broadcast(notify.Notification{
		Topic:     notify.TopicWorkspaceBgTask,
		Action:    "changed",
		ID:        workspaceID,
		OwnerType: w.OwnerType,
		OwnerID:   w.OwnerID,
	})
}

// truncateMessage returns s clipped to max bytes with an ellipsis suffix on
// overflow. Used to keep schedule_runs.message rows compact when surfacing
// pre-flight harness gate failures.
func truncateMessage(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// rateLimitScheduleName marks the one-shot schedules the scheduler creates
// itself in response to a rate-limit rejection. Used both as the display name
// and as the idempotency key so repeated rejects in the same window don't pile
// up duplicate auto-resume schedules.
const rateLimitScheduleName = "限流自动续跑"

// rateLimitResumeBuffer is added to the upstream reset timestamp before firing
// so the limit has definitely lifted (clock skew + propagation slack) by the
// time we re-send.
const rateLimitResumeBuffer = 30 * time.Second

// OnRateLimited implements agentproxy.RateLimitScheduler. When a turn is
// rejected by the upstream rate limiter, it creates a one-shot schedule at the
// window's reset time so the workspace auto-resumes — dequeuing and sending
// whatever is pending (including the rolled-back rejected message) without the
// user having to come back and click. Idempotent per (workspace, reset window):
// a second reject in the same window finds the pending auto-resume schedule and
// skips. Best-effort throughout; failures only log.
//
// The caller's ctx (the agent read-loop / turn context) is intentionally NOT
// used for the DB writes: a rate-limit rejection often ends the turn, which can
// cancel that ctx before the schedule is committed. This is a detached
// fire-and-forget side effect, so it runs on context.Background() — same
// rationale as trigger(). The ctx parameter is kept only to satisfy the
// agentproxy.RateLimitScheduler interface.
func (s *Scheduler) OnRateLimited(_ context.Context, workspaceID int64, resetsAtUnix int64) {
	ctx := context.Background()
	resetAt := time.Unix(resetsAtUnix, 0)
	runAt := resetAt.Add(rateLimitResumeBuffer)
	log := slog.With("workspaceID", workspaceID, "resetAt", resetAt)

	// Reset already in the past (stale event) — nothing to schedule; the next
	// manual or queued send will go straight through.
	if runAt.Before(time.Now()) {
		log.Info("scheduler: rate-limit reset already passed; not scheduling auto-resume")
		return
	}

	// Idempotency + housekeeping in one pass over the workspace's schedules:
	//   - if an UNFIRED auto-resume schedule already targets roughly the same
	//     window (within 5 min), skip — a second reject in the same window must
	//     not pile up a duplicate.
	//   - collect already-FIRED auto-resume schedules so we can delete them
	//     after creating the fresh one, keeping the workspace schedule list from
	//     accumulating spent auto-resume rows over many rate-limit windows.
	// Best-effort — on list failure we fall through and create, accepting a
	// possible duplicate over a miss.
	var staleFired []int64
	if existing, err := s.q.ListSchedules(ctx, workspaceID); err != nil {
		log.Warn("scheduler: OnRateLimited list schedules failed", "err", err)
	} else {
		for _, e := range existing {
			if e.Name != rateLimitScheduleName || e.ScheduleType != "once" {
				continue
			}
			if e.FiredAt.Valid {
				staleFired = append(staleFired, e.ID)
				continue
			}
			if e.Enabled == 1 && e.RunAt.Valid {
				diff := e.RunAt.Time.Sub(runAt)
				if diff < 0 {
					diff = -diff
				}
				if diff < 5*time.Minute {
					log.Info("scheduler: auto-resume schedule already pending; skipping",
						"scheduleID", e.ID)
					return
				}
			}
		}
	}

	sched, err := s.q.CreateSchedule(ctx, store.CreateScheduleParams{
		WorkspaceID:    workspaceID,
		Name:           rateLimitScheduleName,
		DefaultMessage: "", // rely on queued messages; empty queue → trigger no-ops
		ScheduleType:   "once",
		CronExpr:       "",
		RunAt:          sql.NullTime{Time: runAt, Valid: true},
		ActionKind:     "agent_message", // #243: send-message kind (not autonomous_discovery)
	})
	if err != nil {
		log.Error("scheduler: OnRateLimited create schedule failed", "err", err)
		return
	}
	if err := s.Register(sched); err != nil {
		log.Error("scheduler: OnRateLimited register failed", "scheduleID", sched.ID, "err", err)
		return
	}

	// Housekeeping: drop spent auto-resume rows. They're already fired (timer
	// gone), so a bare DB delete is enough — no Unregister needed.
	for _, id := range staleFired {
		if err := s.q.DeleteSchedule(ctx, id); err != nil {
			log.Warn("scheduler: OnRateLimited delete stale auto-resume failed", "scheduleID", id, "err", err)
		}
	}

	s.emitBgTaskNotify(ctx, workspaceID)
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicSchedule, Action: "changed", ID: workspaceID})
	}
	log.Info("scheduler: auto-resume schedule created", "scheduleID", sched.ID, "runAt", runAt,
		"prunedFired", len(staleFired))
}

// autohostWaitScheduleName marks the one-shot schedules the autohost watchdog
// asks the scheduler to create when it stops continuing to WAIT for in-flight
// background work (a running build / subagent / a wakeup the agent set). Used as
// display name and idempotency key.
const autohostWaitScheduleName = "后台任务自动续跑"

// OnAutohostWait implements agentproxy.RateLimitScheduler. The autohost watchdog
// calls it when, at continue-budget exhaustion, the workspace still has pending
// background work: instead of busy-continuing (which would bypass the budget and
// loop forever), it stops and schedules a one-shot resume at runAtUnix that sends
// `message` (the autohost continue prompt) so the agent re-checks and the judge
// re-evaluates. Idempotent: a single unfired autohost-wait schedule per workspace
// at a time; repeated calls while one is pending are skipped. Detached on
// context.Background() like OnRateLimited (the turn ctx is often already
// cancelled when the watchdog decides to stop). Best-effort; failures only log.
func (s *Scheduler) OnAutohostWait(_ context.Context, workspaceID int64, runAtUnix int64, message string) bool {
	ctx := context.Background()
	runAt := time.Unix(runAtUnix, 0)
	log := slog.With("workspaceID", workspaceID, "runAt", runAt)

	if runAt.Before(time.Now()) {
		runAt = time.Now().Add(5 * time.Second)
	}

	// Idempotency + housekeeping: at most one unfired autohost-wait schedule per
	// workspace; prune already-fired ones. Best-effort — on list failure fall
	// through and create.
	var staleFired []int64
	if existing, err := s.q.ListSchedules(ctx, workspaceID); err != nil {
		log.Warn("scheduler: OnAutohostWait list schedules failed", "err", err)
	} else {
		for _, e := range existing {
			if e.Name != autohostWaitScheduleName || e.ScheduleType != "once" {
				continue
			}
			if e.FiredAt.Valid {
				staleFired = append(staleFired, e.ID)
				continue
			}
			if e.Enabled == 1 {
				// A resume is already pending — that's success from the caller's
				// perspective (the workspace will be re-entered).
				log.Info("scheduler: autohost-wait schedule already pending; skipping", "scheduleID", e.ID)
				return true
			}
		}
	}

	sched, err := s.q.CreateSchedule(ctx, store.CreateScheduleParams{
		WorkspaceID:    workspaceID,
		Name:           autohostWaitScheduleName,
		DefaultMessage: message,
		ScheduleType:   "once",
		CronExpr:       "",
		RunAt:          sql.NullTime{Time: runAt, Valid: true},
		ActionKind:     "agent_message",
	})
	if err != nil {
		log.Error("scheduler: OnAutohostWait create schedule failed", "err", err)
		return false
	}
	if err := s.Register(sched); err != nil {
		log.Error("scheduler: OnAutohostWait register failed", "scheduleID", sched.ID, "err", err)
		return false
	}
	for _, id := range staleFired {
		if err := s.q.DeleteSchedule(ctx, id); err != nil {
			log.Warn("scheduler: OnAutohostWait prune stale schedule failed", "scheduleID", id, "err", err)
		}
	}

	s.emitBgTaskNotify(ctx, workspaceID)
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicSchedule, Action: "changed", ID: workspaceID})
	}
	log.Info("scheduler: autohost-wait resume scheduled", "scheduleID", sched.ID, "runAt", runAt,
		"prunedFired", len(staleFired))
	return true
}

// CancelAutoResume implements agentproxy.RateLimitScheduler. It deletes and
// unregisters this workspace's UNFIRED auto-resume one-shot schedules — both the
// rate-limit ("限流自动续跑") and autohost-wait ("后台任务自动续跑") kinds. Called
// when a user takes over a workspace that only LOOKS idle but is holding the
// Enqueue gate closed behind a scheduled resume (manual send / interrupt): the
// user is driving now, so a later auto-resume must not double-fire a continue
// prompt against a queue the user already drained. Best-effort; idempotent — no
// pending auto-resume schedule means nothing to do.
func (s *Scheduler) CancelAutoResume(_ context.Context, workspaceID int64) {
	ctx := context.Background()
	log := slog.With("workspaceID", workspaceID)
	existing, err := s.q.ListSchedules(ctx, workspaceID)
	if err != nil {
		log.Warn("scheduler: CancelAutoResume list schedules failed", "err", err)
		return
	}
	cancelled := 0
	for _, e := range existing {
		if e.ScheduleType != "once" || e.FiredAt.Valid {
			continue
		}
		if e.Name != rateLimitScheduleName && e.Name != autohostWaitScheduleName {
			continue
		}
		s.Unregister(e.ID)
		if err := s.q.DeleteSchedule(ctx, e.ID); err != nil {
			log.Warn("scheduler: CancelAutoResume delete failed", "scheduleID", e.ID, "err", err)
			continue
		}
		cancelled++
	}
	if cancelled == 0 {
		return
	}
	s.emitBgTaskNotify(ctx, workspaceID)
	if s.notifyHub != nil {
		s.notifyHub.Broadcast(notify.Notification{Topic: notify.TopicSchedule, Action: "changed", ID: workspaceID})
	}
	log.Info("scheduler: cancelled pending auto-resume schedules on user takeover", "cancelled", cancelled)
}

// --- #243 autonomous discovery helpers ---

// reportsSubdir is the per-workspace directory where autonomous-discovery
// artifacts and the run log are written. Relative to the workspace checkout.
const reportsSubdir = ".niuniu/reports"

// discoveryReportRelPath returns the workspace-relative path the agent should
// write its discovery report to, dated so successive runs do not clobber.
func discoveryReportRelPath(now time.Time) string {
	return reportsSubdir + "/" + now.Format("2006-01-02") + "-discovery.md"
}

// ensureReportsDir best-effort creates the reports directory under the
// workspace so the agent's write target exists. Non-fatal on failure.
func ensureReportsDir(wsPath string, log *slog.Logger) {
	if wsPath == "" {
		return
	}
	dir := filepath.Join(wsPath, filepath.FromSlash(reportsSubdir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warn("scheduler: create reports dir failed", "dir", dir, "err", err)
	}
}

// buildDiscoveryPrompt frames a self-directed "survey the workspace, find
// actionable work, write a report" instruction. extra is the user's queued or
// default message (may be empty); it is woven in as additional focus.
func buildDiscoveryPrompt(extra, reportRel string) string {
	var b strings.Builder
	b.WriteString("[自主发现 / Autonomous Discovery]\n")
	b.WriteString("你被调度器在空闲时段唤醒，进行一轮自主任务发现。请：\n")
	b.WriteString("1. 巡检当前工作区（代码/文档/未完成项/TODO/失败测试/可改进点）。\n")
	b.WriteString("2. 列出你发现的可执行任务，并按价值/风险排序。\n")
	b.WriteString("3. 挑选 1-2 项你有把握的，推进出阶段性成果。\n")
	b.WriteString(fmt.Sprintf("4. 把本轮发现与进展写入交付报告文件：`%s`（追加或新建，Markdown，含时间、发现清单、已做改动、下一步建议）。\n", reportRel))
	b.WriteString("5. 如沉淀出值得长期记住的经验，用 memory_generate 记录（涉及具体文件/改动时传 source_path 留痕）。\n")
	if strings.TrimSpace(extra) != "" {
		b.WriteString("\n本轮额外关注：\n")
		b.WriteString(extra)
		b.WriteString("\n")
	}
	return b.String()
}

// appendScheduleLog appends a one-line run summary to the workspace's
// schedule-log.md. Best-effort: scheduling must not fail because disk write did.
func appendScheduleLog(wsPath string, sched store.WorkspaceSchedule, status, message string, log *slog.Logger) {
	if wsPath == "" {
		return
	}
	dir := filepath.Join(wsPath, filepath.FromSlash(reportsSubdir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warn("scheduler: create reports dir failed", "dir", dir, "err", err)
		return
	}
	name := sched.Name
	if name == "" {
		name = fmt.Sprintf("schedule %d", sched.ID)
	}
	line := fmt.Sprintf("- %s  **%s**  %s  (%s/%s)  %s\n",
		time.Now().Format("2006-01-02 15:04:05"), status, name, sched.ActionKind, sched.ScheduleType,
		truncateMessage(strings.ReplaceAll(message, "\n", " "), 160))
	f, err := os.OpenFile(filepath.Join(dir, "schedule-log.md"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Warn("scheduler: open schedule-log failed", "err", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		log.Warn("scheduler: write schedule-log failed", "err", err)
	}
}
