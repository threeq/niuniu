package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type EnvAccountHandler struct {
	svc   *service.EnvAccountService
	Authz *service.Authz
	DB    *sql.DB // used for batch owner-name lookup on list endpoints
}

func NewEnvAccountHandler(svc *service.EnvAccountService) *EnvAccountHandler {
	return &EnvAccountHandler{svc: svc}
}

type CreateEnvAccountRequest struct {
	Name        string `json:"name" binding:"required"`
	Platform    string `json:"platform"`
	Description string `json:"description"`
	ApiKey      string `json:"api_key"`
	Owner       *struct {
		Type string `json:"type"`
		ID   int64  `json:"id"`
	} `json:"owner,omitempty"`
}

type UpdateEnvAccountRequest struct {
	Name        string `json:"name" binding:"required"`
	Platform    string `json:"platform"`
	Description string `json:"description"`
	ApiKey      string `json:"api_key"`
}

func (h *EnvAccountHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	var presets []store.EnvAccount
	var err error
	if userID > 0 {
		presets, err = h.svc.ListForUser(ctx, userID)
	} else {
		presets, err = h.svc.List(ctx)
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
		c.JSON(http.StatusOK, toEnvAccountResponsesWithLookup(presets, lk))
		return
	}
	c.JSON(http.StatusOK, toEnvAccountResponses(presets))
}

func (h *EnvAccountHandler) Get(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid env account ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessEnvAccount(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	account, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		slog.Warn("GetEnvAccount failed", "id", id, "error", err)
		NotFound(c, "ENV_ACCOUNT")
		return
	}
	c.JSON(http.StatusOK, toEnvAccountResponse(account))
}

func (h *EnvAccountHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req CreateEnvAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
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
	account, err := h.svc.Create(c.Request.Context(), req.Name, req.Platform, req.Description, req.ApiKey, owner.Type, owner.ID)
	if err != nil {
		if isUniqueViolation(err) {
			BadRequest(c, "account name already exists")
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toEnvAccountResponse(account))
}

func (h *EnvAccountHandler) Update(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid env account ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessEnvAccount(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req UpdateEnvAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if err := h.svc.Update(c.Request.Context(), id, req.Name, req.Platform, req.Description, req.ApiKey); err != nil {
		slog.Warn("UpdateEnvAccount failed", "id", id, "error", err)
		if isUniqueViolation(err) {
			BadRequest(c, "account name already exists")
			return
		}
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *EnvAccountHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid env account ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessEnvAccount(c.Request.Context(), userID, id); err != nil {
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

type EnvAccountResponse struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Platform    string   `json:"platform"`
	Description string   `json:"description"`
	ApiKey      string   `json:"api_key"`
	Owner       OwnerDTO `json:"owner"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

func toEnvAccountResponse(a store.EnvAccount) EnvAccountResponse {
	return EnvAccountResponse{
		ID:          a.ID,
		Name:        a.Name,
		Platform:    a.Platform,
		Description: a.Description,
		ApiKey:      a.ApiKey,
		Owner:       ownerDTOFromRef(a.OwnerType, a.OwnerID),
		CreatedAt:   a.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   a.UpdatedAt.Format(time.RFC3339),
	}
}

func toEnvAccountResponses(as []store.EnvAccount) []EnvAccountResponse {
	out := make([]EnvAccountResponse, len(as))
	for i, a := range as {
		out[i] = toEnvAccountResponse(a)
	}
	return out
}

func toEnvAccountResponsesWithLookup(as []store.EnvAccount, lk *ownerLookup) []EnvAccountResponse {
	out := make([]EnvAccountResponse, len(as))
	for i, a := range as {
		resp := toEnvAccountResponse(a)
		resp.Owner = lk.Build(a.OwnerType, a.OwnerID)
		out[i] = resp
	}
	return out
}