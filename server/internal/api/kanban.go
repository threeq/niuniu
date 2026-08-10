package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// ColumnDTO is the wire format for kanban columns. Avoids leaking
// sql.NullString's `{String, Valid}` shape (which crashes the SPA with
// React error #31). Use *string for nullable text columns.
type ColumnDTO struct {
	ID               int64              `json:"id"`
	ProjectID        int64              `json:"project_id"`
	Name             string             `json:"name"`
	Position         int64              `json:"position"`
	LifecycleMapping string             `json:"lifecycle_mapping"`
	ReviewerAgent    *string            `json:"reviewer_agent"`
	PhasePrompt      *string            `json:"phase_prompt"`
	AutoAdvance      int64              `json:"auto_advance"`
	GateSpecs        []GateSpecBriefDTO `json:"gate_specs"` // ordered by position; empty slice when none
	OpPrimitive      string             `json:"op_primitive"`
	WhenToUse        *string            `json:"when_to_use"`
}

// GateSpecBriefDTO is a column-row summary of a linked harness gate spec, used
// in the project settings table so users see what was configured without
// re-opening the picker dialog.
type GateSpecBriefDTO struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Severity      string `json:"severity"`
	Applicability string `json:"applicability"` // if_routed | always (spec §5/§18)
}

func toColumnDTO(c store.Column) ColumnDTO {
	dto := ColumnDTO{
		ID:               c.ID,
		ProjectID:        c.ProjectID,
		Name:             c.Name,
		Position:         c.Position,
		LifecycleMapping: c.LifecycleMapping,
		AutoAdvance:      c.AutoAdvance,
		GateSpecs:        []GateSpecBriefDTO{},
	}
	if c.ReviewerAgent.Valid {
		v := c.ReviewerAgent.String
		dto.ReviewerAgent = &v
	}
	if c.PhasePrompt.Valid {
		v := c.PhasePrompt.String
		dto.PhasePrompt = &v
	}
	return dto
}

type KanbanHandler struct {
	svc           *service.KanbanService
	checklistSvc  *service.IssueChecklistService
	Authz         *service.Authz
	ClaudeAccount *service.ClaudeAccountService // optional; nil = SuggestGoalCondition spawns with no CLAUDE_CONFIG_DIR
	// Orchestrator auto-starts orchestration when an issue is created directly into
	// an `instruct` column (spec §13 stage 8 / §3 "建 issue 即起编排"). Optional;
	// nil-safe (creation just lands the card). Wired to *service.EpicExecutionService.
	Orchestrator IssueCreateOrchestrator
}

// IssueCreateOrchestrator is the create-into-column orchestration hook used by the
// creator path (CreateIssue). Implemented by *service.EpicExecutionService.
type IssueCreateOrchestrator interface {
	OnIssueCreated(ctx context.Context, issueID, callerUserID int64) (service.AdvanceIssueResult, error)
}

func NewKanbanHandler(svc *service.KanbanService, checklistSvc *service.IssueChecklistService) *KanbanHandler {
	return &KanbanHandler{svc: svc, checklistSvc: checklistSvc}
}

// ListAttentionIssues serves GET /api/me/attention-issues — the "需要我处理的"
// view (spec §19): issues in a terminal needs-human exec_status across every owner
// the caller can access. Tenant isolation is enforced by the owner-scoped query.
func (h *KanbanHandler) ListAttentionIssues(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var ownerUserID int64
	var orgIDs []int64
	if userID > 0 && h.Authz != nil {
		owners, err := h.Authz.Accessible(c.Request.Context(), userID)
		if err != nil {
			InternalError(c, err)
			return
		}
		ownerUserID = owners.UserID
		orgIDs = owners.OrgIDs
	} else {
		ownerUserID = userID // single-user / auth-disabled: personal space only
	}
	issues, err := h.svc.ListAttentionIssues(c.Request.Context(), ownerUserID, orgIDs)
	if err != nil {
		InternalError(c, err)
		return
	}
	// Resolve every issue's column->project in one batched query so the SPA can
	// deep-link the cards without a per-issue GetColumn round-trip.
	colIDs := make([]int64, 0, len(issues))
	seen := make(map[int64]struct{}, len(issues))
	for _, i := range issues {
		if _, ok := seen[i.ColumnID]; ok {
			continue
		}
		seen[i.ColumnID] = struct{}{}
		colIDs = append(colIDs, i.ColumnID)
	}
	colByID, err := h.svc.GetColumnsByIDs(c.Request.Context(), colIDs)
	if err != nil {
		slog.Warn("ListAttentionIssues: column lookup failed", "err", err)
		colByID = map[int64]store.Column{}
	}
	out := make([]IssueResponse, 0, len(issues))
	for _, i := range issues {
		var projectID int64
		if col, ok := colByID[i.ColumnID]; ok {
			projectID = col.ProjectID
		}
		resp := toIssueResponse(service.IssueDetail{Issue: i, ProjectID: projectID})
		resp.ProjectID = projectID
		out = append(out, resp)
	}
	c.JSON(http.StatusOK, out)
}

// authzColumnProject resolves a column's project and checks whether userID may access it.
// Returns the project_id on success, 0 and writes an HTTP error on failure.
func (h *KanbanHandler) authzColumnProject(c *gin.Context, userID, columnID int64) (int64, bool) {
	col, err := h.svc.GetColumn(c.Request.Context(), columnID)
	if err != nil {
		NotFound(c, "COLUMN")
		return 0, false
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessProject(c.Request.Context(), userID, col.ProjectID); aerr != nil {
			writeAuthzError(c, aerr)
			return 0, false
		}
	}
	return col.ProjectID, true
}

