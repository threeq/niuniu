package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// AssistantManagedTaskRequest is the one-call payload the conversational
// assistant agent posts (via the create_managed_task MCP tool) to stand up a
// recurring "managed task". The agent translates the user's natural-language
// timing into a standard 5-field cron expression itself, so the user never
// fills in a technical field (goal_condition GC1).
type AssistantManagedTaskRequest struct {
	// Description is the imperative recurring instruction delivered to the
	// agent on every tick (e.g. "整理下载文件夹中的文件并生成整理报告").
	Description string `json:"description" binding:"required"`
	// CronExpr is the standard 5-field cron (min hour dom month dow) the agent
	// derived from the user's wording (e.g. "0 9 * * 1" = every Monday 09:00).
	CronExpr string `json:"cron_expr" binding:"required"`
	// Name is a short human-readable task name; derived from Description when omitted.
	Name string `json:"name,omitempty"`
	// GoalCondition is the per-run completion criterion driving autohost
	// self-completion ([AUTOHOST_DONE]); defaults to Description.
	GoalCondition string `json:"goal_condition,omitempty"`
	// AttachToCurrent binds the schedule to the agent's CURRENT task (workspace)
	// instead of provisioning a new task. The agent decides from the user's
	// intent + the current task's content. When false (default) a new task is
	// created — nested as a subtask of the current conversation when there is one.
	AttachToCurrent bool                         `json:"attach_to_current,omitempty"`
	Owner           *CreateWorkspaceOwnerRequest `json:"owner,omitempty"`
}

// AssistantManagedTaskResponse hands back the ids the SPA / agent need to
// reference the new managed task (its backing plan + the bound cron schedule).
type AssistantManagedTaskResponse struct {
	ScheduleID  int64      `json:"schedule_id"`
	IssueID     int64      `json:"issue_id"`
	WorkspaceID int64      `json:"workspace_id"`
	ProjectID   int64      `json:"project_id"`
	Name        string     `json:"name"`
	CronExpr    string     `json:"cron_expr"`
	NextRunAt   *time.Time `json:"next_run_at"`
}

// CreateManagedTask: POST /mcp/managed-tasks
//
// One call provisions a complete recurring managed task: a backing issue (with
// goal_condition for autohost self-completion) + a no-repo workspace bound to
// it + a cron workspace_schedule that delivers the recurring instruction to that
// workspace's agent on every tick. Reuses the backing-project plumbing so the
// task also shows up as a plan in the 定时任务 project and a row on the
// /schedules page (GC2 + GC5). When the schedule fires the agent runs to
// completion and emits
// agent_done, which fans out to both the in-app notify hub and (in the personal
// edition) the OS notification (GC3/GC4/GC6).
func (h *AssistantHandler) CreateManagedTask(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()

	var req AssistantManagedTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	description := strings.TrimSpace(req.Description)
	cronExpr := strings.TrimSpace(req.CronExpr)
	if description == "" {
		BadRequest(c, "description is required")
		return
	}
	if err := validateCronExpr(cronExpr); err != nil {
		BadRequest(c, "invalid cron expression: "+err.Error())
		return
	}

	owner, ok := h.resolveAssistantOwner(c, userID, req.Owner)
	if !ok {
		return
	}

	// The issue's goal_condition (used by autohost to self-judge a run's
	// completion) defaults to the recurring instruction when the agent doesn't
	// supply a distinct criterion.
	goalCondition := strings.TrimSpace(req.GoalCondition)
	if goalCondition == "" {
		goalCondition = description
	}

	// Resolve the target task. The agent chooses (from the user's intent + the
	// current task's content) whether to make the CURRENT task recurring or to
	// spin off a new one. The current workspace comes from the agent's own MCP
	// token (mcp_workspace_id), so it's never user-supplied.
	currentWsID := c.GetInt64("mcp_workspace_id")
	plan, err := h.resolveManagedTaskTarget(ctx, userID, owner, goalCondition, strings.TrimSpace(req.Name), req.AttachToCurrent, currentWsID, c.GetHeader("X-Niuniu-Language"))
	if err != nil {
		InternalError(c, err)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = plan.Title
	}

	// Bind a cron schedule to the target workspace; its default_message is the
	// recurring instruction delivered to the agent on every tick.
	schedule, err := h.Q.CreateSchedule(ctx, store.CreateScheduleParams{
		WorkspaceID:    plan.WorkspaceID,
		Name:           name,
		DefaultMessage: description,
		ScheduleType:   "cron",
		CronExpr:       cronExpr,
		ActionKind:     "agent_message",
	})
	if err != nil {
		InternalError(c, err)
		return
	}

	// Stamp the creating user for membership enforcement (mirrors schedule.go).
	if userID != 0 && h.db != nil {
		if _, err := h.db.ExecContext(ctx,
			store.ConvertPlaceholders(`UPDATE workspace_schedules SET triggered_by = ? WHERE id = ?`),
			userID, schedule.ID); err != nil {
			slog.Warn("managed-task: failed to set triggered_by", "scheduleID", schedule.ID, "err", err)
		}
	}

	// Register with the running scheduler so it begins firing without a restart.
	if h.scheduleChanged != nil {
		h.scheduleChanged(schedule.ID, false)
	}

	c.JSON(http.StatusCreated, AssistantManagedTaskResponse{
		ScheduleID:  schedule.ID,
		IssueID:     plan.IssueID,
		WorkspaceID: plan.WorkspaceID,
		ProjectID:   plan.ProjectID,
		Name:        name,
		CronExpr:    cronExpr,
		NextRunAt:   computeNextRunAt("cron", cronExpr, nil, nil, true),
	})
}

