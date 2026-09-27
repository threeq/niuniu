package api

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// CapabilityBackendHandler serves the capability-configuration API: the module
// registry the settings page renders from, plus CRUD over the per-owner
// backends a module's tools call (plan §1.1). It is the tool-layer counterpart
// of EnvProviderHandler — same shape, separate resource and namespace
// (NN_CAP_* vs the agents' LLM env).
type CapabilityBackendHandler struct {
	svc   *service.CapabilityBackendService
	Authz *service.Authz
}

func NewCapabilityBackendHandler(svc *service.CapabilityBackendService) *CapabilityBackendHandler {
	return &CapabilityBackendHandler{svc: svc}
}

// CapabilityBackendRequest is the create/update payload. api_key is write-only:
// responses never echo it, and on update an absent or empty value keeps the
// stored credential (the edit dialog never holds the plaintext key).
type CapabilityBackendRequest struct {
	Module      string            `json:"module" binding:"required"`
	Capability  string            `json:"capability" binding:"required"`
	Backend     string            `json:"backend" binding:"required"`
	Name        string            `json:"name" binding:"required"`
	BaseURL     string            `json:"base_url"`
	ApiKey      string            `json:"api_key"`
	ExtraConfig map[string]string `json:"extra_config"`
	// Enabled is the manual on/off switch (nil = on for create, unchanged on update).
	Enabled *bool `json:"enabled"`
	// Owner scopes the binding: {"workspace", <id>} = workspace override,
	// {"user", <id>} = global default. Omitted → the caller's own user scope.
	Owner *struct {
		Type string `json:"type"`
		ID   int64  `json:"id"`
	} `json:"owner,omitempty"`
}

// CapabilityBackendResponse is one backend binding. It deliberately carries no
// api_key — only has_api_key — so a credential can never leak through the API.
type CapabilityBackendResponse struct {
	ID          int64             `json:"id"`
	Owner       OwnerDTO          `json:"owner"`
	Module      string            `json:"module"`
	Capability  string            `json:"capability"`
	Backend     string            `json:"backend"`
	Name        string            `json:"name"`
	BaseURL     string            `json:"base_url"`
	HasAPIKey   bool              `json:"has_api_key"`
	ExtraConfig map[string]string `json:"extra_config"`
	Enabled     bool              `json:"enabled"`
	Position    int               `json:"position"`
}

func toCapabilityBackendResponse(b service.CapabilityBackend) CapabilityBackendResponse {
	extra := b.ExtraConfig
	if extra == nil {
		extra = map[string]string{}
	}
	return CapabilityBackendResponse{
		ID:          b.ID,
		Owner:       ownerDTOFromRef(b.OwnerType, b.OwnerID),
		Module:      b.Module,
		Capability:  b.Capability,
		Backend:     b.Backend,
		Name:        b.Name,
		BaseURL:     b.BaseURL,
		HasAPIKey:   b.APIKey != "",
		ExtraConfig: extra,
		Enabled:     b.Enabled,
		Position:    b.Position,
	}
}

func toCapabilityBackendResponses(bs []service.CapabilityBackend) []CapabilityBackendResponse {
	out := make([]CapabilityBackendResponse, len(bs))
	for i, b := range bs {
		out[i] = toCapabilityBackendResponse(b)
	}
	return out
}

// ListModules returns the capability module registry (name, display name,
// config schema, scenes) the settings page renders the "capability config"
// section from. Static — modules are compiled in, not installed at runtime.
func (h *CapabilityBackendHandler) ListModules(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"modules": service.ListCapabilityModules()})
}

// List returns the backends of one module, optionally narrowed to a single
// owner via ?owner_type=&owner_id=. With auth on, rows the caller cannot
// access are filtered out (their own user rows, system-wide defaults, and
// workspaces they can reach).
func (h *CapabilityBackendHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	module := c.Query("module")
	if module == "" {
		BadRequest(c, "module is required")
		return
	}
	ownerID, _ := strconv.ParseInt(c.DefaultQuery("owner_id", "0"), 10, 64)
	backends, err := h.svc.List(ctx, c.Query("owner_type"), ownerID, module)
	if err != nil {
		InternalError(c, err)
		return
	}
	if userID > 0 && h.Authz != nil {
		backends = h.filterAccessible(c, userID, backends)
	}
	c.JSON(http.StatusOK, gin.H{"backends": toCapabilityBackendResponses(backends)})
}

