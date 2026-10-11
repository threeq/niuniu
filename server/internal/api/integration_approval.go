package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// IntegrationApprovalHandler exposes the team-edition integration authorization
// approval queue plus project-level role management. Routes live under the
// license-gated /orgs group (approvals) and the project group (roles), so the
// personal edition never reaches them.
//
// The service enforces the role matrix; the handler only maps results. Hiding a
// button in the SPA is convenience — the API is authoritative.
type IntegrationApprovalHandler struct {
	svc     *service.IntegrationApprovalService
	projSvc *service.ProjectMemberService
	authz   *service.Authz
}

func NewIntegrationApprovalHandler(svc *service.IntegrationApprovalService, projSvc *service.ProjectMemberService, authz *service.Authz) *IntegrationApprovalHandler {
	return &IntegrationApprovalHandler{svc: svc, projSvc: projSvc, authz: authz}
}

type integrationApprovalDTO struct {
	ID              int64      `json:"id"`
	OrgID           int64      `json:"org_id"`
	ProjectID       *int64     `json:"project_id"`
	Action          string     `json:"action"`
	SubjectType     string     `json:"subject_type"`
	SubjectID       int64      `json:"subject_id"`
	SubjectLabel    string     `json:"subject_label"`
	Diff            string     `json:"diff"`
	Summary         string     `json:"summary"`
	RequestedBy     int64      `json:"requested_by"`
	RequiredRole    string     `json:"required_role"`
	Status          string     `json:"status"`
	DecidedBy       *int64     `json:"decided_by"`
	DecidedAt       *time.Time `json:"decided_at"`
	DecisionNote    string     `json:"decision_note"`
	EscalationCount int64      `json:"escalation_count"`
	ExpiresAt       time.Time  `json:"expires_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

func toApprovalDTO(r store.IntegrationApprovalRequest) integrationApprovalDTO {
	dto := integrationApprovalDTO{
		ID:              r.ID,
		OrgID:           r.OwnerID,
		Action:          r.Action,
		SubjectType:     r.SubjectType,
		SubjectID:       r.SubjectID,
		SubjectLabel:    r.SubjectLabel,
		Diff:            r.Diff,
		Summary:         r.Summary,
		RequestedBy:     r.RequestedBy,
		RequiredRole:    r.RequiredRole,
		Status:          r.Status,
		DecisionNote:    r.DecisionNote,
		EscalationCount: r.EscalationCount,
		ExpiresAt:       r.ExpiresAt,
		CreatedAt:       r.CreatedAt,
	}
	if r.ProjectID.Valid {
		v := r.ProjectID.Int64
		dto.ProjectID = &v
	}
	if r.DecidedBy.Valid {
		v := r.DecidedBy.Int64
		dto.DecidedBy = &v
	}
	if r.DecidedAt.Valid {
		v := r.DecidedAt.Time
		dto.DecidedAt = &v
	}
	return dto
}

func respondApprovalErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		NotFound(c, "approval request not found")
	case errors.Is(err, service.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "FORBIDDEN", "message": "无权执行该操作"}})
	case errors.Is(err, service.ErrApprovalNotPermitted):
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "NOT_PERMITTED", "message": "当前角色无权批准该集成授权动作"}})
	case errors.Is(err, service.ErrApprovalSelfDecide):
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "SELF_DECIDE", "message": "发起人不能批准自己的申请"}})
	case errors.Is(err, service.ErrApprovalAlreadyDecided):
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"code": "ALREADY_DECIDED", "message": "该申请已处理"}})
	case errors.Is(err, service.ErrApprovalOrgOnly):
		BadRequest(c, "集成审批仅适用于组织（团队版）资源")
	default:
		InternalError(c, err)
	}
}

// POST /api/orgs/:id/integration-approvals
func (h *IntegrationApprovalHandler) Submit(c *gin.Context) {
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	orgID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid org ID")
		return
	}
	var body struct {
		Action       string `json:"action" binding:"required"`
		ProjectID    int64  `json:"project_id"`
		SubjectType  string `json:"subject_type"`
		SubjectID    int64  `json:"subject_id"`
		SubjectLabel string `json:"subject_label"`
		Diff         string `json:"diff"`
		Summary      string `json:"summary"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	req, err := h.svc.Submit(c.Request.Context(), service.SubmitApprovalInput{
		OwnerType:    "org",
		OwnerID:      orgID,
		ProjectID:    body.ProjectID,
		Action:       service.ApprovalAction(body.Action),
		SubjectType:  body.SubjectType,
		SubjectID:    body.SubjectID,
		SubjectLabel: body.SubjectLabel,
		Diff:         body.Diff,
		Summary:      body.Summary,
		RequestedBy:  callerID,
	})
	if err != nil {
		respondApprovalErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, toApprovalDTO(req))
}

