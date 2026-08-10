package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	cron "github.com/robfig/cron/v3"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// ScheduleHandler provides HTTP handlers for schedule CRUD.
type ScheduleHandler struct {
	q            *store.Queries
	db           *sql.DB // raw DB for fields not in sqlc model (triggered_by)
	onChanged    func(scheduleID int64, deleted bool)
	triggerNow   func(ctx context.Context, scheduleID int64) error
	workspaceSvc *service.WorkspaceService
	Authz        *service.Authz
}

// NewScheduleHandler creates a new ScheduleHandler.
func NewScheduleHandler(q *store.Queries, workspaceSvc *service.WorkspaceService) *ScheduleHandler {
	return &ScheduleHandler{q: q, workspaceSvc: workspaceSvc}
}

// SetDB wires the raw DB connection for triggered_by persistence (spec §5.11).
func (h *ScheduleHandler) SetDB(db *sql.DB) {
	h.db = db
}

// SetOnChanged registers a callback invoked after create/update/delete/toggle/trigger.
func (h *ScheduleHandler) SetOnChanged(fn func(scheduleID int64, deleted bool)) {
	h.onChanged = fn
}

func (h *ScheduleHandler) SetTriggerNow(fn func(ctx context.Context, scheduleID int64) error) {
	h.triggerNow = fn
}

func (h *ScheduleHandler) notifyChanged(id int64, deleted bool) {
	if h.onChanged != nil {
		h.onChanged(id, deleted)
	}
}

// ---------- Request / Response types ----------

type CreateScheduleRequest struct {
	Name           string  `json:"name"`
	DefaultMessage string  `json:"default_message"`
	ScheduleType   string  `json:"schedule_type" binding:"required"`
	CronExpr       string  `json:"cron_expr"`
	RunAt          *string `json:"run_at"`
	ActionKind     string  `json:"action_kind"`
}

type UpdateScheduleRequest struct {
	Name           string  `json:"name"`
	DefaultMessage string  `json:"default_message"`
	ScheduleType   string  `json:"schedule_type" binding:"required"`
	CronExpr       string  `json:"cron_expr"`
	RunAt          *string `json:"run_at"`
	ActionKind     string  `json:"action_kind"`
}

type ToggleScheduleRequest struct {
	Enabled bool `json:"enabled"`
}

