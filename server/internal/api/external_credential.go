// External-credential REST endpoints. Routes live under /api/me/* (caller-
// scoped) — owner is always the caller's personal owner in v1; org-scoped
// credentials wait until the SPA grows an org-context switcher and a
// matching ?owner=org:<id> query string. Until then we deliberately do
// not surface the org case so we don't ship dead code.
//
// Since 2026-05-17 credentials are addressed by id rather than (owner, user,
// provider). Each credential carries an alias so users can maintain multiple
// credentials per provider.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// nullTimeToPtr converts a sql.NullTime into a *time.Time so the JSON
// encoder produces an RFC3339 string (or `null`), matching the SPA's
// `last_verified_at: string | null` contract.
func nullTimeToPtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	return &nt.Time
}

// ExternalCredentialHandler binds the credential endpoints. The handler is a
// thin pass-through over the service: validation (provider/token shape) lives
// here; encryption / persistence / provider dispatch lives in the service.
type ExternalCredentialHandler struct {
	svc   *service.ExternalCredentialService
	authz *service.Authz
}

func NewExternalCredentialHandler(svc *service.ExternalCredentialService, authz *service.Authz) *ExternalCredentialHandler {
	return &ExternalCredentialHandler{svc: svc, authz: authz}
}

// resolveCallerOwner reads the caller's user_id from the gin context
// (set by auth.IdentityResolver as "auth_user_id") and returns it as
// the owner triple (owner_type='user', owner_id=user_id, user_id).
func (h *ExternalCredentialHandler) resolveCallerOwner(c *gin.Context) (ownerType string, ownerID, userID int64, ok bool) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return "", 0, 0, false
	}
	return "user", uid, uid, true
}