// Column handlers

type CreateColumnRequest struct {
	Name             string `json:"name" binding:"required"`
	Position         int64  `json:"position"`
	LifecycleMapping string `json:"lifecycle_mapping"`
}

type UpdateColumnRequest struct {
	Name string `json:"name" binding:"required"`
}

type UpdateColumnPositionRequest struct {
	Position int64 `json:"position"`
}

// ListColumns returns all columns for a project
// @Summary      List columns
// @Description  Get all columns for a project
// @Tags         Columns
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Project ID"
// @Success      200  {array}   store.Column
// @Failure      500  {object}  Error
// @Router       /projects/{id}/columns [get]
func (h *KanbanHandler) ListColumns(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	projectID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessProject(c.Request.Context(), userID, projectID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	ctx := c.Request.Context()
	columns, err := h.svc.ListColumns(ctx, projectID)
	if err != nil {
		InternalError(c, err)
		return
	}
	// op_primitive / when_to_use are migrate-only columns absent from the sqlc
	// store.Column; fetch them in one pass and merge into each DTO.
	opFields, ofErr := h.svc.ListColumnOpFields(ctx, projectID)
	if ofErr != nil {
		slog.Warn("ListColumns: op-fields lookup failed", "projectID", projectID, "err", ofErr)
		opFields = map[int64]service.ColumnOpFields{}
	}

	// Resolve gate-spec bindings for all columns and their specs in two batched
	// queries (instead of a per-column + per-spec N+1).
	colIDs := make([]int64, 0, len(columns))
	for _, col := range columns {
		colIDs = append(colIDs, col.ID)
	}
	linksByColumn, lerr := h.svc.ListColumnGateSpecsByColumns(ctx, colIDs)
	if lerr != nil {
		slog.Warn("ListColumns: gate-spec lookup failed; returning empty gate lists", "projectID", projectID, "err", lerr)
		linksByColumn = map[int64][]store.ColumnGateSpec{}
	}
	specIDSet := map[int64]struct{}{}
	for _, links := range linksByColumn {
		for _, link := range links {
			specIDSet[link.SpecID] = struct{}{}
		}
	}
	specIDs := make([]int64, 0, len(specIDSet))
	for id := range specIDSet {
		specIDs = append(specIDs, id)
	}
	specsByID, serr := h.svc.GetHarnessSpecsByIDs(ctx, specIDs)
	if serr != nil {
		slog.Warn("ListColumns: spec lookup failed; omitting gate briefs", "projectID", projectID, "err", serr)
		specsByID = map[int64]store.HarnessSpec{}
	}

	dtos := make([]ColumnDTO, 0, len(columns))
	for _, col := range columns {
		dto := toColumnDTO(col)
		if f, ok := opFields[col.ID]; ok {
			dto.OpPrimitive = f.OpPrimitive
			dto.WhenToUse = f.WhenToUse
		}
		for _, link := range linksByColumn[col.ID] {
			spec, ok := specsByID[link.SpecID]
			if !ok {
				continue
			}
			// applicability is per-binding, not per-spec, so set it from the link.
			dto.GateSpecs = append(dto.GateSpecs, GateSpecBriefDTO{
				ID:            spec.ID,
				Name:          spec.Name,
				Severity:      spec.Severity,
				Applicability: link.Applicability,
			})
		}
		dtos = append(dtos, dto)
	}
	c.JSON(http.StatusOK, dtos)
}

// CreateColumn creates a new column
// @Summary      Create a column
// @Description  Create a new column for a project
// @Tags         Columns
// @Accept       json
// @Produce      json
// @Param        id       path      string               true  "Project ID"
// @Param        request  body      CreateColumnRequest  true  "Column details"
// @Success      201     {object}  store.Column
// @Failure      400     {object}  Error
// @Failure      500     {object}  Error
// @Router       /projects/{id}/columns [post]
func (h *KanbanHandler) CreateColumn(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	projectID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessProject(c.Request.Context(), userID, projectID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	var req CreateColumnRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	column, err := h.svc.CreateColumn(c.Request.Context(), projectID, req.Name, req.LifecycleMapping)
	if err != nil {
		if strings.Contains(err.Error(), "invalid lifecycle status") {
			BadRequest(c, err.Error())
		} else {
			InternalError(c, err)
		}
		return
	}
	c.JSON(http.StatusCreated, toColumnDTO(column))
}

// UpdateColumn updates an existing column
// @Summary      Update a column
// @Description  Update column details
// @Tags         Columns
// @Accept       json
// @Produce      json
// @Param        id       path      string               true  "Column ID"
// @Param        request  body      UpdateColumnRequest  true  "Updated column details"
// @Success      200      {object}  store.Column
// @Failure      400      {object}  Error
// @Failure      404      {object}  Error
// @Failure      500      {object}  Error
// @Router       /columns/{id} [put]
func (h *KanbanHandler) UpdateColumn(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	columnID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid column ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, ok := h.authzColumnProject(c, userID, columnID); !ok {
			return
		}
	}
	var req UpdateColumnRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	column, err := h.svc.UpdateColumn(c.Request.Context(), columnID, req.Name)
	if err != nil {
		slog.Warn("UpdateColumn failed", "columnID", columnID, "error", err)
		NotFound(c, "COLUMN")
		return
	}
	c.JSON(http.StatusOK, toColumnDTO(column))
}

// UpdateColumnExtensionRequest carries the column-based AI-native fields.
// Pointer fields keep "absent vs explicit empty" distinct (nil = no change).
type UpdateColumnExtensionRequest struct {
	ReviewerAgent *string `json:"reviewer_agent"`
	PhasePrompt   *string `json:"phase_prompt"`
	OpPrimitive   *string `json:"op_primitive"`
	WhenToUse     *string `json:"when_to_use"`
	AutoAdvance   *bool   `json:"auto_advance"`
}

// ColumnExtensionResponse flattens sql.NullString to plain string and
// auto_advance int64 to bool for clean JSON output.
type ColumnExtensionResponse struct {
	ID               int64   `json:"id"`
	ProjectID        int64   `json:"project_id"`
	Name             string  `json:"name"`
	Position         int64   `json:"position"`
	LifecycleMapping string  `json:"lifecycle_mapping"`
	ReviewerAgent    string  `json:"reviewer_agent"`
	PhasePrompt      string  `json:"phase_prompt"`
	AutoAdvance      bool    `json:"auto_advance"`
	OpPrimitive      string  `json:"op_primitive"`
	WhenToUse        *string `json:"when_to_use"`
}

func (h *KanbanHandler) UpdateColumnExtension(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	columnID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid column ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, ok := h.authzColumnProject(c, userID, columnID); !ok {
			return
		}
	}
	var req UpdateColumnExtensionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	col, err := h.svc.UpdateColumnExtension(c.Request.Context(), columnID, service.UpdateColumnExtensionInput{
		ReviewerAgent: req.ReviewerAgent,
		PhasePrompt:   req.PhasePrompt,
		OpPrimitive:   req.OpPrimitive,
		WhenToUse:     req.WhenToUse,
		AutoAdvance:   req.AutoAdvance,
	})
	if err != nil {
		slog.Warn("UpdateColumnExtension failed", "columnID", columnID, "error", err)
		if strings.Contains(err.Error(), "invalid op_primitive") {
			BadRequest(c, err.Error())
			return
		}
		NotFound(c, "COLUMN")
		return
	}
	opf, _ := h.svc.GetColumnOpFields(c.Request.Context(), columnID)
	c.JSON(http.StatusOK, ColumnExtensionResponse{
		ID:               col.ID,
		ProjectID:        col.ProjectID,
		Name:             col.Name,
		Position:         col.Position,
		LifecycleMapping: col.LifecycleMapping,
		ReviewerAgent:    col.ReviewerAgent.String,
		PhasePrompt:      col.PhasePrompt.String,
		AutoAdvance:      col.AutoAdvance == 1,
		OpPrimitive:      opf.OpPrimitive,
		WhenToUse:        opf.WhenToUse,
	})
}

