package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

type EnvPresetHandler struct {
	svc   *service.EnvPresetService
	Authz *service.Authz
	DB    *sql.DB // used for batch owner-name lookup on list endpoints
}

func NewEnvPresetHandler(svc *service.EnvPresetService) *EnvPresetHandler {
	return &EnvPresetHandler{svc: svc}
}

type CreateEnvPresetRequest struct {
	Name        string            `json:"name" binding:"required"`
	Description string            `json:"description"`
	Env         map[string]string `json:"env"`
	Owner       *struct {
		Type string `json:"type"`
		ID   int64  `json:"id"`
	} `json:"owner,omitempty"`
}

type UpdateEnvPresetRequest struct {
	Name        string            `json:"name" binding:"required"`
	Description string            `json:"description"`
	Env         map[string]string `json:"env"`
}

func (h *EnvPresetHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	var err error
	presets, err := h.svc.List(ctx)
	if userID > 0 {
		presets, err = h.svc.ListForUser(ctx, userID)
	}
	if err != nil {
		InternalError(c, err)
		return
	}
	if h.DB != nil {
		refs := make([]ownerRef, len(presets))
		for i, p := range presets {
			refs[i] = ownerRef{p.OwnerType, p.OwnerID}
		}
		lk, _ := newOwnerLookup(ctx, h.DB, refs)
		c.JSON(http.StatusOK, toEnvPresetResponsesWithLookup(presets, lk))
		return
	}
	c.JSON(http.StatusOK, toEnvPresetResponses(presets))
}

func (h *EnvPresetHandler) Get(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid env preset ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessEnvPreset(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	preset, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		slog.Warn("GetEnvPreset failed", "id", id, "error", err)
		NotFound(c, "ENV_PRESET")
		return
	}
	c.JSON(http.StatusOK, toEnvPresetResponse(preset))
}

func (h *EnvPresetHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req CreateEnvPresetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if req.Env == nil {
		req.Env = make(map[string]string)
	}
	// Resolve owner: explicit body wins, but treat both nil and the SPA
	// no-currentUser fallback {type:"user", id:0} as "default to caller".
	var owner service.OwnerRef
	if req.Owner != nil && req.Owner.Type != "" && !(req.Owner.Type == "user" && req.Owner.ID == 0) {
		owner = service.OwnerRef{Type: req.Owner.Type, ID: req.Owner.ID}
		if err := owner.Validate(); err != nil {
			BadRequest(c, err.Error())
			return
		}
	} else {
		owner = service.OwnerRef{Type: "user", ID: userID}
	}
	if userID > 0 && h.Authz != nil {
		if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	preset, err := h.svc.Create(c.Request.Context(), req.Name, req.Description, req.Env, owner.Type, owner.ID)
	if err != nil {
		if isUniqueViolation(err) {
			BadRequest(c, "preset name already exists")
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toEnvPresetResponse(preset))
}

func (h *EnvPresetHandler) Update(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid env preset ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessEnvPreset(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req UpdateEnvPresetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if req.Env == nil {
		req.Env = make(map[string]string)
	}
	if err := h.svc.Update(c.Request.Context(), id, req.Name, req.Description, req.Env); err != nil {
		slog.Warn("UpdateEnvPreset failed", "id", id, "error", err)
		if isUniqueViolation(err) {
			BadRequest(c, "preset name already exists")
			return
		}
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *EnvPresetHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid env preset ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessEnvPreset(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// --- Response types and converters ---

type EnvPresetResponse struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Env         map[string]string `json:"env"`
	Owner       OwnerDTO          `json:"owner"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

func toEnvPresetResponse(p store.EnvPreset) EnvPresetResponse {
	env := make(map[string]string)
	_ = json.Unmarshal([]byte(p.Env), &env)
	return EnvPresetResponse{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Env:         env,
		Owner:       ownerDTOFromRef(p.OwnerType, p.OwnerID),
		CreatedAt:   p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   p.UpdatedAt.Format(time.RFC3339),
	}
}

func toEnvPresetResponses(ps []store.EnvPreset) []EnvPresetResponse {
	out := make([]EnvPresetResponse, len(ps))
	for i, p := range ps {
		out[i] = toEnvPresetResponse(p)
	}
	return out
}

// toEnvPresetResponsesWithLookup builds responses for a page of env presets
// using a pre-fetched ownerLookup to avoid N+1 owner name queries.
func toEnvPresetResponsesWithLookup(ps []store.EnvPreset, lk *ownerLookup) []EnvPresetResponse {
	out := make([]EnvPresetResponse, len(ps))
	for i, p := range ps {
		resp := toEnvPresetResponse(p)
		resp.Owner = lk.Build(p.OwnerType, p.OwnerID)
		out[i] = resp
	}
	return out
}
