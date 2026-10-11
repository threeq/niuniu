// Generic one-time credential-onboarding endpoints (issue #727).
//
// Two surfaces:
//
//   - Public, token-authenticated (mounted OUTSIDE the authenticated api group):
//     GET/POST /api/credential-onboarding/:token — render the declared fields and
//     redeem the link. The token IS the authorization; no submitted value is ever
//     echoed, logged, or returned.
//   - MCP, project-scoped (mounted under /mcp/projects/:id): issue a link and
//     query whether a target already holds a credential. The status query returns
//     ONLY a boolean — the agent never learns a secret.
//
// Every response on the public surface carries hardened headers (no-store,
// strict CSP, no-referrer); the accompanying SPA page carries the same CSP.
package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/credpolicy"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// credentialPageCSP is the strict policy for the credential submission page and
// its token-scoped APIs. default-src 'none' denies everything not explicitly
// listed, so no external origin can be loaded (fonts/CDNs/beacons included);
// 'unsafe-inline' is required because the SPA shell ships tiny inline
// bootstrap scripts, and the shell never interpolates the token into markup.
const credentialPageCSP = "default-src 'none'; " +
	"script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'"

// ApplyCredentialPageHeaders hardens a response that handles credential-entry
// material: never cached, never sent as a referrer, never rendered under a
// policy that could pull in a remote resource.
func ApplyCredentialPageHeaders(c *gin.Context) {
	h := c.Writer.Header()
	h.Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	h.Set("Pragma", "no-cache")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", credentialPageCSP)
}

// CredentialOnboardingHandler binds the generic credential-onboarding endpoints.
type CredentialOnboardingHandler struct {
	svc   *service.CredentialOnboardingService
	authz *service.Authz
}

func NewCredentialOnboardingHandler(svc *service.CredentialOnboardingService, authz *service.Authz) *CredentialOnboardingHandler {
	return &CredentialOnboardingHandler{svc: svc, authz: authz}
}

// GetCredentialLinkInfo handles GET /api/credential-onboarding/:token (public).
// Returns the non-secret descriptor the submission form renders from. Read-only:
// does NOT consume the token. 410 for unknown/expired/used tokens.
func (h *CredentialOnboardingHandler) GetCredentialLinkInfo(c *gin.Context) {
	ApplyCredentialPageHeaders(c)
	token := c.Param("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing token"})
		return
	}
	info, err := h.svc.Info(c.Request.Context(), token)
	if err != nil {
		if errors.Is(err, service.ErrCredentialTokenInvalid) {
			c.JSON(http.StatusGone, gin.H{"error": "invalid or expired token"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"kind": info.Kind, "title": info.Title, "fields": info.Fields})
}

// SubmitCredentialLink handles POST /api/credential-onboarding/:token (public).
// The body is a flat {fieldName: value} map; validation rejects undeclared
// fields and missing required ones. Nothing submitted is ever echoed back.
func (h *CredentialOnboardingHandler) SubmitCredentialLink(c *gin.Context) {
	ApplyCredentialPageHeaders(c)
	token := c.Param("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing token"})
		return
	}
	var values map[string]any
	if err := c.ShouldBindJSON(&values); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	result, err := h.svc.Submit(c.Request.Context(), token, values)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrCredentialTokenInvalid):
			c.JSON(http.StatusGone, gin.H{"error": "invalid or expired token"})
		case errors.Is(err, service.ErrCredentialSubmissionInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		}
		return
	}
	// Never echo a submitted value — only the non-secret identifier(s).
	c.JSON(http.StatusOK, gin.H{
		"ok":            true,
		"kind":          result.Kind,
		"credential_id": result.CredentialID,
		"source_id":     result.SourceID,
		"name":          result.Name,
		"provider":      result.Provider,
		"alias":         result.Alias,
	})
}

// mcpProjectAccess resolves the project-scoped MCP caller (auth_user_id +
// CanAccessProject on :id), mirroring the IM Bot MCP handlers.
func (h *CredentialOnboardingHandler) mcpProjectAccess(c *gin.Context) (projectID, uid int64, ok bool) {
	projectID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || projectID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad project id"})
		return 0, 0, false
	}
	uid = c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return 0, 0, false
	}
	if _, aerr := h.authz.CanAccessProject(c.Request.Context(), uid, projectID); aerr != nil {
		writeAuthzError(c, aerr)
		return 0, 0, false
	}
	return projectID, uid, true
}

// MCPRequestCredentialLink handles POST /mcp/projects/:id/credential-links.
// Issues a one-time token and returns a root-relative submission path. The raw
// token lives only inside the URL — never in another response field.
func (h *CredentialOnboardingHandler) MCPRequestCredentialLink(c *gin.Context) {
	pid, uid, ok := h.mcpProjectAccess(c)
	if !ok {
		return
	}
	var body struct {
		Kind   string         `json:"kind" binding:"required"`
		Title  string         `json:"title"`
		Fields []string       `json:"fields"`
		Target map[string]any `json:"target"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rawToken, err := h.svc.Issue(c.Request.Context(), pid, uid, body.Kind, body.Title, body.Fields, body.Target)
	if err != nil {
		if errors.Is(err, service.ErrCredentialLinkInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	// Root-relative path: the user is already on the right origin (personal:
	// 127.0.0.1, team: the domain), so one path works for both editions and the
	// server never has to guess its own public address from spoofable headers.
	linkPath := "/credential/" + rawToken
	c.JSON(http.StatusOK, gin.H{
		"url":                linkPath,
		"link_markdown":      "[点击打开凭据录入页面](" + linkPath + ")",
		"expires_in_seconds": int(service.CredentialOnboardingTTL().Seconds()),
		"note":               credpolicy.LinkNote,
	})
}

// MCPCredentialStatus handles POST /mcp/projects/:id/credential-links/status.
// Returns ONLY {has_credential: bool} — the agent can confirm a secret landed
// without ever seeing it.
func (h *CredentialOnboardingHandler) MCPCredentialStatus(c *gin.Context) {
	pid, _, ok := h.mcpProjectAccess(c)
	if !ok {
		return
	}
	var body struct {
		Kind   string         `json:"kind" binding:"required"`
		Target map[string]any `json:"target"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	has, err := h.svc.HasCredential(c.Request.Context(), pid, body.Kind, body.Target)
	if err != nil {
		if errors.Is(err, service.ErrCredentialLinkInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"has_credential": has})
}