// SetIssueExecFields sets the Executable Epic hierarchy + execution fields
// (parent_issue_id, issue_type, exec_wave, exec_status) on an issue.
// PUT /api/issues/:id/exec-fields. The same-project parent constraint is
// enforced in the service layer; cross-project parents are rejected with 400.
func (h *KanbanHandler) SetIssueExecFields(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		issue, ierr := h.svc.GetIssue(c.Request.Context(), issueID)
		if ierr != nil {
			NotFound(c, "ISSUE")
			return
		}
		col, cerr := h.svc.GetColumn(c.Request.Context(), issue.ColumnID)
		if cerr != nil {
			NotFound(c, "COLUMN")
			return
		}
		if _, aerr := h.Authz.CanAccessProject(c.Request.Context(), userID, col.ProjectID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	var req SetIssueExecFieldsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	detail, err := h.svc.SetIssueExecFields(c.Request.Context(), issueID, req.ParentIssueID, req.IssueType, req.ExecWave, req.ExecStatus)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, toIssueResponse(detail))
}

// ReplaceColumnGateSpecsRequest replaces a column's gate spec list. `gates` is the
// applicability-aware form (if_routed | always per spec); `spec_ids` is the legacy
// form (all default to if_routed) kept for mobile/desktop back-compat. When both are
// present, `gates` wins.
type ReplaceColumnGateSpecsRequest struct {
	Gates   []GateSpecBindingDTO `json:"gates"`
	SpecIDs []int64              `json:"spec_ids"`
}

// GateSpecBindingDTO is one entry of the applicability-aware bind request.
type GateSpecBindingDTO struct {
	SpecID        int64  `json:"spec_id"`
	Applicability string `json:"applicability"` // if_routed | always; blank -> if_routed
}

func (h *KanbanHandler) ReplaceColumnGateSpecs(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	columnID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid column ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		projectID, ok := h.authzColumnProject(c, userID, columnID)
		if !ok {
			return
		}
		// Floor-config write permission (spec §18): binding/unbinding a column's gate
		// specs is how the floor (always-applicability error specs) is set or removed,
		// so it must be limited to org owner/admin (personal owners always pass). A
		// plain member cannot architect-away the floor by unbinding it.
		admin, aerr := h.Authz.IsProjectAdmin(c.Request.Context(), userID, projectID)
		if aerr != nil {
			InternalError(c, aerr)
			return
		}
		if !admin {
			RespondError(c, http.StatusForbidden, "ADMIN_REQUIRED", "only an org owner/admin may change a column's gate specs")
			return
		}
	}
	var req ReplaceColumnGateSpecsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	bindings := make([]service.GateSpecBinding, 0, len(req.Gates)+len(req.SpecIDs))
	if len(req.Gates) > 0 {
		for _, g := range req.Gates {
			bindings = append(bindings, service.GateSpecBinding{SpecID: g.SpecID, Applicability: g.Applicability})
		}
	} else {
		for _, sid := range req.SpecIDs {
			bindings = append(bindings, service.GateSpecBinding{SpecID: sid, Applicability: "if_routed"})
		}
	}
	gates, err := h.svc.ReplaceColumnGateSpecs(c.Request.Context(), columnID, bindings)
	if err != nil {
		slog.Warn("ReplaceColumnGateSpecs failed", "columnID", columnID, "error", err)
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"specs": gates})
}

