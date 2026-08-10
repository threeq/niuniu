package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// usageProvider is the slice of *service.ClaudeUsageService the handler uses.
type usageProvider interface {
	GetForAccount(ctx context.Context, accountID int64, force bool) (*service.AccountUsage, error)
}

// accountAuthorizer guards per-account read access.
type accountAuthorizer interface {
	CanAccessClaudeAccount(ctx context.Context, caller service.CallerInfo, accountID int64) (*service.ResolvedAccount, error)
}

// ClaudeAccountHandler handles /api/claude-accounts and /api/claude-active-account.
type ClaudeAccountHandler struct {
	svc          *service.ClaudeAccountService
	usage        usageProvider
	accountAuthz accountAuthorizer
}

func NewClaudeAccountHandler(svc *service.ClaudeAccountService) *ClaudeAccountHandler {
	return &ClaudeAccountHandler{svc: svc}
}

// caller reads identity from gin context. IdentityResolver middleware sets
// auth_user_id / auth_role keys (server/internal/auth/identity_resolver.go).
func (h *ClaudeAccountHandler) caller(c *gin.Context) (int64, string) {
	idVal, _ := c.Get("auth_user_id")
	roleVal, _ := c.Get("auth_role")
	uid, _ := idVal.(int64)
	r, _ := roleVal.(string)
	return uid, r
}

// SetUsageDeps wires the usage subsystem deps post-construction. Called once
// by server.New after both ClaudeUsageService and ClaudeAccountService exist.
func (h *ClaudeAccountHandler) SetUsageDeps(usage usageProvider, authz accountAuthorizer) {
	h.usage = usage
	h.accountAuthz = authz
}

// GetUsage returns aggregated JSONL usage for a single Claude account.
// Authz: caller must be able to see the account (public OR creator OR admin).
func (h *ClaudeAccountHandler) GetUsage(c *gin.Context) {
	uid, role := h.caller(c)
	idStr := c.Param("id")
	accountID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || accountID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"code": "bad_account_id", "message": "account id must be a positive integer"})
		return
	}

	if _, err := h.accountAuthz.CanAccessClaudeAccount(c.Request.Context(), service.NewCallerInfo(uid, role), accountID); err != nil {
		switch {
		case errors.Is(err, service.ErrAccountNotFound), errors.Is(err, service.ErrNotVisible):
			// Same envelope shape as the rest of this handler's usage errors —
			// FE reads error.code uniformly. Hide existence: invisible == 404.
			c.JSON(http.StatusNotFound, gin.H{
				"code": "not_found", "message": "account not found", "hint_zh": "账号不存在或不可见",
			})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"code": "authz_error", "message": err.Error()})
		}
		return
	}

	force := c.Query("force") == "1"
	usage, err := h.usage.GetForAccount(c.Request.Context(), accountID, force)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrClaudeProjectsNotFound):
			c.JSON(http.StatusNotFound, gin.H{
				"code":    "projects_not_found",
				"message": "this account has no recorded usage yet",
				"hint_zh": "此账号尚未记录任何用量。在工作区中启动一次使用此账号的会话即可。",
			})
		case errors.Is(err, service.ErrClaudeDataDirNotFound):
			c.JSON(http.StatusNotFound, gin.H{
				"code":    "claude_data_dir_not_found",
				"message": "No Claude CLI data directory found on this machine.",
				"hint_zh": "未在本机找到 Claude CLI 会话数据目录。",
			})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{
				"code": "jsonl_walk_failed", "message": err.Error(), "hint_zh": "扫描会话日志失败。",
			})
		}
		return
	}
	c.JSON(http.StatusOK, usage)
}

func (h *ClaudeAccountHandler) requireAdmin(c *gin.Context) bool {
	_, role := h.caller(c)
	if role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin_required"})
		return false
	}
	return true
}

