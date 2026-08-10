// External-source REST endpoints. Routes live under /api/projects/:id/...
// (project-scoped, authz-gated). Add / Delete require write access on the
// project owner; List only requires read access.
//
// The legacy SPA-driven "Browse upstream issues" drawer was removed when
// the integration model shifted to an AI-driven proxy (`/mcp/external-proxy/*`):
// the agent now lists / imports upstream issues on demand via MCP tool
// calls, so there's no longer a dedicated HTTP path here.
package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// ExternalSourceHandler binds the four external-source endpoints. The
// service does the heavy lifting (caching, owner resolution, provider
// dispatch); the handler is mostly param parsing + authz + error mapping.
type ExternalSourceHandler struct {
	svc   *service.ExternalSourceService
	authz *service.Authz
}

// NewExternalSourceHandler wires service + authz into the handler.
func NewExternalSourceHandler(svc *service.ExternalSourceService, authz *service.Authz) *ExternalSourceHandler {
	return &ExternalSourceHandler{svc: svc, authz: authz}
}

// projectIDFromPath parses /:id from the route. On parse failure it
// writes the 400 response itself and returns ok=false; callers should
// short-circuit when ok is false.
func (h *ExternalSourceHandler) projectIDFromPath(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad project id"})
		return 0, false
	}
	return id, true
}

// callerUserID reads auth_user_id from the gin context (set by
// auth.IdentityResolver) and 401s on missing identity.
func (h *ExternalSourceHandler) callerUserID(c *gin.Context) (int64, bool) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return 0, false
	}
	return uid, true
}

// List returns every external source bound to the project. Requires
// CanAccessProject (read access — org members can browse, personal owner
// is the only viewer for personal projects).
func (h *ExternalSourceHandler) List(c *gin.Context) {
	pid, ok := h.projectIDFromPath(c)
	if !ok {
		return
	}
	uid, ok := h.callerUserID(c)
	if !ok {
		return
	}
	if _, err := h.authz.CanAccessProject(c.Request.Context(), uid, pid); err != nil {
		writeAuthzError(c, err)
		return
	}
	items, err := h.svc.List(c.Request.Context(), pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// addSourceBody is the POST body shape. Provider is constrained to the
// whitelist; SourceKey shape is provider-specific (GitHub: "owner/repo").
// CredentialID is the id of a previously-configured credential.
type addSourceBody struct {
	Provider     integration.ProviderName `json:"provider" binding:"required"`
	SourceKey    string                   `json:"source_key" binding:"required"`
	CredentialID int64                    `json:"credential_id" binding:"required"`
	Config       map[string]any           `json:"config"`
}

// Add upserts a (project, provider, source_key) binding. Write-gated via
// EnsureOwnerWritable (org members can write; personal projects require
// the caller to BE the owner, per spec §6.5).
func (h *ExternalSourceHandler) Add(c *gin.Context) {
	pid, ok := h.projectIDFromPath(c)
	if !ok {
		return
	}
	uid, ok := h.callerUserID(c)
	if !ok {
		return
	}
	owner, err := h.authz.CanAccessProject(c.Request.Context(), uid, pid)
	if err != nil {
		writeAuthzError(c, err)
		return
	}
	if err := h.authz.EnsureOwnerWritable(c.Request.Context(), uid, owner); err != nil {
		writeAuthzError(c, err)
		return
	}
	var b addSourceBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// No provider-name whitelist: providers are an open, user-creatable set
	// (the enum CHECK these used to mirror was dropped). The provider is
	// validated by the credential match below (ErrCredentialProviderMismatch),
	// and the credential can only reference a real external_providers row.
	if strings.TrimSpace(string(b.Provider)) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider required"})
		return
	}
	if strings.TrimSpace(b.SourceKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_key required"})
		return
	}
	dto, err := h.svc.Add(c.Request.Context(), pid, b.Provider, b.SourceKey, b.CredentialID, b.Config, uid)
	if err != nil {
		if errors.Is(err, service.ErrCredentialProviderMismatch) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, service.ErrNoCredential) {
			c.JSON(http.StatusPreconditionFailed, gin.H{
				"error":      "configure provider credential first",
				"error_kind": "no_credential",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, dto)
}

// Delete removes a binding by source row id. Same write gate as Add.
// Path is /projects/:id/external-sources/:sid — :id is the project (used
// for the authz check), :sid is the source row id.
func (h *ExternalSourceHandler) Delete(c *gin.Context) {
	pid, ok := h.projectIDFromPath(c)
	if !ok {
		return
	}
	uid, ok := h.callerUserID(c)
	if !ok {
		return
	}
	owner, err := h.authz.CanAccessProject(c.Request.Context(), uid, pid)
	if err != nil {
		writeAuthzError(c, err)
		return
	}
	if err := h.authz.EnsureOwnerWritable(c.Request.Context(), uid, owner); err != nil {
		writeAuthzError(c, err)
		return
	}
	sid, perr := strconv.ParseInt(c.Param("sid"), 10, 64)
	if perr != nil || sid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad source id"})
		return
	}
	if err := h.svc.Delete(c.Request.Context(), sid); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// Note: writeAuthzError is defined in api/errors.go and shared across
// handlers — it maps service.ErrNotFound → 404, service.ErrForbidden →
// 403, others → 500. The external-source handlers reuse it for project
// authz failures.