func (h *KanbanHandler) ListColumnGateSpecs(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	columnID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid column ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, ok := h.authzColumnProject(c, userID, columnID); !ok {
			return
		}
	}
	gates, err := h.svc.ListColumnGateSpecs(c.Request.Context(), columnID)
	if err != nil {
		BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"specs": gates})
}

// UpdateColumnPosition updates a column's position
// @Summary      Update column position
// @Description  Update column position by swapping with another column
// @Tags         Columns
// @Accept       json
// @Produce      json
// @Param        id       path      string                      true  "Column ID"
// @Param        request  body      UpdateColumnPositionRequest  true  "New position"
// @Success      204
// @Failure      400      {object}  Error
// @Failure      404      {object}  Error
// @Failure      500      {object}  Error
// @Router       /columns/{id}/position [put]
func (h *KanbanHandler) UpdateColumnPosition(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	columnID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid column ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, ok := h.authzColumnProject(c, userID, columnID); !ok {
			return
		}
	}
	var req UpdateColumnPositionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	// Get the column to find its project_id
	column, err := h.svc.GetColumn(c.Request.Context(), columnID)
	if err != nil {
		slog.Warn("UpdateColumnPosition failed to get column", "columnID", columnID, "error", err)
		NotFound(c, "COLUMN")
		return
	}

	if err := h.svc.UpdateColumnPosition(c.Request.Context(), column.ProjectID, columnID, req.Position); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// DeleteColumn removes a column
// @Summary      Delete a column
// @Description  Delete a column and all associated issues
// @Tags         Columns
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Column ID"
// @Success      204
// @Failure      500  {object}  Error
// @Router       /columns/{id} [delete]
func (h *KanbanHandler) DeleteColumn(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	columnID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid column ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, ok := h.authzColumnProject(c, userID, columnID); !ok {
			return
		}
	}
	if err := h.svc.DeleteColumn(c.Request.Context(), columnID); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Issue handlers

type CreateIssueRequest struct {
	Title        string  `json:"title" binding:"required"`
	Description  string  `json:"description"`
	Priority     int64   `json:"priority"`
	Position     int64   `json:"position"`
	StartDate    string  `json:"start_date"`
	DueDate      string  `json:"due_date"`
	EstimateType string  `json:"estimate_type"`
	Estimate     float64 `json:"estimate"`
	// Executable Epic (optional): ParentIssueID attaches this issue as a child of
	// an Epic (nil = top-level), IssueType is 'task' (default) or 'epic', ExecWave
	// is the child execution wave.
	ParentIssueID   *int64  `json:"parent_issue_id,omitempty"`
	IssueType       string  `json:"issue_type,omitempty"`
	ExecWave        int64   `json:"exec_wave,omitempty"`
	AssigneeUserIDs []int64 `json:"assignee_user_ids,omitempty"`
	LabelIDs        []int64 `json:"label_ids,omitempty"`
}

// SetIssueExecFieldsRequest sets the Executable Epic hierarchy + execution fields
// on an existing issue. ParentIssueID nil clears the parent.
type SetIssueExecFieldsRequest struct {
	ParentIssueID *int64 `json:"parent_issue_id"`
	IssueType     string `json:"issue_type"`
	ExecWave      int64  `json:"exec_wave"`
	ExecStatus    string `json:"exec_status"`
}

// UpdateIssueRequest covers only the scalar fields. Assignees and labels move
// through the dedicated PUT /api/issues/:id/{assignees,labels} subresources.
//
// GoalCondition is a pointer so the handler can distinguish "field omitted"
// (no-op) from "explicit empty string" (clear the autohost stop condition).
type UpdateIssueRequest struct {
	Title         string  `json:"title" binding:"required"`
	Description   string  `json:"description"`
	Priority      int64   `json:"priority"`
	StartDate     string  `json:"start_date"`
	DueDate       string  `json:"due_date"`
	EstimateType  string  `json:"estimate_type"`
	Estimate      float64 `json:"estimate"`
	ActualTime    float64 `json:"actual_time"`
	GoalCondition *string `json:"goal_condition,omitempty"`
}

type MoveIssueRequest struct {
	ColumnID int64 `json:"column_id" binding:"required"`
	Position int64 `json:"position" binding:"gte=0"`
}

type UpdateLifecycleRequest struct {
	LifecycleStatus string `json:"lifecycleStatus" binding:"required"`
}

type UpdateColumnLifecycleMappingRequest struct {
	LifecycleMapping string `json:"lifecycle_mapping"`
}