// GET /api/orgs/:id/integration-approvals?status=pending
func (h *IntegrationApprovalHandler) ListByOrg(c *gin.Context) {
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	orgID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid org ID")
		return
	}
	rows, err := h.svc.ListByOwner(c.Request.Context(), callerID, orgID, c.Query("status"))
	if err != nil {
		respondApprovalErr(c, err)
		return
	}
	out := make([]integrationApprovalDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toApprovalDTO(r))
	}
	c.JSON(http.StatusOK, out)
}

// GET /api/orgs/:id/integration-approvals/:req_id
func (h *IntegrationApprovalHandler) Get(c *gin.Context) {
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	reqID, err := strconv.ParseInt(c.Param("req_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid request ID")
		return
	}
	req, err := h.svc.Get(c.Request.Context(), callerID, reqID)
	if err != nil {
		respondApprovalErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toApprovalDTO(req))
}

// POST /api/orgs/:id/integration-approvals/:req_id/decision
func (h *IntegrationApprovalHandler) Decide(c *gin.Context) {
	callerID, callerLabel, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	reqID, err := strconv.ParseInt(c.Param("req_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid request ID")
		return
	}
	// Pointer so a missing `approve` is distinguishable from `false`: a
	// zero-value bool would silently record a denial (and denials are terminal).
	var body struct {
		Approve *bool  `json:"approve"`
		Note    string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if body.Approve == nil {
		BadRequest(c, "approve is required")
		return
	}
	req, err := h.svc.Decide(c.Request.Context(), callerID, callerLabel, reqID, *body.Approve, body.Note)
	if err != nil {
		respondApprovalErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toApprovalDTO(req))
}

// DELETE /api/orgs/:id/integration-approvals/:req_id — requester or org admin.
func (h *IntegrationApprovalHandler) Cancel(c *gin.Context) {
	callerID, callerLabel, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	reqID, err := strconv.ParseInt(c.Param("req_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid request ID")
		return
	}
	req, err := h.svc.Cancel(c.Request.Context(), callerID, callerLabel, reqID)
	if err != nil {
		respondApprovalErr(c, err)
		return
	}
	c.JSON(http.StatusOK, toApprovalDTO(req))
}

// GET /api/me/integration-approvals — pending requests this caller may decide.
func (h *IntegrationApprovalHandler) ListMine(c *gin.Context) {
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	rows, err := h.svc.ListPendingForApprover(c.Request.Context(), callerID)
	if err != nil {
		respondApprovalErr(c, err)
		return
	}
	out := make([]integrationApprovalDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toApprovalDTO(r))
	}
	c.JSON(http.StatusOK, out)
}

// ---- project-level roles -----------------------------------------------------

// GET /api/projects/:id/members
func (h *IntegrationApprovalHandler) ListProjectMembers(c *gin.Context) {
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	projectID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	rows, err := h.projSvc.List(c.Request.Context(), callerID, projectID)
	if err != nil {
		respondApprovalErr(c, err)
		return
	}
	c.JSON(http.StatusOK, rows)
}

// POST /api/projects/:id/members
func (h *IntegrationApprovalHandler) GrantProjectMember(c *gin.Context) {
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	projectID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	var body struct {
		UserID int64 `json:"user_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.projSvc.Grant(c.Request.Context(), callerID, projectID, body.UserID); err != nil {
		respondApprovalErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// DELETE /api/projects/:id/members/:user_id
func (h *IntegrationApprovalHandler) RevokeProjectMember(c *gin.Context) {
	callerID, _, ok := callerInfo(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "UNAUTHORIZED", "message": "not authenticated"}})
		return
	}
	projectID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid project ID")
		return
	}
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid user_id")
		return
	}
	if err := h.projSvc.Revoke(c.Request.Context(), callerID, projectID, userID); err != nil {
		respondApprovalErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