// Create stores one backend binding and returns it (without the key).
func (h *CapabilityBackendHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req CapabilityBackendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	ownerType, ownerID, ok := h.resolveOwner(c, userID, req.Owner)
	if !ok {
		return
	}
	created, err := h.svc.Create(c.Request.Context(), service.CapabilityBackend{
		OwnerType:   ownerType,
		OwnerID:     ownerID,
		Module:      req.Module,
		Capability:  req.Capability,
		Backend:     req.Backend,
		Name:        req.Name,
		BaseURL:     req.BaseURL,
		APIKey:      req.ApiKey,
		ExtraConfig: req.ExtraConfig,
		Enabled:     req.Enabled == nil || *req.Enabled,
	})
	if err != nil {
		if isUniqueViolation(err) {
			BadRequest(c, "a backend with this name already exists for this owner, module and capability")
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toCapabilityBackendResponse(*created))
}

// Update rewrites a backend binding. The owner is immutable (the payload's
// owner, if any, is ignored) and an absent or empty api_key keeps the stored
// credential.
func (h *CapabilityBackendHandler) Update(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid capability backend ID")
		return
	}
	cur, err := h.svc.Get(ctx, id)
	if err != nil {
		NotFound(c, "CAPABILITY_BACKEND")
		return
	}
	if err := h.checkRowAccess(c, userID, cur); err != nil {
		writeAuthzError(c, err)
		return
	}
	var req CapabilityBackendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	enabled := cur.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	updated, err := h.svc.Update(ctx, id, service.CapabilityBackend{
		Module:      req.Module,
		Capability:  req.Capability,
		Backend:     req.Backend,
		Name:        req.Name,
		BaseURL:     req.BaseURL,
		APIKey:      req.ApiKey,
		ExtraConfig: req.ExtraConfig,
		Enabled:     enabled,
		Position:    cur.Position,
	}, req.ApiKey != "")
	if err != nil {
		slog.Warn("UpdateCapabilityBackend failed", "id", id, "error", err)
		if isUniqueViolation(err) {
			BadRequest(c, "a backend with this name already exists for this owner, module and capability")
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusOK, toCapabilityBackendResponse(*updated))
}

// Delete removes a backend binding.
func (h *CapabilityBackendHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid capability backend ID")
		return
	}
	cur, err := h.svc.Get(ctx, id)
	if err != nil {
		NotFound(c, "CAPABILITY_BACKEND")
		return
	}
	if err := h.checkRowAccess(c, userID, cur); err != nil {
		writeAuthzError(c, err)
		return
	}
	if err := h.svc.Delete(ctx, id); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// resolveOwner maps the request's optional owner to the row's (type, id) and
// enforces write access. An omitted owner (or an explicit user/0, mirroring
// env_providers) means the caller's own user scope — with auth off that is
// user 0, the system-wide default. Returns ok=false after writing the error
// response.
func (h *CapabilityBackendHandler) resolveOwner(c *gin.Context, userID int64, req *struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}) (string, int64, bool) {
	ownerType, ownerID := "user", userID
	if req != nil && req.Type != "" && !(req.Type == "user" && req.ID == 0) {
		ownerType, ownerID = req.Type, req.ID
	}
	switch ownerType {
	case "user":
		if userID > 0 && h.Authz != nil {
			if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), userID, service.OwnerRef{Type: "user", ID: ownerID}); err != nil {
				writeAuthzError(c, err)
				return "", 0, false
			}
		}
	case "workspace":
		if ownerID <= 0 {
			BadRequest(c, "workspace owner id is required")
			return "", 0, false
		}
		if userID > 0 && h.Authz != nil {
			if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, ownerID); err != nil {
				writeAuthzError(c, err)
				return "", 0, false
			}
		}
	default:
		BadRequest(c, "owner type must be 'user' or 'workspace'")
		return "", 0, false
	}
	return ownerType, ownerID, true
}

// checkRowAccess enforces read/write access to an existing row: a workspace
// row requires access to that workspace, a user row just the caller's own
// scope (which is exactly what CanAccessOwner checks).
func (h *CapabilityBackendHandler) checkRowAccess(c *gin.Context, userID int64, row *service.CapabilityBackend) error {
	if userID <= 0 || h.Authz == nil {
		return nil
	}
	if row.OwnerType == "workspace" {
		_, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, row.OwnerID)
		return err
	}
	return h.Authz.CanAccessOwner(c.Request.Context(), userID, service.OwnerRef{Type: "user", ID: row.OwnerID})
}

// filterAccessible drops the rows the caller may not see: their own user rows,
// system-wide defaults (user 0), and workspaces they can access.
func (h *CapabilityBackendHandler) filterAccessible(c *gin.Context, userID int64, rows []service.CapabilityBackend) []service.CapabilityBackend {
	ctx := c.Request.Context()
	out := make([]service.CapabilityBackend, 0, len(rows))
	for _, row := range rows {
		switch row.OwnerType {
		case "user":
			if row.OwnerID == userID || row.OwnerID == 0 {
				out = append(out, row)
			}
		case "workspace":
			if _, err := h.Authz.CanAccessWorkspace(ctx, userID, row.OwnerID); err == nil {
				out = append(out, row)
			}
		}
	}
	return out
}