// ListIssues returns all issues for a column
// @Summary      List issues
// @Description  Get all issues for a column
// @Tags         Issues
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Column ID"
// @Success      200  {array}   IssueResponse
// @Failure      500  {object}  Error
// @Router       /columns/{id}/issues [get]
func (h *KanbanHandler) ListIssues(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	columnID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid column ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, ok := h.authzColumnProject(c, userID, columnID); !ok {
			return
		}
	}
	issues, err := h.svc.ListIssues(c.Request.Context(), columnID)
	if err != nil {
		InternalError(c, err)
		return
	}
	responses := toIssueResponses(issues)

	// Resolve project_id from the column (one lookup for all issues)
	col, colErr := h.svc.GetColumn(c.Request.Context(), columnID)
	if colErr != nil {
		slog.Warn("ListIssues: failed to resolve project_id", "columnID", columnID, "error", colErr)
	}

	for i, issue := range issues {
		if colErr == nil {
			responses[i].ProjectID = col.ProjectID
		}
		stats, sErr := h.checklistSvc.GetStats(c.Request.Context(), issue.ID)
		if sErr == nil && stats.Total > 0 {
			responses[i].ChecklistStats = &ChecklistStatsDTO{
				Total:     stats.Total,
				Completed: stats.Completed,
			}
		}
	}
	c.JSON(http.StatusOK, responses)
}

// ListIssuesByProject returns all issues for a project across all columns
func (h *KanbanHandler) ListIssuesByProject(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	projectID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessProject(c.Request.Context(), userID, projectID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	issues, err := h.svc.ListIssuesByProject(c.Request.Context(), projectID)
	if err != nil {
		InternalError(c, err)
		return
	}
	responses := toIssueResponses(issues)
	for i, issue := range issues {
		responses[i].ProjectID = projectID
		stats, sErr := h.checklistSvc.GetStats(c.Request.Context(), issue.ID)
		if sErr == nil && stats.Total > 0 {
			responses[i].ChecklistStats = &ChecklistStatsDTO{
				Total:     stats.Total,
				Completed: stats.Completed,
			}
		}
	}
	c.JSON(http.StatusOK, responses)
}

// CreateIssue creates a new issue
// @Summary      Create an issue
// @Description  Create a new issue for a column
// @Tags         Issues
// @Accept       json
// @Produce      json
// @Param        id       path      string              true  "Column ID"
// @Param        request  body      CreateIssueRequest  true  "Issue details"
// @Success      201     {object}  IssueResponse
// @Failure      400     {object}  Error
// @Failure      500     {object}  Error
// @Router       /columns/{id}/issues [post]
func (h *KanbanHandler) CreateIssue(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	columnID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid column ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, ok := h.authzColumnProject(c, userID, columnID); !ok {
			return
		}
	}
	var req CreateIssueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if req.Priority < 0 || req.Priority > 3 {
		BadRequest(c, "priority must be between 0 and 3")
		return
	}
	detail, err := h.svc.CreateIssue(c.Request.Context(), columnID, req.Title, req.Description, req.Priority, req.Position, req.StartDate, req.DueDate, req.EstimateType, req.Estimate, req.ParentIssueID, req.IssueType, req.ExecWave, req.AssigneeUserIDs, req.LabelIDs, userID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidAssignee):
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_assignee", "message": err.Error()})
			return
		case errors.Is(err, service.ErrInvalidLabel):
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_label", "message": err.Error()})
			return
		case errors.Is(err, service.ErrTooMany):
			c.JSON(http.StatusBadRequest, gin.H{"code": "too_many", "message": err.Error()})
			return
		}
		InternalError(c, err)
		return
	}
	resp := toIssueResponse(detail)
	col, colErr := h.svc.GetColumn(c.Request.Context(), columnID)
	if colErr == nil {
		resp.ProjectID = col.ProjectID
	}

	// 建 issue 即起编排 (spec §13 stage 8 / §3): a card created directly into an
	// `instruct` column auto-starts its workspace + sends the column instruction.
	// Best-effort — the issue is already created, so an orchestration hiccup is
	// logged, not surfaced as a create failure (the user can drag the card to retry).
	// The batch path (BatchCreateIssues) deliberately does NOT call this (§3.1).
	if h.Orchestrator != nil {
		if _, oerr := h.Orchestrator.OnIssueCreated(c.Request.Context(), detail.ID, userID); oerr != nil {
			slog.Warn("create issue: auto-orchestration failed (card created)",
				"issueID", detail.ID, "columnID", columnID, "error", oerr)
		}
	}
	c.JSON(http.StatusCreated, resp)
}

// BatchCreateIssues creates multiple issues in the first column of a project.
type BatchCreateIssuesRequest struct {
	Tasks []service.BatchCreateIssuesTask `json:"tasks" binding:"required"`
}

