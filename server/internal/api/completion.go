// Completion (自动完成) HTTP surface — design §6.9.
//
// Project default settings + named profile CRUD. The per-issue override is
// exposed both through PATCH /api/issues/:id (completion_profile_id) and the
// dedicated issue_set_completion_profile MCP tunnel.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// parseCompletionProfileField decodes the tri-state completion_profile_id field
// on PATCH /api/issues/:id: absent (unchanged), JSON null (clear), or an id.
func parseCompletionProfileField(raw json.RawMessage) (bool, *int64, error) {
	if len(raw) == 0 {
		return false, nil, nil
	}
	if string(raw) == "null" {
		return true, nil, nil
	}
	var id int64
	if err := json.Unmarshal(raw, &id); err != nil || id <= 0 {
		return false, nil, fmt.Errorf("completion_profile_id must be a positive integer or null")
	}
	return true, &id, nil
}

// CompletionHandler serves completion settings and named profiles.
type CompletionHandler struct {
	svc   *service.CompletionService
	authz *service.Authz
}

func NewCompletionHandler(svc *service.CompletionService, authz *service.Authz) *CompletionHandler {
	return &CompletionHandler{svc: svc, authz: authz}
}

func (h *CompletionHandler) completionProjectAccess(c *gin.Context, write bool) (int64, bool) {
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

func (h *CompletionHandler) mapCompletionErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrCompletionProfileInUse):
		c.JSON(http.StatusConflict, gin.H{
			"error": gin.H{
				"code":    "COMPLETION_PROFILE_IN_USE",
				"message": "this profile is assigned to one or more issues; clear those assignments first",
			},
		})
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, service.ErrNotFound):
		NotFound(c, "COMPLETION_PROFILE")
	case errors.Is(err, service.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	default:
		BadRequest(c, err.Error())
	}
}

