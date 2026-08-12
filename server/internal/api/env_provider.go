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

type EnvProviderHandler struct {
	svc        *service.EnvProviderService
	presetSvc  *service.EnvPresetService
	accountSvc *service.EnvAccountService
	Authz      *service.Authz
	DB         *sql.DB // used for batch owner-name lookup on list endpoints
}

func NewEnvProviderHandler(svc *service.EnvProviderService, presetSvc *service.EnvPresetService, accountSvc *service.EnvAccountService) *EnvProviderHandler {
	return &EnvProviderHandler{svc: svc, presetSvc: presetSvc, accountSvc: accountSvc}
}

type CreateEnvProviderRequest struct {
	Name          string            `json:"name" binding:"required"`
	Platform      string            `json:"platform"`
	Description   string            `json:"description"`
	BaseUrls      map[string]string `json:"base_urls"`
	ApiKey        string            `json:"api_key"`
	Model         string            `json:"model"`
	HaikuModel    string            `json:"haiku_model"`
	SonnetModel   string            `json:"sonnet_model"`
	OpusModel     string            `json:"opus_model"`
	SubagentModel string            `json:"subagent_model"`
	ExtraEnv      map[string]string `json:"extra_env"`
	Owner         *struct {
		Type string `json:"type"`
		ID   int64  `json:"id"`
	} `json:"owner,omitempty"`
}

type UpdateEnvProviderRequest struct {
	Name          string            `json:"name" binding:"required"`
	Platform      string            `json:"platform"`
	Description   string            `json:"description"`
	BaseUrls      map[string]string `json:"base_urls"`
	ApiKey        string            `json:"api_key"`
	Model         string            `json:"model"`
	HaikuModel    string            `json:"haiku_model"`
	SonnetModel   string            `json:"sonnet_model"`
	OpusModel     string            `json:"opus_model"`
	SubagentModel string            `json:"subagent_model"`
	ExtraEnv      map[string]string `json:"extra_env"`
}

// validateProvider checks the provider payload: api_key, when set, must be a
// "${ACCOUNT:<name>}" reference — the credential always lives in an account,
// never inline. base_urls keys are validated to be known protocols.
func validateProvider(baseUrls map[string]string, apiKey string) string {
	for proto := range baseUrls {
		if proto != "anthropic" && proto != "openai" {
			return "base_urls keys must be 'anthropic' or 'openai'"
		}
	}
	if apiKey != "" && !isAccountKeyRef(apiKey) {
		return "api_key must reference an account (${ACCOUNT:name})"
	}
	return ""
}

// isAccountKeyRef reports whether v is exactly "${ACCOUNT:<name>}".
func isAccountKeyRef(v string) bool {
	prefix := "${ACCOUNT:"
	return len(v) > len(prefix)+1 &&
		strings.HasPrefix(v, prefix) &&
		strings.HasSuffix(v, "}") &&
		!strings.Contains(v[len(prefix):len(v)-1], "}")
}

func (h *EnvProviderHandler) List(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	ctx := c.Request.Context()
	var providers []store.EnvProvider
	var err error
	if userID > 0 {
		providers, err = h.svc.ListForUser(ctx, userID)
	} else {
		providers, err = h.svc.List(ctx)
	}
	if err != nil {
		InternalError(c, err)
		return
	}
	if h.DB != nil {
		refs := make([]ownerRef, len(providers))
		for i, p := range providers {
			refs[i] = ownerRef{p.OwnerType, p.OwnerID}
		}
		lk, _ := newOwnerLookup(ctx, h.DB, refs)
		c.JSON(http.StatusOK, toEnvProviderResponsesWithLookup(providers, lk))
		return
	}
	c.JSON(http.StatusOK, toEnvProviderResponses(providers))
}

func (h *EnvProviderHandler) Get(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid env provider ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessEnvProvider(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	provider, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		slog.Warn("GetEnvProvider failed", "id", id, "error", err)
		NotFound(c, "ENV_PROVIDER")
		return
	}
	c.JSON(http.StatusOK, toEnvProviderResponse(provider))
}