func (h *KanbanHandler) BatchCreateIssues(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	projectID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessProject(c.Request.Context(), userID, projectID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	var req BatchCreateIssuesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	if len(req.Tasks) == 0 {
		BadRequest(c, "tasks array must not be empty")
		return
	}

	for i, task := range req.Tasks {
		if task.Title == "" {
			BadRequest(c, fmt.Sprintf("task %d: title is required", i))
			return
		}
		if task.Priority < 0 || task.Priority > 3 {
			BadRequest(c, fmt.Sprintf("task %d: priority must be between 0 and 3", i))
			return
		}
	}

	result, err := h.svc.BatchCreateIssues(c.Request.Context(), projectID, req.Tasks, userID)
	if err != nil {
		InternalError(c, err)
		return
	}

	c.JSON(http.StatusCreated, result)
}

// ListIssuesByProjectFiltered returns issues for a project with optional filters.
func (h *KanbanHandler) ListIssuesByProjectFiltered(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	projectID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessProject(c.Request.Context(), userID, projectID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	var params service.ListIssuesByProjectFilteredParams
	if v := c.Query("column_id"); v != "" {
		params.ColumnID, _ = strconv.ParseInt(v, 10, 64)
	}
	params.LifecycleStatus = c.Query("lifecycle_status")
	// Phase-1: structured label filtering moves to a dedicated route.
	// Accept ?label_ids=1,2,3 here as a transitional convenience so the
	// existing kanban filter UI keeps working until phase-2 ships.
	if s := c.Query("label_ids"); s != "" {
		for _, p := range strings.Split(s, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
				params.LabelIDs = append(params.LabelIDs, id)
			}
		}
	}
	params.Search = c.Query("search")

	issues, err := h.svc.ListIssuesByProjectFiltered(c.Request.Context(), projectID, params)
	if err != nil {
		InternalError(c, err)
		return
	}

	responses := toIssueResponses(issues)
	for i, issue := range issues {
		responses[i].ProjectID = projectID
		stats, sErr := h.checklistSvc.GetStats(c.Request.Context(), issue.ID)
		if sErr == nil && stats.Total > 0 {
			responses[i].ChecklistStats = &ChecklistStatsDTO{
				Total:     stats.Total,
				Completed: stats.Completed,
			}
		}
	}
	c.JSON(http.StatusOK, responses)
}

// GetIssue returns a single issue by ID
// @Summary      Get an issue
// @Description  Get issue details by ID
// @Tags         Issues
// @Produce      json
// @Param        id   path      string  true  "Issue ID"
// @Success      200  {object}  IssueResponse
// @Failure      400  {object}  Error
// @Failure      404  {object}  Error
// @Failure      500  {object}  Error
// @Router       /issues/{id} [get]
func (h *KanbanHandler) GetIssue(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessIssue(c.Request.Context(), userID, issueID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	issue, err := h.svc.GetIssue(c.Request.Context(), issueID)
	if err != nil {
		slog.Warn("GetIssue failed", "issueID", issueID, "error", err)
		NotFound(c, "ISSUE")
		return
	}
	resp := toIssueResponse(issue)
	// project_id and checklist stats now ride along with the detail load
	// (single composite query inside loadIssueDetail), so no separate
	// GetColumn / GetChecklistStats round trips are needed here.
	resp.ProjectID = issue.ProjectID
	if issue.ChecklistTotal > 0 {
		resp.ChecklistStats = &ChecklistStatsDTO{
			Total:     issue.ChecklistTotal,
			Completed: issue.ChecklistCompleted,
		}
	}

	c.JSON(http.StatusOK, resp)
}

// UpdateIssue updates an existing issue
// @Summary      Update an issue
// @Description  Update issue details
// @Tags         Issues
// @Accept       json
// @Produce      json
// @Param        id       path      string              true  "Issue ID"
// @Param        request  body      UpdateIssueRequest  true  "Updated issue details"
// @Success      200      {object}  IssueResponse
// @Failure      400      {object}  Error
// @Failure      404      {object}  Error
// @Failure      500      {object}  Error
// @Router       /issues/{id} [put]
func (h *KanbanHandler) UpdateIssue(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessIssue(c.Request.Context(), userID, issueID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req UpdateIssueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if req.Priority < 0 || req.Priority > 3 {
		BadRequest(c, "priority must be between 0 and 3")
		return
	}
	detail, err := h.svc.UpdateIssue(c.Request.Context(), issueID, req.Title, req.Description, req.Priority, req.StartDate, req.DueDate, req.EstimateType, req.Estimate, req.ActualTime, req.GoalCondition)
	if err != nil {
		slog.Warn("UpdateIssue failed", "issueID", issueID, "error", err)
		NotFound(c, "ISSUE")
		return
	}
	resp := toIssueResponse(detail)
	col, colErr := h.svc.GetColumn(c.Request.Context(), detail.ColumnID)
	if colErr == nil {
		resp.ProjectID = col.ProjectID
	}
	c.JSON(http.StatusOK, resp)
}

// SuggestGoalConditionResponse is the wire format for the AI-suggest endpoint.
type SuggestGoalConditionResponse struct {
	Suggestion string `json:"suggestion"`
}

// SuggestGoalCondition asks haiku to draft a one-line goal_condition from the
// issue's title + description. The suggestion is NOT persisted — the SPA
// fills its textarea and the user decides whether to keep it (blur-save) or
// edit/discard. Per-user rate-limit is applied at the route layer.
//
// @Summary      AI-suggest a goal_condition for an issue
// @Description  Spawn `claude -p --model haiku` to propose a completion criterion
// @Tags         Issues
// @Produce      json
// @Param        id   path      string  true  "Issue ID"
// @Success      200  {object}  SuggestGoalConditionResponse
// @Failure      400  {object}  Error
// @Failure      403  {object}  Error
// @Failure      404  {object}  Error
// @Failure      429  {object}  Error
// @Failure      502  {object}  Error
// @Failure      503  {object}  Error
// @Router       /issues/{id}/suggest-goal-condition [post]
func (h *KanbanHandler) SuggestGoalCondition(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessIssue(c.Request.Context(), userID, issueID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	if !agentproxy.ClaudeCLIAvailable() {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": gin.H{"code": "CLI_UNAVAILABLE", "message": "claude CLI not available"},
		})
		return
	}
	detail, err := h.svc.GetIssue(c.Request.Context(), issueID)
	if err != nil {
		slog.Warn("SuggestGoalCondition: issue lookup failed",
			"user_id", userID, "issue_id", issueID, "error", err)
		NotFound(c, "ISSUE")
		return
	}

	// Resolve a Claude account CLAUDE_CONFIG_DIR for this user so the
	// subprocess inherits the same auth niuniu's workspace agents use.
	// Empty → fall back to claude CLI's native home; on most niuniu hosts
	// that won't have working creds and the call will fail with 502.
	//
	// Keep the full account around (not just configDir) so failure paths
	// can name the account that was used — opaque "exit status 1" wasted
	// hours of operator time when the cause was a wrong account selection
	// (rate-limit, expired auth, etc.). accountLabel is the human-readable
	// "<name> (id=<id>)" or "default ~/.claude" so logs and 502 bodies
	// point at the exact account, not just the config_dir path.
	configDir := ""
	accountLabel := "default ~/.claude"
	if h.ClaudeAccount != nil {
		if acc := h.ClaudeAccount.ResolveAccountForUser(c.Request.Context(), userID); acc != nil {
			configDir = acc.ConfigDir
			accountLabel = fmt.Sprintf("%s (id=%d)", acc.Name, acc.ID)
		}
	}
	suggestion, err := agentproxy.SuggestGoalCondition(
		c.Request.Context(),
		detail.Title,
		detail.Description.String,
		configDir,
	)
	if err != nil {
		// CLI may have flipped to unavailable between the entry check and
		// the spawn — surface 503 rather than a generic 502 so the SPA can
		// show the right toast.
		if !agentproxy.ClaudeCLIAvailable() {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": gin.H{"code": "CLI_UNAVAILABLE", "message": "claude CLI not available"},
			})
			return
		}
		slog.Warn("SuggestGoalCondition failed",
			"user_id", userID, "issue_id", issueID,
			"claude_account", accountLabel, "error", err)
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"code":           "SUGGEST_FAILED",
				"message":        fmt.Sprintf("[account %s] %s", accountLabel, err.Error()),
				"claude_account": accountLabel,
			},
		})
		return
	}
	slog.Info("SuggestGoalCondition ok",
		"user_id", userID, "issue_id", issueID,
		"claude_account", accountLabel, "len", len(suggestion))
	c.JSON(http.StatusOK, gin.H{
		"data": SuggestGoalConditionResponse{Suggestion: suggestion},
	})
}

