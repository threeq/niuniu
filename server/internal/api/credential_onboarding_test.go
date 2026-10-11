// credential_onboarding_test.go — handler tests for the generic one-time
// credential-link endpoints (issue #727): the public token-authenticated
// info/submit pair, the MCP issue/status pair, the hardened response headers,
// and the issue rate limit. Real router wiring, in-memory SQLite, no mocks.
package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/auth"
	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// setupCredOnboardingAPI wires the credential-onboarding routes exactly as
// router.go does, over a real MCP token gate.
func setupCredOnboardingAPI(t *testing.T) (r *gin.Engine, tok1 string, proj1 int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openMCPPermTestDB(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)
	ctx := context.Background()

	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','admin')`)
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'p1', 'user', 1)`)
	proj1 = 1

	kr, err := crypto.LoadOrCreate(t.TempDir() + "/kr")
	require.NoError(t, err)

	authz := service.NewAuthz(q, db)
	ext := service.NewExternalCredentialService(q, db, kr, integration.NewRegistry())
	ds := service.NewDataSourceService(q, db, kr, dataconn.NewRegistry())
	svc := service.NewCredentialOnboardingService(q, ext, ds)
	handler := api.NewCredentialOnboardingHandler(svc, authz)

	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		Name: "ws1", Path: "/tmp/ws1", Status: "created", OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	mustExec(t, db, `UPDATE workspaces SET current_session_user_id=1 WHERE id=?`, ws.ID)

	r = gin.New()
	// Public, token-authenticated surface (mounted outside the api group).
	r.GET("/api/credential-onboarding/:token", handler.GetCredentialLinkInfo)
	r.POST("/api/credential-onboarding/:token", handler.SubmitCredentialLink)

	// MCP, project-scoped surface — same middleware + rate limit as router.go.
	mcpSess := service.NewMCPSessionService(q)
	g := r.Group("/mcp")
	g.Use(api.LocalhostOnly())
	g.Use(api.MCPTokenAuth(mcpSess, store.New(db)))
	g.POST("/projects/:id/credential-links",
		auth.RateLimitMiddleware(0.2, 5),
		handler.MCPRequestCredentialLink)
	g.POST("/projects/:id/credential-links/status", handler.MCPCredentialStatus)

	tok1 = mintMCPToken(t, db, ws.ID)
	return r, tok1, proj1
}

// issueLink drives the MCP issue endpoint and returns the parsed response.
func issueLink(t *testing.T, r *gin.Engine, token, projectID, body string) map[string]any {
	t.Helper()
	w := mcpPOST(t, r, "/mcp/projects/"+projectID+"/credential-links", token, body)
	require.Equalf(t, http.StatusOK, w.Code, "issue link failed: %s", w.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

// TestCredentialOnboardingAPI_EndToEnd: AI issues a link → user submits in the
// browser → the AI can only learn has_credential. Nothing submitted is echoed.
func TestCredentialOnboardingAPI_EndToEnd(t *testing.T) {
	r, tok, pid := setupCredOnboardingAPI(t)
	pidStr := "1"

	// 1. The agent mints a link.
	issued := issueLink(t, r, tok, pidStr, `{
		"kind":"provider_credential","title":"GitHub PAT","fields":["token"],
		"target":{"provider":"github","alias":"Work GitHub","auth_mode":"bearer"}}`)
	url, _ := issued["url"].(string)
	require.True(t, strings.HasPrefix(url, "/credential/"), "url should be root-relative, got %q", url)
	linkPath := strings.TrimPrefix(url, "/credential/")
	require.NotEmpty(t, linkPath)
	require.Contains(t, issued["note"], "绝不在对话里索取")

	// 2. The browser fetches the field descriptor.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/credential-onboarding/"+linkPath, nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var info map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &info))
	assert.Equal(t, "provider_credential", info["kind"])
	assert.Equal(t, "GitHub PAT", info["title"])
	fields, _ := info["fields"].([]any)
	require.Len(t, fields, 1)

	// 3. The user submits the secret.
	body := `{"token":"ghp_e2e_secret"}`
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/credential-onboarding/"+linkPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	// The response must never echo the submitted secret.
	assert.NotContains(t, w.Body.String(), "ghp_e2e_secret")
	var submit map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &submit))
	assert.Equal(t, true, submit["ok"])
	assert.Greater(t, submit["credential_id"].(float64), float64(0))

	// 4. The agent can only ask whether a credential exists.
	w = mcpPOST(t, r, "/mcp/projects/"+pidStr+"/credential-links/status", tok,
		`{"kind":"provider_credential","target":{"provider":"github","alias":"Work GitHub"}}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"has_credential":true}`, w.Body.String())
	assert.NotContains(t, w.Body.String(), "ghp_e2e_secret")

	// 5. The link is single-use.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/credential-onboarding/"+linkPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusGone, w.Code)

	_ = pid
}

// TestCredentialOnboardingAPI_SecurityHeaders: every public-surface response
// carries no-store / no-referrer / a strict CSP that forbids external loads.
func TestCredentialOnboardingAPI_SecurityHeaders(t *testing.T) {
	r, _, _ := setupCredOnboardingAPI(t)

	// Unknown token still gets the hardened headers (an error page is still a
	// credential-handling page).
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/credential-onboarding/nope", nil))
	assert.Equal(t, http.StatusGone, w.Code)

	assert.Contains(t, w.Header().Get("Cache-Control"), "no-store")
	assert.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))
	csp := w.Header().Get("Content-Security-Policy")
	require.NotEmpty(t, csp)
	assert.Contains(t, csp, "default-src 'none'")
	assert.NotContains(t, csp, "https://", "CSP must not allow any external origin")
	assert.Contains(t, csp, "frame-ancestors 'none'")

	// Submit carries them too.
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/credential-onboarding/nope", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Contains(t, w.Header().Get("Cache-Control"), "no-store")
	assert.NotEmpty(t, w.Header().Get("Content-Security-Policy"))
}

// TestCredentialOnboardingAPI_SubmitValidation: undeclared fields and missing
// required fields are rejected with 400.
func TestCredentialOnboardingAPI_SubmitValidation(t *testing.T) {
	r, tok, _ := setupCredOnboardingAPI(t)

	issued := issueLink(t, r, tok, "1", `{
		"kind":"data_source","title":"MySQL","fields":["host","password"],
		"target":{"name":"db","kind":"mysql"}}`)
	linkPath := strings.TrimPrefix(issued["url"].(string), "/credential/")

	post := func(body string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credential-onboarding/"+linkPath, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code
	}
	assert.Equal(t, http.StatusBadRequest, post(`{"host":"h","password":"p","nope":"x"}`))
	assert.Equal(t, http.StatusBadRequest, post(`{"host":"h"}`))
	// A valid submission then succeeds (the failures did not consume the token).
	assert.Equal(t, http.StatusOK, post(`{"host":"h","password":"p"}`))
}

// TestCredentialOnboardingAPI_IssueRateLimited: the issue endpoint is throttled
// so a caller cannot farm links.
func TestCredentialOnboardingAPI_IssueRateLimited(t *testing.T) {
	r, tok, _ := setupCredOnboardingAPI(t)
	body := `{"kind":"provider_credential","title":"t","fields":["token"],
		"target":{"provider":"github","alias":"rate-limited"}}`

	// burst = 5 → the 6th immediate request is rejected.
	statuses := make([]int, 0, 6)
	for i := 0; i < 6; i++ {
		statuses = append(statuses, mcpPOST(t, r, "/mcp/projects/1/credential-links", tok, body).Code)
	}
	for i := 0; i < 5; i++ {
		require.Equalf(t, http.StatusOK, statuses[i], "request %d should pass (statuses=%v)", i+1, statuses)
	}
	assert.Equalf(t, http.StatusTooManyRequests, statuses[5], "6th request should be rate limited (statuses=%v)", statuses)
}

// TestCredentialOnboardingAPI_UnauthorizedRejected: issuing requires the MCP
// session gate — an unknown token never mints a link.
func TestCredentialOnboardingAPI_UnauthorizedRejected(t *testing.T) {
	r, _, _ := setupCredOnboardingAPI(t)
	w := mcpPOST(t, r, "/mcp/projects/1/credential-links", "not-a-real-token",
		`{"kind":"provider_credential","title":"t","fields":["token"],"target":{"provider":"github","alias":"x"}}`)
	assert.NotEqual(t, http.StatusOK, w.Code)
}