// resolveManagedTaskTarget picks the workspace the schedule binds to:
//
//   - attachToCurrent && a usable current task → reuse the current task's
//     workspace (the task the agent is already working on becomes recurring);
//     no new issue/workspace is created.
//   - otherwise → provision a new task. When there is a current conversation the
//     new task nests under its top-level ancestor (a subtask, mirroring how
//     mid-conversation tasks are grouped); otherwise it's a top-level task.
//
// currentWsID is the agent's own workspace (from its MCP token); 0 when absent.
func (h *AssistantHandler) resolveManagedTaskTarget(ctx context.Context, userID int64, owner service.OwnerRef, goalCondition, nameHint string, attachToCurrent bool, currentWsID int64, language string) (AssistantPlanDTO, error) {
	current, hasCurrent := h.currentTaskIssue(ctx, currentWsID)

	// Attach the schedule to the current task in place. Point the issue's
	// goal_condition at the per-run criterion so each scheduled run self-judges
	// (autohost) against what the recurring job should accomplish, not the
	// task's original one-shot goal.
	if attachToCurrent && hasCurrent {
		if err := h.Kanban.SetIssueGoalCondition(ctx, current.ID, goalCondition); err != nil {
			return AssistantPlanDTO{}, err
		}
		title := current.Title
		if title == "" {
			title = strings.TrimSpace(nameHint)
		}
		return AssistantPlanDTO{
			IssueID:     current.ID,
			WorkspaceID: currentWsID,
			ProjectID:   current.ProjectID,
			Title:       title,
		}, nil
	}

	// Create a new task, nested under the current conversation when present
	// (resolve to the top-level ancestor — CreateIssue caps at two levels).
	parent := int64(0)
	if hasCurrent {
		if current.ParentIssueID.Valid && current.ParentIssueID.Int64 > 0 {
			parent = current.ParentIssueID.Int64
		} else {
			parent = current.ID
		}
	}
	return h.createPlan(ctx, userID, owner, goalCondition, nameHint, parent, language)
}

// currentTaskIssue resolves the issue backing the agent's current workspace.
// hasCurrent is false when there's no workspace context or it can't be resolved
// (best-effort: a missing current task simply falls back to creating a new one).
func (h *AssistantHandler) currentTaskIssue(ctx context.Context, currentWsID int64) (service.IssueDetail, bool) {
	if currentWsID <= 0 || h.Workspace == nil || h.Kanban == nil {
		return service.IssueDetail{}, false
	}
	ws, err := h.Workspace.Get(ctx, currentWsID)
	if err != nil || !ws.IssueID.Valid || ws.IssueID.Int64 <= 0 {
		return service.IssueDetail{}, false
	}
	issue, err := h.Kanban.GetIssue(ctx, ws.IssueID.Int64)
	if err != nil {
		return service.IssueDetail{}, false
	}
	return issue, true
}