// List returns accounts visible to the caller (public + own private; admin sees all).
func (h *ClaudeAccountHandler) List(c *gin.Context) {
	id, role := h.caller(c)
	rows, err := h.svc.List(c.Request.Context(), service.NewCallerInfo(id, role))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

type createAccountBody struct {
	Name       string `json:"name"`
	Visibility string `json:"visibility"`
}

// Create allocates a new account row + config_dir, then issues a single-use
// ws_token for the WebSocket login PTY.
func (h *ClaudeAccountHandler) Create(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	var body createAccountBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	uid, _ := h.caller(c)
	out, err := h.svc.Create(c.Request.Context(), service.CreateAccountInput{
		Name: body.Name, Visibility: body.Visibility, CallerID: uid,
	})
	if err != nil {
		mapClaudeAccountServiceError(c, err)
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
//
// Rejects status='active' on managed (non-default) rows (spec §"多 admin 同时
//登录" — delete + recreate is the supported credential-switch path).
//
// Exception: the default row (config_dir == "") cannot be deleted (service
// returns ErrCannotDeleteDefault), so the delete-recreate workaround is
// impossible. Allow active re-login on the default row instead — `claude
// /login` overwrites ~/.claude/ regardless of niuniu's bookkeeping, and host
// shell access defeats the guard anyway, so blocking the UI gives no real
// safety benefit and just strands operators wanting to switch the host
// account.
func (h *ClaudeAccountHandler) IssueLoginToken(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	row, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		mapClaudeAccountServiceError(c, err)
		return
	}
	if row.Status == "active" && row.ConfigDir != "" {
		// `code` mirrors `error` so frontends using the canonical
		// `{error:{code,message}}` envelope can translate; `error` stays a
		// flat string to keep back-compat with existing callers.
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

type updateAccountBody struct {
	Name       *string `json:"name"`
	Visibility *string `json:"visibility"`
}

// Update changes name and/or visibility.
func (h *ClaudeAccountHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var body updateAccountBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	uid, role := h.caller(c)
	if err := h.svc.Update(c.Request.Context(), service.UpdateAccountInput{
		ID: id, Name: body.Name, Visibility: body.Visibility,
		Caller: service.NewCallerInfo(uid, role),
	}); err != nil {
		mapClaudeAccountServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Delete removes a managed account row. Default row is rejected.
func (h *ClaudeAccountHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	uid, role := h.caller(c)
	if err := h.svc.Delete(c.Request.Context(), id, service.NewCallerInfo(uid, role)); err != nil {
		mapClaudeAccountServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Refresh re-reads the account's config_dir / ~/.claude.json for email + status.
// IM-5: visibility-gated to prevent member from probing private accounts.
func (h *ClaudeAccountHandler) Refresh(c *gin.Context) {
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	uid, role := h.caller(c)
	row, err := h.svc.GetByID(c.Request.Context(), id)
	if err != nil {
		mapClaudeAccountServiceError(c, err)
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

// GetActive returns the active account for an owner (or null).
func (h *ClaudeAccountHandler) GetActive(c *gin.Context) {
	ownerType := c.Query("owner_type")
	ownerID, _ := strconv.ParseInt(c.Query("owner_id"), 10, 64)
	got, err := h.svc.GetActive(c.Request.Context(), ownerType, ownerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"account": got})
}

type setActiveBody struct {
	OwnerType string `json:"owner_type"`
	OwnerID   int64  `json:"owner_id"`
	AccountID *int64 `json:"account_id"`
}

// SetActive sets (or clears, account_id null) the active account for an owner.
// Org-scope authz (org owner/admin) is enforced in service.SetActive via
// caller checks against the existing org-membership constraints.
func (h *ClaudeAccountHandler) SetActive(c *gin.Context) {
	var body setActiveBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	uid, role := h.caller(c)
	if err := h.svc.SetActive(c.Request.Context(), body.OwnerType, body.OwnerID, body.AccountID,
		service.NewCallerInfo(uid, role)); err != nil {
		mapClaudeAccountServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// AuditLog returns recent audit entries for an account. Admin only.
func (h *ClaudeAccountHandler) AuditLog(c *gin.Context) {
	if !h.requireAdmin(c) {
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	limit, _ := strconv.ParseInt(c.DefaultQuery("limit", "50"), 10, 64)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	beforeQ := c.Query("before")
	before := int64(1<<62 - 1) // effectively "from newest"
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

// SetWorkspaceClaudeAccount handles PUT /api/workspaces/:id/claude-account.
// Workspace agent accounts are immutable after creation; callers must choose
// the desired account in the create-workspace flow.
func SetWorkspaceClaudeAccount(claudeSvc *service.ClaudeAccountService, authz *service.Authz, wsSvc *service.WorkspaceService) gin.HandlerFunc {
	return func(c *gin.Context) {
		workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_id"})
			return
		}

		uidVal, _ := c.Get("auth_user_id")
		uid, _ := uidVal.(int64)

		if uid > 0 && authz != nil {
			if _, err := authz.CanAccessWorkspace(c.Request.Context(), uid, workspaceID); err != nil {
				writeAuthzError(c, err)
				return
			}
		}

		c.JSON(http.StatusConflict, gin.H{
			"error":   "workspace_account_immutable",
			"message": "workspace agent account cannot be changed after creation",
		})
	}
}

// mapClaudeAccountServiceError maps service-layer sentinel errors to HTTP codes.
func mapClaudeAccountServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAccountNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
	case errors.Is(err, service.ErrCannotDeleteDefault):
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot_delete_default"})
	case errors.Is(err, service.ErrAccountInUse):
		// C4: include the workspace IDs from the wrapped error message.
		c.JSON(http.StatusConflict, gin.H{"error": "in_use", "message": err.Error()})
	case errors.Is(err, service.ErrNotVisible):
		c.JSON(http.StatusNotFound, gin.H{"error": "not_visible"})
	case errors.Is(err, service.ErrInvalidVisibility):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_visibility"})
	case service.IsNameTaken(err):
		c.JSON(http.StatusConflict, gin.H{"error": "name_taken"})
	case errors.Is(err, service.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}