type ScheduleResponse struct {
	ID             int64      `json:"id"`
	WorkspaceID    int64      `json:"workspace_id"`
	Name           string     `json:"name"`
	DefaultMessage string     `json:"default_message"`
	ScheduleType   string     `json:"schedule_type"`
	ActionKind     string     `json:"action_kind"`
	CronExpr       string     `json:"cron_expr"`
	RunAt          *time.Time `json:"run_at"`
	Enabled        bool       `json:"enabled"`
	FiredAt        *time.Time `json:"fired_at"`
	LastRunAt      *time.Time `json:"last_run_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// GlobalScheduleResponse extends ScheduleResponse with cross-workspace fields.
type GlobalScheduleResponse struct {
	ScheduleResponse
	NextRunAt       *time.Time `json:"next_run_at"`
	WorkspaceName   string     `json:"workspace_name"`
	WorkspaceStatus string     `json:"workspace_status"`
}

// computeNextRunAt calculates the next run time for a schedule.
func computeNextRunAt(scheduleType, cronExpr string, runAt, firedAt *time.Time, enabled bool) *time.Time {
	if !enabled {
		return nil
	}
	if scheduleType == "cron" {
		sched, err := cronParser.Parse(cronExpr)
		if err != nil {
			return nil
		}
		next := sched.Next(time.Now())
		return &next
	}
	// once type
	if firedAt != nil {
		return nil // already fired
	}
	return runAt // may be in the past (overdue) — frontend handles display
}

// ScheduleRunResponse is the API response for a schedule run record.
type ScheduleRunResponse struct {
	ID          int64     `json:"id"`
	ScheduleID  int64     `json:"schedule_id"`
	WorkspaceID int64     `json:"workspace_id"`
	Status      string    `json:"status"`
	Message     string    `json:"message"`
	CreatedAt   time.Time `json:"created_at"`
}

func toScheduleResponse(s store.WorkspaceSchedule) ScheduleResponse {
	var runAt *time.Time
	if s.RunAt.Valid {
		runAt = &s.RunAt.Time
	}
	var firedAt *time.Time
	if s.FiredAt.Valid {
		firedAt = &s.FiredAt.Time
	}
	var lastRunAt *time.Time
	if s.LastRunAt.Valid {
		lastRunAt = &s.LastRunAt.Time
	}
	return ScheduleResponse{
		ID:             s.ID,
		WorkspaceID:    s.WorkspaceID,
		Name:           s.Name,
		DefaultMessage: s.DefaultMessage,
		ScheduleType:   s.ScheduleType,
		ActionKind:     s.ActionKind,
		CronExpr:       s.CronExpr,
		RunAt:          runAt,
		Enabled:        s.Enabled != 0,
		FiredAt:        firedAt,
		LastRunAt:      lastRunAt,
		CreatedAt:      s.CreatedAt,
		UpdatedAt:      s.UpdatedAt,
	}
}

func toScheduleResponses(ss []store.WorkspaceSchedule) []ScheduleResponse {
	out := make([]ScheduleResponse, len(ss))
	for i, s := range ss {
		out[i] = toScheduleResponse(s)
	}
	return out
}

// ---------- Helpers ----------

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func validateCronExpr(expr string) error {
	_, err := cronParser.Parse(expr)
	return err
}

// normalizeActionKind validates the schedule action kind, defaulting unknown or
// empty values to "agent_message" (the legacy send-message behavior). #243 adds
// "autonomous_discovery" which makes idle ticks self-direct a survey+report run.
func normalizeActionKind(k string) string {
	switch k {
	case "autonomous_discovery":
		return "autonomous_discovery"
	default:
		return "agent_message"
	}
}

func parseRunAt(raw *string) (sql.NullTime, error) {
	if raw == nil || *raw == "" {
		return sql.NullTime{}, nil
	}
	t, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return sql.NullTime{}, err
	}
	return sql.NullTime{Time: t, Valid: true}, nil
}

// ---------- Handlers ----------

// List returns all schedules for a workspace.
func (h *ScheduleHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace_id")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, wsID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	schedules, err := h.q.ListSchedules(c.Request.Context(), wsID)
	if err != nil {
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, toScheduleResponses(schedules))
}

// Create creates a new schedule.
func (h *ScheduleHandler) Create(c *gin.Context) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), wsID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	var req CreateScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	// Validate cron expression for cron-type schedules.
	if req.ScheduleType == "cron" {
		if err := validateCronExpr(req.CronExpr); err != nil {
			BadRequest(c, "invalid cron expression: "+err.Error())
			return
		}
	}

	// Validate run_at for once-type schedules.
	if req.ScheduleType == "once" {
		if req.RunAt == nil || *req.RunAt == "" {
			BadRequest(c, "run_at is required for once schedules")
			return
		}
	}

	runAt, err := parseRunAt(req.RunAt)
	if err != nil {
		BadRequest(c, "invalid run_at: "+err.Error())
		return
	}

	schedule, err := h.q.CreateSchedule(c.Request.Context(), store.CreateScheduleParams{
		WorkspaceID:    wsID,
		Name:           req.Name,
		DefaultMessage: req.DefaultMessage,
		ScheduleType:   req.ScheduleType,
		CronExpr:       req.CronExpr,
		RunAt:          runAt,
		ActionKind:     normalizeActionKind(req.ActionKind),
	})
	if err != nil {
		InternalError(c, err)
		return
	}

	// Record the creating user as triggered_by for membership enforcement (spec §5.11).
	if userID := c.GetInt64("auth_user_id"); userID != 0 && h.db != nil {
		if _, err := h.db.ExecContext(c.Request.Context(),
			store.ConvertPlaceholders(`UPDATE workspace_schedules SET triggered_by = ? WHERE id = ?`),
			userID, schedule.ID); err != nil {
			slog.Warn("schedule: failed to set triggered_by", "scheduleID", schedule.ID, "err", err)
		}
	}

	h.notifyChanged(schedule.ID, false)
	c.JSON(http.StatusCreated, toScheduleResponse(schedule))
}

// Update updates an existing schedule.
func (h *ScheduleHandler) Update(c *gin.Context) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), wsID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}
	_ = wsID

	id, err := strconv.ParseInt(c.Param("scheduleId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid schedule ID")
		return
	}

	var req UpdateScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	// Validate cron expression for cron-type schedules.
	if req.ScheduleType == "cron" {
		if err := validateCronExpr(req.CronExpr); err != nil {
			BadRequest(c, "invalid cron expression: "+err.Error())
			return
		}
	}
	if req.ScheduleType == "once" {
		if req.RunAt == nil || *req.RunAt == "" {
			BadRequest(c, "run_at is required for once schedules")
			return
		}
	}

	runAt, err := parseRunAt(req.RunAt)
	if err != nil {
		BadRequest(c, "invalid run_at: "+err.Error())
		return
	}

	if err := h.q.UpdateSchedule(c.Request.Context(), store.UpdateScheduleParams{
		Name:           req.Name,
		DefaultMessage: req.DefaultMessage,
		ScheduleType:   req.ScheduleType,
		CronExpr:       req.CronExpr,
		RunAt:          runAt,
		ActionKind:     normalizeActionKind(req.ActionKind),
		ID:             id,
	}); err != nil {
		InternalError(c, err)
		return
	}

	h.notifyChanged(id, false)
	c.Status(http.StatusNoContent)
}

// Delete removes a schedule.
func (h *ScheduleHandler) Delete(c *gin.Context) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), wsID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}
	_ = wsID

	id, err := strconv.ParseInt(c.Param("scheduleId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid schedule ID")
		return
	}

	if err := h.q.DeleteSchedule(c.Request.Context(), id); err != nil {
		InternalError(c, err)
		return
	}

	h.notifyChanged(id, true)
	c.Status(http.StatusNoContent)
}

// Toggle enables or disables a schedule.
func (h *ScheduleHandler) Toggle(c *gin.Context) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), wsID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}
	_ = wsID

	id, err := strconv.ParseInt(c.Param("scheduleId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid schedule ID")
		return
	}

	var req ToggleScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	var enabled int64
	if req.Enabled {
		enabled = 1
	}

	if err := h.q.ToggleSchedule(c.Request.Context(), store.ToggleScheduleParams{
		Enabled: enabled,
		ID:      id,
	}); err != nil {
		InternalError(c, err)
		return
	}

	h.notifyChanged(id, false)
	c.Status(http.StatusNoContent)
}

// Trigger manually triggers a schedule execution immediately.
func (h *ScheduleHandler) Trigger(c *gin.Context) {
	wsID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), wsID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}
	_ = wsID

	id, err := strconv.ParseInt(c.Param("scheduleId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid schedule ID")
		return
	}

	ctx := c.Request.Context()
	if h.triggerNow != nil {
		if err := h.triggerNow(ctx, id); err != nil {
			slog.Warn("Trigger: failed", "id", id, "error", err)
			NotFound(c, "SCHEDULE")
			return
		}
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "triggered"})
}

// ListAll returns all schedules across all workspaces.
// When auth is active (userID > 0), post-filters to only include schedules
// whose workspace is accessible to the calling user.
func (h *ScheduleHandler) ListAll(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	rows, err := h.q.ListAllSchedulesWithWorkspace(c.Request.Context())
	if err != nil {
		InternalError(c, err)
		return
	}

	out := make([]GlobalScheduleResponse, 0, len(rows))
	for _, row := range rows {
		// When auth is active, drop schedules for workspaces the caller cannot access.
		if userID > 0 && h.Authz != nil {
			if _, aerr := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, row.WorkspaceID); aerr != nil {
				continue
			}
		}

		// Map the row fields to a store.WorkspaceSchedule to reuse toScheduleResponse
		base := toScheduleResponse(store.WorkspaceSchedule{
			ID:             row.ID,
			WorkspaceID:    row.WorkspaceID,
			Name:           row.Name,
			DefaultMessage: row.DefaultMessage,
			ScheduleType:   row.ScheduleType,
			ActionKind:     row.ActionKind,
			CronExpr:       row.CronExpr,
			RunAt:          row.RunAt,
			Enabled:        row.Enabled,
			FiredAt:        row.FiredAt,
			LastRunAt:      row.LastRunAt,
			CreatedAt:      row.CreatedAt,
			UpdatedAt:      row.UpdatedAt,
		})

		out = append(out, GlobalScheduleResponse{
			ScheduleResponse: base,
			NextRunAt:        computeNextRunAt(row.ScheduleType, row.CronExpr, base.RunAt, base.FiredAt, row.Enabled != 0),
			WorkspaceName:    row.WorkspaceName,
			WorkspaceStatus:  row.WorkspaceStatus,
		})
	}
	c.JSON(http.StatusOK, out)
}

// ListRuns returns paginated run history for a schedule.
func (h *ScheduleHandler) ListRuns(c *gin.Context) {
	scheduleID, err := strconv.ParseInt(c.Param("scheduleId"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid schedule ID")
		return
	}

	var beforeID int64
	if b := c.Query("before"); b != "" {
		beforeID, err = strconv.ParseInt(b, 10, 64)
		if err != nil {
			BadRequest(c, "invalid before parameter")
			return
		}
	}

	limitStr := c.DefaultQuery("limit", "20")
	limit, err := strconv.ParseInt(limitStr, 10, 64)
	if err != nil || limit < 1 || limit > 100 {
		limit = 20
	}

	runs, err := h.q.ListScheduleRuns(c.Request.Context(), store.ListScheduleRunsParams{
		ScheduleID: scheduleID,
		BeforeID:   beforeID,
		Limit:      limit,
	})
	if err != nil {
		InternalError(c, err)
		return
	}

	out := make([]ScheduleRunResponse, len(runs))
	for i, r := range runs {
		out[i] = ScheduleRunResponse{
			ID:          r.ID,
			ScheduleID:  r.ScheduleID,
			WorkspaceID: r.WorkspaceID,
			Status:      r.Status,
			Message:     r.Message,
			CreatedAt:   r.CreatedAt,
		}
	}
	c.JSON(http.StatusOK, out)
}