// List returns metadata for every credential the caller has configured
// in their personal owner. Tokens are redacted by the service.
func (h *ExternalCredentialHandler) List(c *gin.Context) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	// Resolve the target owner from the query (defaults to the caller's
	// personal owner). For org owners any member may list (EnsureOwnerWritable
	// allows any org member); for the user owner the id must equal the caller.
	owner := service.OwnerRef{Type: "user", ID: uid}
	if t := c.Query("owner_type"); t != "" {
		id, err := strconv.ParseInt(c.Query("owner_id"), 10, 64)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid owner_id"})
			return
		}
		owner = service.OwnerRef{Type: t, ID: id}
	}
	if err := h.authz.EnsureOwnerWritable(c.Request.Context(), uid, owner); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	provider := integration.ProviderName(c.Query("provider"))

	var creds []service.ExternalCredentialDecoded
	var err error
	switch owner.Type {
	case "org":
		// Org credentials are owner-scoped: every member sees the whole org's
		// credentials regardless of which member created them.
		creds, err = h.svc.ListForOwner(c.Request.Context(), owner.Type, owner.ID, provider)
	default:
		// Personal owner — preserve the existing per-user listing behavior,
		// optionally filtered by provider.
		if provider != "" {
			creds, err = h.svc.ListByProvider(c.Request.Context(), owner.Type, owner.ID, uid, provider)
		} else {
			creds, err = h.svc.List(c.Request.Context(), owner.Type, owner.ID, uid)
		}
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]gin.H, len(creds))
	for i, cr := range creds {
		out[i] = gin.H{
			"id":               cr.ID,
			"provider":         cr.Provider,
			"alias":            cr.Alias,
			"last_verified_at": nullTimeToPtr(cr.LastVerifiedAt),
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// createCredentialBody is the POST body shape. Two shapes are accepted:
//
//  1. Generic (preferred): `config` is a free-form object whose contents
//     the proxy reads at call time (e.g. `{token: ...}` for bearer auth,
//     `{api_user, api_password}` for basic auth). This is the only shape
//     that works for user-defined providers.
//
//  2. Legacy named fields (token/api_user/.../site/email): retained so
//     pre-existing SPA code paths keep working until the provider-driven
//     credential dialog ships everywhere. When `config` is present it
//     wins; otherwise the named fields are folded into a config object
//     using the per-provider conventions below.
type createCredentialBody struct {
	Provider string         `json:"provider" binding:"required"`
	Alias    string         `json:"alias" binding:"required"`
	Config   map[string]any `json:"config,omitempty"`

	// Owner — optional target owner for the credential. Omitted (or
	// {type:"user", id:0}) means the caller's personal owner. An org owner
	// requires the caller to be an org admin (see Create).
	Owner *struct {
		Type string `json:"type"`
		ID   int64  `json:"id"`
	} `json:"owner,omitempty"`

	// Legacy named fields — kept for backward compatibility.
	Token       string `json:"token,omitempty"`
	ApiUser     string `json:"api_user,omitempty"`
	ApiPassword string `json:"api_password,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
	Site        string `json:"site,omitempty"`
	Email       string `json:"email,omitempty"`
}

// buildLegacyConfig folds the named per-provider fields into a config map
// using the conventions the previous adapter stack relied on. Returns nil
// when there is nothing to fold (caller treats that as "no config" and
// rejects).
func buildLegacyConfig(b createCredentialBody) map[string]any {
	switch integration.ProviderName(b.Provider) {
	case integration.ProviderGitHub:
		if b.Token == "" {
			return nil
		}
		return map[string]any{"token": b.Token}
	case integration.ProviderTAPD:
		if b.AccessToken != "" {
			cfg := map[string]any{"access_token": b.AccessToken}
			if b.ApiUser != "" {
				cfg["api_user"] = b.ApiUser
			}
			return cfg
		}
		if b.ApiUser != "" && b.ApiPassword != "" {
			return map[string]any{"api_user": b.ApiUser, "api_password": b.ApiPassword}
		}
		return nil
	case integration.ProviderJira:
		if b.Site == "" || b.Email == "" || b.Token == "" {
			return nil
		}
		return map[string]any{"site": b.Site, "email": b.Email, "token": b.Token}
	}
	// Unknown provider with named fields: best-effort fold of the most
	// common bearer shape so a stray pre-existing client still works.
	if b.Token != "" {
		return map[string]any{"token": b.Token}
	}
	return nil
}

// Create inserts a new credential. Alias is required; duplicates in the
// same (owner, user, provider) scope return 409.
//
// Provider is matched by string against external_providers.name — any
// provider listed in /api/me/external-proxy/providers is acceptable,
// including user-defined ones. The legacy hardcoded github/tapd/jira
// validation has been removed because the credential is now consumed by
// the generic proxy (server/internal/service/external_proxy.go), which
// looks for `token`/`api_key`/`password` in the config at call time.
func (h *ExternalCredentialHandler) Create(c *gin.Context) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	var b createCredentialBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Resolve the target owner (default: caller's personal owner). Credentials
	// are secrets, so org ownership requires org-admin (CanManageOrg), stricter
	// than the member-level visibility used for listing (spec 规约5).
	owner := service.OwnerRef{Type: "user", ID: uid}
	if b.Owner != nil && b.Owner.Type != "" && !(b.Owner.Type == "user" && b.Owner.ID == 0) {
		owner = service.OwnerRef{Type: b.Owner.Type, ID: b.Owner.ID}
	}
	switch owner.Type {
	case "user":
		if owner.ID != uid {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
	case "org":
		if err := h.authz.CanManageOrg(c.Request.Context(), uid, owner.ID); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden: org admin required"})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid owner type"})
		return
	}
	if strings.TrimSpace(b.Provider) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider required"})
		return
	}
	rawConfig := b.Config
	if len(rawConfig) == 0 {
		rawConfig = buildLegacyConfig(b)
	}
	if len(rawConfig) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "config required (non-empty object)"})
		return
	}
	// Sanity-check the GitHub PAT shape on the way in — historically this
	// was the most common copy/paste mistake; keep the cheap check.
	if b.Provider == string(integration.ProviderGitHub) {
		if tok, _ := rawConfig["token"].(string); tok != "" {
			if !strings.HasPrefix(tok, "ghp_") && !strings.HasPrefix(tok, "github_pat_") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "GitHub token must start with ghp_ or github_pat_"})
				return
			}
		}
	}
	row, err := h.svc.Create(c.Request.Context(), service.ExternalCredentialUpsertInput{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
		UserID:    uid,
		Provider:  integration.ProviderName(b.Provider),
		Alias:     b.Alias,
		RawConfig: rawConfig,
	})
	if err != nil {
		if errors.Is(err, service.ErrAliasInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, service.ErrAliasDuplicated) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": row.ID, "provider": row.Provider, "alias": row.Alias})
}

// patchBody — PATCH /me/external-credentials/:id accepts any subset of:
//   - alias  : rename the credential
//   - config : replace the encrypted auth payload (re-enter token, etc.)
// Either alone, or both together. Empty PATCH is a no-op (200).
type patchBody struct {
	Alias  *string        `json:"alias,omitempty"`
	Config map[string]any `json:"config,omitempty"`
}

// Patch applies a partial update — alias rename and/or config rewrite —
// to an existing credential. Splits the work into the two existing
// service calls (Rename, UpdateConfig); preserves the legacy 1-call-per
// -field error mapping.
func (h *ExternalCredentialHandler) Patch(c *gin.Context) {
	ot, oid, uid, ok := h.resolveCallerOwner(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad credential id"})
		return
	}
	var b patchBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if b.Alias == nil && b.Config == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "patch body must include alias or config"})
		return
	}

	var row *service.ExternalCredentialDecoded
	if b.Config != nil {
		if len(b.Config) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "config must be a non-empty object"})
			return
		}
		row, err = h.svc.UpdateConfig(c.Request.Context(), id, ot, oid, uid, b.Config)
		if err != nil {
			if errors.Is(err, service.ErrNoCredential) {
				c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if b.Alias != nil {
		row, err = h.svc.Rename(c.Request.Context(), id, ot, oid, uid, *b.Alias)
		if err != nil {
			if errors.Is(err, service.ErrNoCredential) {
				c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
				return
			}
			if errors.Is(err, service.ErrAliasInvalid) {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			if errors.Is(err, service.ErrAliasDuplicated) {
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if row == nil {
		// Unreachable — guarded by the alias/config presence check above.
		c.Status(http.StatusOK)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": row.ID, "provider": row.Provider, "alias": row.Alias})
}

// VerifyImap performs a bind-time IMAP LOGIN probe for an imap credential so
// the SPA can give the user immediate "works / wrong password" feedback (spec
// §11). 200 {ok:true} on success; 422 {error_kind:"auth"} on credential
// rejection; 502 on connect/IO failure; 404 when not found.
func (h *ExternalCredentialHandler) VerifyImap(c *gin.Context) {
	ot, oid, uid, ok := h.resolveCallerOwner(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad credential id"})
		return
	}
	if err := h.svc.VerifyImapByID(c.Request.Context(), id, ot, oid, uid); err != nil {
		if errors.Is(err, service.ErrNoCredential) {
			c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
			return
		}
		var authErr *service.ErrIMAPAuth
		if errors.As(err, &authErr) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": authErr.Error(), "error_kind": "auth"})
			return
		}
		if errors.Is(err, service.ErrCredentialProviderMismatch) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// connect / TLS / IO failure — distinct from auth so the SPA can say
		// "couldn't reach the mail server" rather than "wrong password".
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "error_kind": "connect"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Usages returns the list of project_external_sources rows that
// reference this credential — the same shape the 409 Delete-in-use
// response carries. Lets the SPA show "this credential is used by N
// project(s)" inline + a "remove binding" affordance per row.
func (h *ExternalCredentialHandler) Usages(c *gin.Context) {
	_, _, _, ok := h.resolveCallerOwner(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad credential id"})
		return
	}
	sources, err := h.svc.ListUsages(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	items := make([]gin.H, len(sources))
	for i, s := range sources {
		items[i] = gin.H{
			"id":           s.ID,
			"project_id":   s.ProjectID,
			"project_name": s.ProjectName,
			"provider":     s.Provider,
			"source_key":   s.SourceKey,
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// Delete removes a credential by id. If the credential is in use by any
// project external sources, returns 409 with the in_use_by list.
func (h *ExternalCredentialHandler) Delete(c *gin.Context) {
	ot, oid, uid, ok := h.resolveCallerOwner(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad credential id"})
		return
	}
	if err := h.svc.DeleteByID(c.Request.Context(), id, ot, oid, uid); err != nil {
		var inUse *service.ErrCredentialInUse
		if errors.As(err, &inUse) {
			sources := make([]gin.H, len(inUse.Sources))
			for i, s := range inUse.Sources {
				sources[i] = gin.H{
					"id":           s.ID,
					"project_id":   s.ProjectID,
					"project_name": s.ProjectName,
					"provider":     s.Provider,
					"source_key":   s.SourceKey,
				}
			}
			c.JSON(http.StatusConflict, gin.H{
				"error":      err.Error(),
				"error_kind": "in_use",
				"in_use_by":  sources,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