// MoveIssue moves an issue to another column
// @Summary      Move an issue
// @Description  Move an issue to a different column and/or position
// @Tags         Issues
// @Accept       json
// @Produce      json
// @Param        id       path      string            true  "Issue ID"
// @Param        request  body      MoveIssueRequest  true  "New column and position"
// @Success      204
// @Failure      400      {object}  Error
// @Failure      500      {object}  Error
// @Router       /issues/{id}/move [put]
func (h *KanbanHandler) MoveIssue(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	var req MoveIssueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if userID > 0 && h.Authz != nil {
		// Gate via the issue's current project (from-column).
		if _, aerr := h.Authz.CanAccessIssue(c.Request.Context(), userID, issueID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
		// Verify the target column belongs to the same project.
		toCol, colErr := h.svc.GetColumn(c.Request.Context(), req.ColumnID)
		if colErr != nil {
			NotFound(c, "COLUMN")
			return
		}
		issue, issErr := h.svc.GetIssue(c.Request.Context(), issueID)
		if issErr != nil {
			NotFound(c, "ISSUE")
			return
		}
		fromCol, fromErr := h.svc.GetColumn(c.Request.Context(), issue.ColumnID)
		if fromErr != nil {
			NotFound(c, "COLUMN")
			return
		}
		if toCol.ProjectID != fromCol.ProjectID {
			BadRequest(c, "target column belongs to a different project")
			return
		}
	}
	if err := h.svc.MoveIssue(c.Request.Context(), issueID, req.ColumnID, req.Position, userID); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// DeleteIssue removes an issue
// @Summary      Delete an issue
// @Description  Delete an issue
// @Tags         Issues
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Issue ID"
// @Success      204
// @Failure      500  {object}  Error
// @Router       /issues/{id} [delete]
func (h *KanbanHandler) DeleteIssue(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessIssue(c.Request.Context(), userID, issueID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	if err := h.svc.DeleteIssue(c.Request.Context(), issueID); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *KanbanHandler) UpdateIssueLifecycle(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue id")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessIssue(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req UpdateLifecycleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if _, err := h.svc.UpdateIssueLifecycle(c.Request.Context(), id, req.LifecycleStatus); err != nil {
		if strings.Contains(err.Error(), "invalid lifecycle status") {
			BadRequest(c, err.Error())
		} else {
			InternalError(c, err)
		}
		return
	}
	// Reload as IssueDetail so the response shape matches all other issue
	// endpoints (assignees + labels arrays populated).
	detail, err := h.svc.GetIssue(c.Request.Context(), id)
	if err != nil {
		InternalError(c, err)
		return
	}
	resp := toIssueResponse(detail)
	col, colErr := h.svc.GetColumn(c.Request.Context(), detail.ColumnID)
	if colErr == nil {
		resp.ProjectID = col.ProjectID
	}
	c.JSON(http.StatusOK, resp)
}

// LifecycleGroupDTO represents a lifecycle phase group for sidebar display.
type LifecycleGroupDTO struct {
	Label    string   `json:"label"`
	Statuses []string `json:"statuses"`
}

func (h *KanbanHandler) ListLifecycleGroups(c *gin.Context) {
	projectIDStr := c.Query("project_id")
	if projectIDStr == "" {
		// No project filter: return fallback groups
		fallback := []LifecycleGroupDTO{
			{Label: "Backlog", Statuses: []string{"created"}},
			{Label: "Spec·Plan", Statuses: []string{"spec", "spec-review", "plan", "plan-review"}},
			{Label: "Implement", Statuses: []string{"implement"}},
			{Label: "Review", Statuses: []string{"implement-review"}},
			{Label: "Test", Statuses: []string{"test"}},
		}
		c.JSON(http.StatusOK, fallback)
		return
	}

	projectID, err := strconv.ParseInt(projectIDStr, 10, 64)
	if err != nil {
		BadRequest(c, "invalid project_id")
		return
	}
	if userID := c.GetInt64("auth_user_id"); userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessProject(c.Request.Context(), userID, projectID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	columns, err := h.svc.ListColumns(c.Request.Context(), projectID)
	if err != nil {
		InternalError(c, err)
		return
	}

	groups := make([]LifecycleGroupDTO, 0)
	for _, col := range columns {
		if col.LifecycleMapping == "" {
			continue
		}
		// Skip Done (completed) — keep Backlog (created) visible in sidebar
		statuses := strings.Split(col.LifecycleMapping, ",")
		for i := range statuses {
			statuses[i] = strings.TrimSpace(statuses[i])
		}
		if len(statuses) == 1 && statuses[0] == "completed" {
			continue
		}
		groups = append(groups, LifecycleGroupDTO{
			Label:    col.Name,
			Statuses: statuses,
		})
	}
	c.JSON(http.StatusOK, groups)
}

func (h *KanbanHandler) UpdateColumnLifecycleMapping(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	columnID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid column ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, ok := h.authzColumnProject(c, userID, columnID); !ok {
			return
		}
	}
	var req UpdateColumnLifecycleMappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	column, err := h.svc.UpdateColumnLifecycleMapping(c.Request.Context(), columnID, req.LifecycleMapping)
	if err != nil {
		if strings.Contains(err.Error(), "invalid lifecycle status") {
			BadRequest(c, err.Error())
		} else {
			InternalError(c, err)
		}
		return
	}
	c.JSON(http.StatusOK, toColumnDTO(column))
}

// ─── Batch issue mutation handlers ───────────────────────────────────────────
// Each handler partitions input ids into "authorized" and "skipped:forbidden"
// via authorizeIssues, calls the service for the authorized set, then merges
// authz-skipped entries back into the response. HTTP is 200 even when some
// (or all) ids are skipped — clients read res.skipped to surface a toast.

type batchMoveRequest struct {
	IssueIDs []int64 `json:"issue_ids" binding:"required"`
	ColumnID int64   `json:"column_id" binding:"required"`
}
type batchPriorityRequest struct {
	IssueIDs []int64 `json:"issue_ids" binding:"required"`
	Priority int64   `json:"priority"`
}
type batchDeleteRequest struct {
	IssueIDs []int64 `json:"issue_ids" binding:"required"`
}

// authorizeIssues partitions issueIDs into (authorized, skipped). When auth
// is disabled (userID==0 or Authz nil), every id is authorized. Used by all
// four batch endpoints (and BatchSetIssueLabels in label.go).
func (h *KanbanHandler) authorizeIssues(c *gin.Context, userID int64, ids []int64) ([]int64, []service.SkippedItem) {
	if userID <= 0 || h.Authz == nil {
		return ids, nil
	}
	ok := make([]int64, 0, len(ids))
	skipped := make([]service.SkippedItem, 0)
	for _, id := range ids {
		if _, err := h.Authz.CanAccessIssue(c.Request.Context(), userID, id); err != nil {
			skipped = append(skipped, service.SkippedItem{ID: id, Reason: "forbidden"})
			continue
		}
		ok = append(ok, id)
	}
	return ok, skipped
}

// BatchMoveIssues moves multiple issues to a target column (appended to end,
// input order preserved).
func (h *KanbanHandler) BatchMoveIssues(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req batchMoveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if userID > 0 && h.Authz != nil {
		// Verify caller can write to the target column's project.
		col, err := h.svc.GetColumn(c.Request.Context(), req.ColumnID)
		if err != nil {
			NotFound(c, "COLUMN")
			return
		}
		if _, aerr := h.Authz.CanAccessProject(c.Request.Context(), userID, col.ProjectID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	ok, skipped := h.authorizeIssues(c, userID, req.IssueIDs)
	res, err := h.svc.BatchMoveIssues(c.Request.Context(), ok, req.ColumnID, userID)
	if err != nil {
		InternalError(c, err)
		return
	}
	res.Skipped = append(res.Skipped, skipped...)
	c.JSON(http.StatusOK, res)
}

// BatchUpdatePriority sets priority on multiple issues.
func (h *KanbanHandler) BatchUpdatePriority(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req batchPriorityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if req.Priority < 0 || req.Priority > 3 {
		BadRequest(c, "priority must be between 0 and 3")
		return
	}
	ok, skipped := h.authorizeIssues(c, userID, req.IssueIDs)
	res, err := h.svc.BatchUpdatePriority(c.Request.Context(), ok, req.Priority)
	if err != nil {
		InternalError(c, err)
		return
	}
	res.Skipped = append(res.Skipped, skipped...)
	c.JSON(http.StatusOK, res)
}

// BatchDeleteIssues hard-deletes multiple issues.
func (h *KanbanHandler) BatchDeleteIssues(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req batchDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	ok, skipped := h.authorizeIssues(c, userID, req.IssueIDs)
	res, err := h.svc.BatchDeleteIssues(c.Request.Context(), ok)
	if err != nil {
		InternalError(c, err)
		return
	}
	res.Skipped = append(res.Skipped, skipped...)
	c.JSON(http.StatusOK, res)
}
