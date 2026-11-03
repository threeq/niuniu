// Intake (任务来源) HTTP surface — design §6.9.
//
// Project-scoped routes require CanAccessProject for reads and
// EnsureOwnerWritable for writes. The decision routes under /mcp/... address a
// decision by id alone, so they resolve the owning project from the decision
// before applying the same checks (an agent may only touch its own project).
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// IntakeHandler serves intake settings + the dry-run calibration queue.
type IntakeHandler struct {
	svc   *service.IntakeService
	authz *service.Authz
}

func NewIntakeHandler(svc *service.IntakeService, authz *service.Authz) *IntakeHandler {
	return &IntakeHandler{svc: svc, authz: authz}
}

// intakeProjectAccess parses :id and enforces read (write=false) or write access.
func (h *IntakeHandler) intakeProjectAccess(c *gin.Context, write bool) (projectID int64, ok bool) {
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || pid <= 0 {
		BadRequest(c, "invalid project ID")
		return 0, false
	}
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return 0, false
	}
	owner, err := h.authz.CanAccessProject(c.Request.Context(), uid, pid)
	if err != nil {
		writeAuthzError(c, err)
		return 0, false
	}
	if write {
		if err := h.authz.EnsureOwnerWritable(c.Request.Context(), uid, owner); err != nil {
			writeAuthzError(c, err)
			return 0, false
		}
	}
	return pid, true
}

func (h *IntakeHandler) mapIntakeErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrDecisionAcceptorUnavailable):
		c.JSON(http.StatusNotImplemented, gin.H{
			"error": gin.H{
				"code":    "INTAKE_ACCEPTOR_UNAVAILABLE",
				"message": "the intake decision pipeline is not wired yet; accept will create the issue once it lands",
			},
		})
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, service.ErrNotFound):
		NotFound(c, "DECISION")
	case errors.Is(err, service.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	default:
		BadRequest(c, err.Error())
	}
}

