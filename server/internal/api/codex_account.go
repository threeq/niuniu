package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// CodexAccountHandler handles /api/codex-accounts/* routes. Mirrors
// ClaudeAccountHandler but is simpler (no usage subsystem, no SetActive,
// no workspace projects-dir migration on rebind).
type CodexAccountHandler struct {
	svc *service.CodexAccountService
}

func NewCodexAccountHandler(svc *service.CodexAccountService) *CodexAccountHandler {
	return &CodexAccountHandler{svc: svc}
}

func (h *CodexAccountHandler) caller(c *gin.Context) (int64, string) {
	idVal, _ := c.Get("auth_user_id")
	roleVal, _ := c.Get("auth_role")
	uid, _ := idVal.(int64)
	r, _ := roleVal.(string)
	return uid, r
}

func (h *CodexAccountHandler) requireAdmin(c *gin.Context) bool {
	_, role := h.caller(c)
	if role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin_required"})
		return false
	}
	return true
}

// List returns accounts visible to caller (public + own private; admin sees all).
func (h *CodexAccountHandler) List(c *gin.Context) {
	id, role := h.caller(c)
	rows, err := h.svc.List(c.Request.Context(), service.NewCallerInfo(id, role))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

type createCodexAccountBody struct {
	Name       string `json:"name"`
	Visibility string `json:"visibility"`
}

// Create allocates a new codex account row + config_dir, then issues a
// single-use ws_token bound to it for the WS login PTY.
func (h *CodexAccountHandler) Create(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	var body createCodexAccountBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	uid, _ := h.caller(c)
	out, err := h.svc.Create(c.Request.Context(), service.CreateCodexAccountInput{
		Name: body.Name, Visibility: body.Visibility, CallerID: uid,
	})
	if err != nil {
		mapCodexAccountServiceError(c, err)
		return
	}
	tok, err := h.svc.IssueLoginToken(out.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":         out.ID,
		"name":       out.Name,
		"config_dir": out.ConfigDir,
		"visibility": out.Visibility,
		"status":     out.Status,
		"ws_token":   tok,
	})
}

// IssueLoginToken re-issues a ws_token for an existing pending/failed account.
// Reject status='active' (must delete + recreate to switch credentials).
func (h *CodexAccountHandler) IssueLoginToken(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	row, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		mapCodexAccountServiceError(c, err)
		return
	}
	if row.Status == "active" {
		// `code` mirrors `error` so frontends using the canonical
		// `{error:{code,message}}` envelope can translate; `error` stays a
		// flat string to keep back-compat with existing callers. Codex has
		// no unkillable default row (the synthetic "system default" is
		// served by DefaultStatus, not a real table row), so delete +
		// recreate is always feasible here — unlike claude_account.go,
		// which carves out the config_dir=="" exception.
		c.JSON(http.StatusConflict, gin.H{
			"error":   "already_authed",
			"code":    "already_authed",
			"message": "account already logged in; delete and re-create to switch credentials",
		})
		return
	}
	tok, err := h.svc.IssueLoginToken(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ws_token": tok})
}

type updateCodexAccountBody struct {
	Name       *string `json:"name"`
	Visibility *string `json:"visibility"`
}