func (h *EnvProviderHandler) Create(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	var req CreateEnvProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if msg := validateProvider(req.BaseUrls, req.ApiKey); msg != "" {
		BadRequest(c, msg)
		return
	}
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
	extra, _ := json.Marshal(req.ExtraEnv)
	baseURLs, _ := json.Marshal(req.BaseUrls)
	provider, err := h.svc.Create(c.Request.Context(), store.EnvProvider{
		Name: req.Name, Platform: req.Platform, Description: req.Description,
		BaseUrls: string(baseURLs), ApiKey: req.ApiKey, Model: req.Model,
		HaikuModel: req.HaikuModel, SonnetModel: req.SonnetModel, OpusModel: req.OpusModel,
		SubagentModel: req.SubagentModel, ExtraEnv: string(extra),
		OwnerType: owner.Type, OwnerID: owner.ID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			BadRequest(c, "provider name already exists")
			return
		}
		InternalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toEnvProviderResponse(provider))
}

func (h *EnvProviderHandler) Update(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid env provider ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessEnvProvider(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req UpdateEnvProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	if msg := validateProvider(req.BaseUrls, req.ApiKey); msg != "" {
		BadRequest(c, msg)
		return
	}
	extra, _ := json.Marshal(req.ExtraEnv)
	baseURLs, _ := json.Marshal(req.BaseUrls)
	if err := h.svc.Update(c.Request.Context(), id, store.EnvProvider{
		Name: req.Name, Platform: req.Platform, Description: req.Description,
		BaseUrls: string(baseURLs), ApiKey: req.ApiKey, Model: req.Model,
		HaikuModel: req.HaikuModel, SonnetModel: req.SonnetModel, OpusModel: req.OpusModel,
		SubagentModel: req.SubagentModel, ExtraEnv: string(extra),
	}); err != nil {
		slog.Warn("UpdateEnvProvider failed", "id", id, "error", err)
		if isUniqueViolation(err) {
			BadRequest(c, "provider name already exists")
			return
		}
		InternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *EnvProviderHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid env provider ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessEnvProvider(c.Request.Context(), userID, id); err != nil {
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

// Env returns the environment key/value the provider expands to for a given
// agent CLI type (?cli_type=claude|codex|qwen|omp|goose). Used to preview the
// auto-generated mapping before applying.
func (h *EnvProviderHandler) Env(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid env provider ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessEnvProvider(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	provider, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		NotFound(c, "ENV_PROVIDER")
		return
	}
	accounts, _ := h.accountSvc.List(c.Request.Context())
	// Preview resolves an account ref to the real key so the user sees what the
	// agent will actually use (masked in the UI).
	env := h.svc.Expand(c.Request.Context(), provider, c.DefaultQuery("cli_type", ""), accounts, false)
	c.JSON(http.StatusOK, env)
}

type ImportProviderRequest struct {
	CliType    string `json:"cli_type"`     // claude|codex|qwen|omp|goose (""=claude)
	PresetName string `json:"preset_name"`  // optional; defaults to provider name
	Overwrite  bool   `json:"overwrite"`    // update an existing preset of that name
	Owner      *struct {
		Type string `json:"type"`
		ID   int64  `json:"id"`
	} `json:"owner,omitempty"`
}

// Import expands a provider for the given agent type and creates (or, with
// overwrite, updates) an env_preset whose env is the generated mapping. The
// preset then flows through the existing scene→workspace env mechanism, so the
// provider's unified config reaches any Agent that mounts the preset.
func (h *EnvProviderHandler) Import(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid env provider ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessEnvProvider(c.Request.Context(), userID, id); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	var req ImportProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}
	provider, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		NotFound(c, "ENV_PROVIDER")
		return
	}
	accounts, _ := h.accountSvc.List(c.Request.Context())
	// Import preserves a ${ACCOUNT:<name>} reference so the resulting preset stays
	// runtime-live: changing the account key propagates to every agent that mounts
	// the preset, without re-importing.
	env := h.svc.Expand(c.Request.Context(), provider, req.CliType, accounts, true)
	if len(env) == 0 {
		BadRequest(c, "provider has no env to import")
		return
	}
	name := req.PresetName
	if name == "" {
		name = provider.Name
	}
	// Resolve owner: explicit body wins, else provider's owner, else caller.
	owner := service.OwnerRef{Type: provider.OwnerType, ID: provider.OwnerID}
	if req.Owner != nil && req.Owner.Type != "" && !(req.Owner.Type == "user" && req.Owner.ID == 0) {
		owner = service.OwnerRef{Type: req.Owner.Type, ID: req.Owner.ID}
	}
	if userID > 0 && h.Authz != nil {
		if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), userID, owner); err != nil {
			writeAuthzError(c, err)
			return
		}
	}
	// Overwrite: find existing preset by name among those the owner can edit.
	if req.Overwrite {
		presets, lerr := h.presetSvc.ListForUser(c.Request.Context(), userID)
		if lerr == nil {
			for _, p := range presets {
				if p.Name == name && p.OwnerType == owner.Type && p.OwnerID == owner.ID {
					if uerr := h.presetSvc.Update(c.Request.Context(), p.ID, name, "由订阅平台 Provider「"+provider.Name+"」生成", env); uerr == nil {
						c.JSON(http.StatusOK, gin.H{"preset_id": p.ID, "preset_name": name, "updated": true})
						return
					}
				}
			}
		}
	}
	preset, cerr := h.presetSvc.Create(c.Request.Context(), name, "由订阅平台 Provider「"+provider.Name+"」生成", env, owner.Type, owner.ID)
	if cerr != nil {
		if isUniqueViolation(cerr) {
			BadRequest(c, "preset name already exists; enable overwrite to update it")
			return
		}
		InternalError(c, cerr)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"preset_id": preset.ID, "preset_name": preset.Name, "updated": false})
}

