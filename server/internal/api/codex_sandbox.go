package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// callerFromContext is a small shim that mirrors h.caller for the
// non-receiver HandlerFuncs below.
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