// GetSettings serves GET /api/projects/:id/completion/settings.
//
// @Summary      Read a project's default completion policy
// @Description  Falls back to schema defaults when no row has been stored yet.
// @Tags         Completion
// @Produce      json
// @Param        id   path      int  true  "Project ID"
// @Success      200  {object}  service.CompletionSettingsView
// @Failure      401  {object}  Error
// @Failure      403  {object}  Error
// @Router       /projects/{id}/completion/settings [get]
func (h *CompletionHandler) GetSettings(c *gin.Context) {
	pid, ok := h.completionProjectAccess(c, false)
	if !ok {
		return
	}
	view, err := h.svc.GetSettings(c.Request.Context(), pid)
	if err != nil {
		h.mapCompletionErr(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// PutSettings serves PUT /api/projects/:id/completion/settings.
//
// @Summary      Update a project's default completion policy
// @Description  Partial: omitted fields keep their current value.
// @Tags         Completion
// @Accept       json
// @Produce      json
// @Param        id    path      int                                  true  "Project ID"
// @Param        body  body      service.CompletionSettingsInput      true  "Policy fields"
// @Success      200   {object}  service.CompletionSettingsView
// @Failure      400   {object}  Error
// @Failure      401   {object}  Error
// @Failure      403   {object}  Error
// @Router       /projects/{id}/completion/settings [put]
func (h *CompletionHandler) PutSettings(c *gin.Context) {
	pid, ok := h.completionProjectAccess(c, true)
	if !ok {
		return
	}
	var in service.CompletionSettingsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	view, err := h.svc.UpdateSettings(c.Request.Context(), pid, in)
	if err != nil {
		h.mapCompletionErr(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// ListProfiles serves GET /api/projects/:id/completion/profiles.
//
// @Summary      List a project's named completion profiles
// @Tags         Completion
// @Produce      json
// @Param        id   path      int  true  "Project ID"
// @Success      200  {array}   service.CompletionProfileDTO
// @Failure      401  {object}  Error
// @Failure      403  {object}  Error
// @Router       /projects/{id}/completion/profiles [get]
func (h *CompletionHandler) ListProfiles(c *gin.Context) {
	pid, ok := h.completionProjectAccess(c, false)
	if !ok {
		return
	}
	items, err := h.svc.ListProfiles(c.Request.Context(), pid)
	if err != nil {
		h.mapCompletionErr(c, err)
		return
	}
	c.JSON(http.StatusOK, items)
}

// CreateProfile serves POST /api/projects/:id/completion/profiles.
//
// @Summary      Create a named completion profile
// @Tags         Completion
// @Accept       json
// @Produce      json
// @Param        id    path      int                                 true  "Project ID"
// @Param        body  body      service.CompletionProfileInput      true  "Profile"
// @Success      201   {object}  service.CompletionProfileDTO
// @Failure      400   {object}  Error
// @Failure      401   {object}  Error
// @Failure      403   {object}  Error
// @Router       /projects/{id}/completion/profiles [post]
func (h *CompletionHandler) CreateProfile(c *gin.Context) {
	pid, ok := h.completionProjectAccess(c, true)
	if !ok {
		return
	}
	var in service.CompletionProfileInput
	if err := c.ShouldBindJSON(&in); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	profile, err := h.svc.CreateProfile(c.Request.Context(), pid, in)
	if err != nil {
		h.mapCompletionErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, profile)
}

// UpdateProfile serves PATCH /api/projects/:id/completion/profiles/:pid.
//
// @Summary      Edit a named completion profile
// @Description  Partial: omitted fields keep their current value.
// @Tags         Completion
// @Accept       json
// @Produce      json
// @Param        id    path      int                                 true  "Project ID"
// @Param        pid   path      int                                 true  "Profile ID"
// @Param        body  body      service.CompletionProfileInput      true  "Fields to change"
// @Success      200   {object}  service.CompletionProfileDTO
// @Failure      400   {object}  Error
// @Failure      401   {object}  Error
// @Failure      403   {object}  Error
// @Failure      404   {object}  Error
// @Router       /projects/{id}/completion/profiles/{pid} [patch]
func (h *CompletionHandler) UpdateProfile(c *gin.Context) {
	pid, ok := h.completionProjectAccess(c, true)
	if !ok {
		return
	}
	profileID, ok := completionProfileIDParam(c)
	if !ok {
		return
	}
	var in service.CompletionProfileInput
	if err := c.ShouldBindJSON(&in); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	profile, err := h.svc.UpdateProfile(c.Request.Context(), pid, profileID, in)
	if err != nil {
		h.mapCompletionErr(c, err)
		return
	}
	c.JSON(http.StatusOK, profile)
}

// DeleteProfile serves DELETE /api/projects/:id/completion/profiles/:pid.
//
// @Summary      Delete a named completion profile
// @Description  Refused with 409 while any issue still references the profile.
// @Tags         Completion
// @Param        id   path  int  true  "Project ID"
// @Param        pid  path  int  true  "Profile ID"
// @Success      204
// @Failure      401  {object}  Error
// @Failure      403  {object}  Error
// @Failure      404  {object}  Error
// @Failure      409  {object}  Error
// @Router       /projects/{id}/completion/profiles/{pid} [delete]
func (h *CompletionHandler) DeleteProfile(c *gin.Context) {
	pid, ok := h.completionProjectAccess(c, true)
	if !ok {
		return
	}
	profileID, ok := completionProfileIDParam(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteProfile(c.Request.Context(), pid, profileID); err != nil {
		h.mapCompletionErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ─── MCP tunnel endpoints (/mcp/...) ─────────────────────────────────────────

// MCPSetPolicy backs completion_set_policy.
func (h *CompletionHandler) MCPSetPolicy(c *gin.Context) {
	pid, ok := h.completionProjectAccess(c, true)
	if !ok {
		return
	}
	var in service.CompletionSettingsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	view, err := h.svc.UpdateSettings(c.Request.Context(), pid, in)
	if err != nil {
		h.mapCompletionErr(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// MCPListProfiles backs completion_list_profiles.
func (h *CompletionHandler) MCPListProfiles(c *gin.Context) {
	pid, ok := h.completionProjectAccess(c, false)
	if !ok {
		return
	}
	items, err := h.svc.ListProfiles(c.Request.Context(), pid)
	if err != nil {
		h.mapCompletionErr(c, err)
		return
	}
	c.JSON(http.StatusOK, items)
}

// MCPUpsertProfile backs completion_upsert_profile. profile_id > 0 updates,
// otherwise a profile is created.
func (h *CompletionHandler) MCPUpsertProfile(c *gin.Context) {
	pid, ok := h.completionProjectAccess(c, true)
	if !ok {
		return
	}
	var body struct {
		ProfileID       int64  `json:"profile_id"`
		Name            string `json:"name"`
		AutohostEnabled *bool  `json:"autohost_enabled"`
		MaxRounds       *int64 `json:"max_rounds"`
		TokenBudget     *int64 `json:"token_budget"`
		StopOnDecision  *bool  `json:"stop_on_decision"`
		RequireReview   *bool  `json:"require_review"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	in := service.CompletionProfileInput{
		AutohostEnabled: body.AutohostEnabled,
		MaxRounds:       body.MaxRounds,
		TokenBudget:     body.TokenBudget,
		StopOnDecision:  body.StopOnDecision,
		RequireReview:   body.RequireReview,
	}
	if body.Name != "" {
		in.Name = &body.Name
	}
	var (
		profile service.CompletionProfileDTO
		err     error
	)
	if body.ProfileID > 0 {
		profile, err = h.svc.UpdateProfile(c.Request.Context(), pid, body.ProfileID, in)
	} else {
		profile, err = h.svc.CreateProfile(c.Request.Context(), pid, in)
	}
	if err != nil {
		h.mapCompletionErr(c, err)
		return
	}
	c.JSON(http.StatusOK, profile)
}

// MCPSetIssueProfile backs issue_set_completion_profile. A null profile_id
// clears the override (issue inherits the project default).
func (h *CompletionHandler) MCPSetIssueProfile(c *gin.Context) {
	issueID, ok := issueIDParam(c)
	if !ok {
		return
	}
	if uid := c.GetInt64("auth_user_id"); uid > 0 {
		owner, err := h.authz.CanAccessIssue(c.Request.Context(), uid, issueID)
		if err != nil {
			writeAuthzError(c, err)
			return
		}
		if err := h.authz.EnsureOwnerWritable(c.Request.Context(), uid, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var body struct {
		ProfileID *int64 `json:"profile_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid request body")
		return
	}
	issue, err := h.svc.SetIssueProfile(c.Request.Context(), issueID, body.ProfileID)
	if err != nil {
		h.mapCompletionErr(c, err)
		return
	}
	var profileID *int64
	if issue.CompletionProfileID.Valid {
		v := issue.CompletionProfileID.Int64
		profileID = &v
	}
	c.JSON(http.StatusOK, gin.H{"issue_id": issue.ID, "completion_profile_id": profileID})
}

func completionProfileIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("pid"), 10, 64)
	if err != nil || id <= 0 {
		BadRequest(c, "invalid profile ID")
		return 0, false
	}
	return id, true
}