// Update changes name and/or visibility.
func (h *CodexAccountHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var body updateCodexAccountBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	uid, role := h.caller(c)
	if err := h.svc.Update(c.Request.Context(), service.UpdateCodexAccountInput{
		ID: id, Name: body.Name, Visibility: body.Visibility,
		Caller: service.NewCallerInfo(uid, role),
	}); err != nil {
		mapCodexAccountServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Delete removes the account row + config_dir. Authz: admin OR creator.
// Refuses if account is in use by running agents.
func (h *CodexAccountHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	uid, role := h.caller(c)
	if err := h.svc.Delete(c.Request.Context(), id, service.NewCallerInfo(uid, role)); err != nil {
		mapCodexAccountServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Refresh re-reads the account's config_dir/auth.json for email + status.
// Visibility-gated.
func (h *CodexAccountHandler) Refresh(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	uid, role := h.caller(c)
	row, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		mapCodexAccountServiceError(c, err)
		return
	}
	if !h.svc.VisibleTo(row, service.NewCallerInfo(uid, role)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_visible"})
		return
	}
	if err := h.svc.RefreshFromDisk(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// DefaultStatus returns the host's native ~/.codex/ auth state (i.e. whether
// the user has completed `codex login` on the host). Drives the synthetic
// "system default" row on the Codex accounts settings page so the row can
// reflect already-logged-in state instead of always offering a login button.
// No personal-mode gate on purpose: hosted-mode frontends do not render this
// row at all, and the reported state of the server-host's ~/.codex/ is not
// itself sensitive.
func (h *CodexAccountHandler) DefaultStatus(c *gin.Context) {
	state, err := h.svc.GetDefaultStatus(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, state)
}

// AuditLog returns recent audit entries for an account. Admin only.
func (h *CodexAccountHandler) AuditLog(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	limit, _ := strconv.ParseInt(c.DefaultQuery("limit", "50"), 10, 64)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	beforeQ := c.Query("before")
	before := int64(1<<62 - 1)
	if beforeQ != "" {
		if v, err := strconv.ParseInt(beforeQ, 10, 64); err == nil {
			before = v
		}
	}
	rows, err := h.svc.ListAuditLog(c.Request.Context(), id, before, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// SetWorkspaceCodexAccount handles PUT /api/workspaces/:id/codex-account.
// Workspace agent accounts are immutable after creation; callers must choose
// the desired account in the create-workspace flow.
func SetWorkspaceCodexAccount(codexSvc *service.CodexAccountService, authz *service.Authz, wsSvc *service.WorkspaceService) gin.HandlerFunc {
	return func(c *gin.Context) {
		workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
			return
		}

		uid, _ := callerFromContext(c)
		if uid > 0 && authz != nil {
			if _, err := authz.CanAccessWorkspace(c.Request.Context(), uid, workspaceID); err != nil {
				c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
				return
			}
		}
		c.JSON(http.StatusConflict, gin.H{
			"error":   "workspace_account_immutable",
			"message": "workspace agent account cannot be changed after creation",
		})
	}
}

// callerFromContext is a small shim that mirrors h.caller for the
// non-receiver SetWorkspaceCodexAccount HandlerFunc.
func callerFromContext(c *gin.Context) (int64, string) {
	idVal, _ := c.Get("auth_user_id")
	roleVal, _ := c.Get("auth_role")
	uid, _ := idVal.(int64)
	r, _ := roleVal.(string)
	return uid, r
}

// validCodexSandboxModes / validCodexApprovalPolicies — kept in api/ to map
// service.ErrInvalidCodex* sentinels. Service layer also validates.
var validCodexSandboxModes = map[string]struct{}{
	"read-only":          {},
	"workspace-write":    {},
	"danger-full-access": {},
}
var validCodexApprovalPolicies = map[string]struct{}{
	"untrusted":  {},
	"on-failure": {},
	"on-request": {},
	"never":      {},
}

type codexSandboxBody struct {
	SandboxMode    *string `json:"sandbox_mode"`
	ApprovalPolicy *string `json:"approval_policy"`
}

// SetWorkspaceCodexSandbox handles PUT /api/workspaces/:id/codex-sandbox.
// Both fields are optional pointers — omit means "no change". Only allowed
// when the workspace is cli_type='codex'.
func SetWorkspaceCodexSandbox(
	wsSvc *service.WorkspaceService,
	authz *service.Authz,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
			return
		}
		var body codexSandboxBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
			return
		}
		if body.SandboxMode != nil {
			if _, ok := validCodexSandboxModes[*body.SandboxMode]; !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_sandbox_mode"})
				return
			}
		}
		if body.ApprovalPolicy != nil {
			if _, ok := validCodexApprovalPolicies[*body.ApprovalPolicy]; !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_approval_policy"})
				return
			}
		}
		uid, _ := callerFromContext(c)
		if _, err := authz.CanAccessWorkspace(c.Request.Context(), uid, workspaceID); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		if err := wsSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := wsSvc.UpdateCodexSandbox(c.Request.Context(), workspaceID, body.SandboxMode, body.ApprovalPolicy); err != nil {
			if errors.Is(err, service.ErrCodexSandboxNotCodexWorkspace) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "not_codex_workspace"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// mapCodexAccountServiceError maps service sentinels to HTTP codes.
func mapCodexAccountServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrCodexAccountNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
	case errors.Is(err, service.ErrCodexAccountInUse):
		c.JSON(http.StatusConflict, gin.H{"error": "in_use", "message": err.Error()})
	case errors.Is(err, service.ErrCodexNotVisible):
		c.JSON(http.StatusNotFound, gin.H{"error": "not_visible"})
	case errors.Is(err, service.ErrCodexInvalidVisibility):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_visibility"})
	case errors.Is(err, service.ErrCodexNameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": "name_taken"})
	case errors.Is(err, service.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