// --- Response types and converters ---

type EnvProviderResponse struct {
	ID            int64             `json:"id"`
	Name          string            `json:"name"`
	Platform      string            `json:"platform"`
	Description   string            `json:"description"`
	BaseUrls      map[string]string `json:"base_urls"`
	ApiKey        string            `json:"api_key"`
	Model         string            `json:"model"`
	HaikuModel    string            `json:"haiku_model"`
	SonnetModel   string            `json:"sonnet_model"`
	OpusModel     string            `json:"opus_model"`
	SubagentModel string            `json:"subagent_model"`
	ExtraEnv      map[string]string `json:"extra_env"`
	Owner         OwnerDTO          `json:"owner"`
	CreatedAt     string            `json:"created_at"`
	UpdatedAt     string            `json:"updated_at"`
}

func toEnvProviderResponse(p store.EnvProvider) EnvProviderResponse {
	extra := map[string]string{}
	_ = json.Unmarshal([]byte(p.ExtraEnv), &extra)
	baseURLs := map[string]string{}
	_ = json.Unmarshal([]byte(p.BaseUrls), &baseURLs)
	return EnvProviderResponse{
		ID: p.ID, Name: p.Name, Platform: p.Platform, Description: p.Description,
		BaseUrls: baseURLs, ApiKey: p.ApiKey, Model: p.Model,
		HaikuModel: p.HaikuModel, SonnetModel: p.SonnetModel, OpusModel: p.OpusModel,
		SubagentModel: p.SubagentModel, ExtraEnv: extra,
		Owner: ownerDTOFromRef(p.OwnerType, p.OwnerID),
		CreatedAt: p.CreatedAt.Format(time.RFC3339),
		UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
	}
}

func toEnvProviderResponses(ps []store.EnvProvider) []EnvProviderResponse {
	out := make([]EnvProviderResponse, len(ps))
	for i, p := range ps {
		out[i] = toEnvProviderResponse(p)
	}
	return out
}

func toEnvProviderResponsesWithLookup(ps []store.EnvProvider, lk *ownerLookup) []EnvProviderResponse {
	out := make([]EnvProviderResponse, len(ps))
	for i, p := range ps {
		resp := toEnvProviderResponse(p)
		resp.Owner = lk.Build(p.OwnerType, p.OwnerID)
		out[i] = resp
	}
	return out
}