// GetSettings serves GET /api/projects/:id/intake/settings.
//
// @Summary      Read a project's intake settings
// @Description  One row per source kind (im_bot/github/ci/email/webhook); kinds without a stored row are rendered with defaults.
// @Tags         Intake
// @Produce      json
// @Param        id   path      int  true  "Project ID"
// @Success      200  {object}  service.IntakeSettingsView
// @Failure      401  {object}  Error
// @Failure      403  {object}  Error
// @Router       /projects/{id}/intake/settings [get]
func (h *IntakeHandler) GetSettings(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, false)
	if !ok {
		return
	}
	view, err := h.svc.GetSettings(c.Request.Context(), pid)
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// PutSettings serves PUT /api/projects/:id/intake/settings.
//
// @Summary      Write a project's intake settings
// @Description  Upserts each supplied source row and records one policy version per touched source.
// @Tags         Intake
// @Accept       json
// @Produce      json
// @Param        id    path      int                                true  "Project ID"
// @Param        body  body      service.UpdateIntakeSettingsRequest true  "Settings to upsert"
// @Success      200   {object}  service.IntakeSettingsView
// @Failure      400   {object}  Error
// @Failure      401   {object}  Error
// @Failure      403   {object}  Error
// @Router       /projects/{id}/intake/settings [put]
func (h *IntakeHandler) PutSettings(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	var req service.UpdateIntakeSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	view, err := h.svc.UpdateSettings(c.Request.Context(), pid, req)
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// SetPause serves PUT /api/projects/:id/intake/pause.
//
// @Summary      Pause or resume intake for a project
// @Description  While paused the pipeline forces every source to mode off: explicit requests keep working, inferred/observe work stops immediately.
// @Tags         Intake
// @Accept       json
// @Produce      json
// @Param        id    path      int  true  "Project ID"
// @Param        body  body      object{paused=bool}  true  "Pause state"
// @Success      200   {object}  object{paused=bool}
// @Failure      400   {object}  Error
// @Failure      401   {object}  Error
// @Failure      403   {object}  Error
// @Router       /projects/{id}/intake/pause [put]
func (h *IntakeHandler) SetPause(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	var body struct {
		Paused *bool `json:"paused"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Paused == nil {
		BadRequest(c, "paused is required")
		return
	}
	paused, err := h.svc.SetPaused(c.Request.Context(), pid, *body.Paused)
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"paused": paused})
}

// ListRules serves GET /api/projects/:id/intake/rules.
//
// @Summary      List intake rules
// @Description  Rules for a project, optionally filtered by source_kind, priority order.
// @Tags         Intake
// @Produce      json
// @Param        id           path      int     true   "Project ID"
// @Param        source_kind  query     string  false  "Filter by source kind"
// @Success      200  {array}   service.IntakeRuleDTO
// @Failure      401  {object}  Error
// @Failure      403  {object}  Error
// @Router       /projects/{id}/intake/rules [get]
func (h *IntakeHandler) ListRules(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, false)
	if !ok {
		return
	}
	items, err := h.svc.ListRules(c.Request.Context(), pid, c.Query("source_kind"))
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, items)
}

// CreateRule serves POST /api/projects/:id/intake/rules.
//
// @Summary      Create an intake rule
// @Description  Adds a rule and records a policy version for its source.
// @Tags         Intake
// @Accept       json
// @Produce      json
// @Param        id    path      int                            true  "Project ID"
// @Param        body  body      service.UpsertIntakeRuleInput  true  "Rule"
// @Success      201   {object}  service.IntakeRuleDTO
// @Failure      400   {object}  Error
// @Failure      401   {object}  Error
// @Failure      403   {object}  Error
// @Router       /projects/{id}/intake/rules [post]
func (h *IntakeHandler) CreateRule(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	var in service.UpsertIntakeRuleInput
	if err := c.ShouldBindJSON(&in); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	item, err := h.svc.CreateRule(c.Request.Context(), pid, in)
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, item)
}

// UpdateRule serves PUT /api/projects/:id/intake/rules/:rid.
//
// @Summary      Update an intake rule
// @Description  Edits a rule in place and records a policy version for its source.
// @Tags         Intake
// @Accept       json
// @Produce      json
// @Param        id    path      int                            true  "Project ID"
// @Param        rid   path      int                            true  "Rule ID"
// @Param        body  body      service.UpsertIntakeRuleInput  true  "Rule"
// @Success      200   {object}  service.IntakeRuleDTO
// @Failure      400   {object}  Error
// @Failure      401   {object}  Error
// @Failure      403   {object}  Error
// @Failure      404   {object}  Error
// @Router       /projects/{id}/intake/rules/{rid} [put]
func (h *IntakeHandler) UpdateRule(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	rid, ok := ruleIDParam(c)
	if !ok {
		return
	}
	var in service.UpsertIntakeRuleInput
	if err := c.ShouldBindJSON(&in); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	item, err := h.svc.UpdateRule(c.Request.Context(), pid, rid, in)
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

// DeleteRule serves DELETE /api/projects/:id/intake/rules/:rid.
//
// @Summary      Delete an intake rule
// @Description  Removes a rule and records a policy version for its source.
// @Tags         Intake
// @Param        id   path      int  true  "Project ID"
// @Param        rid  path      int  true  "Rule ID"
// @Success      204  "No Content"
// @Failure      401  {object}  Error
// @Failure      403  {object}  Error
// @Failure      404  {object}  Error
// @Router       /projects/{id}/intake/rules/{rid} [delete]
func (h *IntakeHandler) DeleteRule(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	rid, ok := ruleIDParam(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteRule(c.Request.Context(), pid, rid); err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ListPolicyVersions serves GET /api/projects/:id/intake/policy-versions.
//
// @Summary      List intake policy versions
// @Description  Auditable snapshots for one source, newest first.
// @Tags         Intake
// @Produce      json
// @Param        id           path      int     true   "Project ID"
// @Param        source_kind  query     string  true   "Source kind"
// @Param        limit        query     int     false  "Max rows (default 50)"
// @Success      200  {array}   service.IntakePolicyVersionDTO
// @Failure      400  {object}  Error
// @Failure      401  {object}  Error
// @Failure      403  {object}  Error
// @Router       /projects/{id}/intake/policy-versions [get]
func (h *IntakeHandler) ListPolicyVersions(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, false)
	if !ok {
		return
	}
	items, err := h.svc.ListPolicyVersions(c.Request.Context(), pid, c.Query("source_kind"), intParam(c, "limit"))
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, items)
}

// ListDecisions serves GET /api/projects/:id/intake/decisions.
//
// @Summary      List intake decisions (calibration queue)
// @Description  Joins each decision with its event. Query: executed=true|false, decision=accept|resume|ignore|unavailable, limit.
// @Tags         Intake
// @Produce      json
// @Param        id        path      int   true   "Project ID"
// @Param        executed  query     bool  false  "Filter by execution state"
// @Param        decision  query     string false  "Filter by decision outcome"
// @Param        limit     query     int   false  "Max rows (default 100)"
// @Success      200  {array}   service.IntakeDecisionDTO
// @Failure      400  {object}  Error
// @Failure      401  {object}  Error
// @Failure      403  {object}  Error
// @Router       /projects/{id}/intake/decisions [get]
func (h *IntakeHandler) ListDecisions(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, false)
	if !ok {
		return
	}
	items, err := h.svc.ListDecisions(c.Request.Context(), pid, executedFilter(c), c.Query("decision"), intParam(c, "limit"))
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, items)
}

// AcceptDecision serves POST /api/projects/:id/intake/decisions/:did/accept.
//
// @Summary      Accept a dry-run intake decision
// @Description  Executes the decision through the intake pipeline, creating the issue/workspace. Returns 501 until the pipeline is wired.
// @Tags         Intake
// @Produce      json
// @Param        id   path      int  true  "Project ID"
// @Param        did  path      int  true  "Decision ID"
// @Success      200  {object}  service.IntakeDecisionDTO
// @Failure      400  {object}  Error
// @Failure      401  {object}  Error
// @Failure      403  {object}  Error
// @Failure      404  {object}  Error
// @Failure      501  {object}  Error
// @Router       /projects/{id}/intake/decisions/{did}/accept [post]
func (h *IntakeHandler) AcceptDecision(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	did, ok := decisionIDParam(c)
	if !ok {
		return
	}
	if !h.decisionInProject(c, pid, did) {
		return
	}
	item, err := h.svc.AcceptDecision(c.Request.Context(), did)
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

// IgnoreDecision serves POST /api/projects/:id/intake/decisions/:did/ignore.
//
// @Summary      Ignore a dry-run intake decision
// @Description  Records why the decision was rejected; a reason is mandatory.
// @Tags         Intake
// @Accept       json
// @Produce      json
// @Param        id    path      int                            true  "Project ID"
// @Param        did   path      int                            true  "Decision ID"
// @Param        body  body      service.IgnoreDecisionInput    true  "Reason"
// @Success      200   {object}  service.IntakeDecisionDTO
// @Failure      400   {object}  Error
// @Failure      401   {object}  Error
// @Failure      403   {object}  Error
// @Failure      404   {object}  Error
// @Router       /projects/{id}/intake/decisions/{did}/ignore [post]
func (h *IntakeHandler) IgnoreDecision(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	did, ok := decisionIDParam(c)
	if !ok {
		return
	}
	if !h.decisionInProject(c, pid, did) {
		return
	}
	var in service.IgnoreDecisionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	item, err := h.svc.IgnoreDecision(c.Request.Context(), did, in)
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

// ─── MCP tunnel endpoints (/mcp/...) ─────────────────────────────────────────

// MCPGetSettings backs intake_get_settings.
func (h *IntakeHandler) MCPGetSettings(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, false)
	if !ok {
		return
	}
	view, err := h.svc.GetSettings(c.Request.Context(), pid)
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// MCPSetMode backs intake_set_mode.
func (h *IntakeHandler) MCPSetMode(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	var body struct {
		SourceKind string `json:"source_kind"`
		Mode       string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	setting, err := h.svc.SetMode(c.Request.Context(), pid, body.SourceKind, body.Mode)
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, setting)
}

// MCPListPending backs intake_list_pending: pending accept/resume decisions.
func (h *IntakeHandler) MCPListPending(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, false)
	if !ok {
		return
	}
	executed := false
	items, err := h.svc.ListDecisions(c.Request.Context(), pid, &executed, "accept", intParam(c, "limit"))
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, items)
}

// MCPAcceptDecision backs intake_accept_decision (addressed by decision id).
func (h *IntakeHandler) MCPAcceptDecision(c *gin.Context) {
	did, ok := decisionIDParam(c)
	if !ok {
		return
	}
	if !h.mcpDecisionAccess(c, did) {
		return
	}
	item, err := h.svc.AcceptDecision(c.Request.Context(), did)
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

// MCPIgnoreDecision backs intake_ignore_decision.
func (h *IntakeHandler) MCPIgnoreDecision(c *gin.Context) {
	did, ok := decisionIDParam(c)
	if !ok {
		return
	}
	if !h.mcpDecisionAccess(c, did) {
		return
	}
	var body struct {
		Reason     string `json:"reason"`
		ReasonCode string `json:"reason_code"`
		ReasonText string `json:"reason_text"`
	}
	if err := c.ShouldBindJSON(&body); err != nil && c.Request.ContentLength > 0 {
		BadRequest(c, "invalid request body")
		return
	}
	reasonText := body.ReasonText
	if reasonText == "" {
		reasonText = body.Reason
	}
	item, err := h.svc.IgnoreDecision(c.Request.Context(), did, service.IgnoreDecisionInput{
		ReasonCode: body.ReasonCode,
		ReasonText: reasonText,
	})
	if err != nil {
		h.mapIntakeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

// mcpDecisionAccess resolves the decision's project and enforces write access.
func (h *IntakeHandler) mcpDecisionAccess(c *gin.Context, did int64) bool {
	d, err := h.svc.GetDecision(c.Request.Context(), did)
	if err != nil {
		h.mapIntakeErr(c, err)
		return false
	}
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return false
	}
	owner, err := h.authz.CanAccessProject(c.Request.Context(), uid, d.ProjectID)
	if err != nil {
		writeAuthzError(c, err)
		return false
	}
	if err := h.authz.EnsureOwnerWritable(c.Request.Context(), uid, owner); err != nil {
		writeAuthzError(c, err)
		return false
	}
	return true
}

// decisionInProject returns 404 unless the decision belongs to projectID.
func (h *IntakeHandler) decisionInProject(c *gin.Context, projectID, decisionID int64) bool {
	d, err := h.svc.GetDecision(c.Request.Context(), decisionID)
	if err != nil {
		h.mapIntakeErr(c, err)
		return false
	}
	if d.ProjectID != projectID {
		NotFound(c, "DECISION")
		return false
	}
	return true
}

func ruleIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("rid"), 10, 64)
	if err != nil || id <= 0 {
		BadRequest(c, "invalid rule ID")
		return 0, false
	}
	return id, true
}

func decisionIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("did"), 10, 64)
	if err != nil || id <= 0 {
		BadRequest(c, "invalid decision ID")
		return 0, false
	}
	return id, true
}

func executedFilter(c *gin.Context) *bool {
	raw := c.Query("executed")
	if raw == "" {
		return nil
	}
	v := raw == "1" || raw == "true"
	return &v
}

func intParam(c *gin.Context, name string) int {
	n, err := strconv.Atoi(c.Query(name))
	if err != nil {
		return 0
	}
	return n
}
