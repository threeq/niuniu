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
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupGitIdentityRouter wires a minimal Gin engine for the GitIdentityHandler
// with a middleware that fake-stamps auth_user_id from the X-Test-Caller-ID
// header (same fallback `callerID` honors in production handlers).
func setupGitIdentityRouter(t *testing.T) (*gin.Engine, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, q := niutest.SetupDBRaw(t)
	// Seed a user we can act as.
	u, err := q.CreateUser(context.Background(), store.CreateUserParams{
		Username:     "alice",
		PasswordHash: "x",
		DisplayName:  "Alice Niu",
		Role:         "member",
	})
	require.NoError(t, err)

	h := api.NewGitIdentityHandler(service.NewGitIdentityService(db))
	r := gin.New()
	r.GET("/api/me/git-identity", h.Get)
	r.PUT("/api/me/git-identity", h.Put)
	return r, u.ID
}

func reqWithCaller(t *testing.T, r *gin.Engine, method, path, body string, callerID int64) *httptest.ResponseRecorder {
	t.Helper()
	var br *strings.Reader
	if body != "" {
		br = strings.NewReader(body)
	}
	var req *http.Request
	if br != nil {
		req = httptest.NewRequest(method, path, br)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("X-Test-Caller-ID", itoa(callerID))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestGitIdentityHandler_Get_DefaultsToFallback(t *testing.T) {
	r, uid := setupGitIdentityRouter(t)
	w := reqWithCaller(t, r, http.MethodGet, "/api/me/git-identity", "", uid)
	require.Equal(t, http.StatusOK, w.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "", got["email"], "raw email empty when unset")
	assert.Equal(t, "Alice Niu", got["author"])
	assert.Equal(t, "alice@niuniu.local", got["address"])
}

func TestGitIdentityHandler_Put_AcceptsAndEchoes(t *testing.T) {
	r, uid := setupGitIdentityRouter(t)
	w := reqWithCaller(t, r, http.MethodPut, "/api/me/git-identity",
		`{"email":"alice@example.com"}`, uid)
	require.Equal(t, http.StatusOK, w.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "alice@example.com", got["email"])
	assert.Equal(t, "alice@example.com", got["address"], "PUT echoes resolved address too")
}

func TestGitIdentityHandler_Put_RejectsMalformed(t *testing.T) {
	r, uid := setupGitIdentityRouter(t)
	w := reqWithCaller(t, r, http.MethodPut, "/api/me/git-identity",
		`{"email":"not-an-email"}`, uid)
	require.Equal(t, http.StatusBadRequest, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ERR_INVALID_EMAIL", errObj["code"])
}

func TestGitIdentityHandler_Put_EmptyClears(t *testing.T) {
	r, uid := setupGitIdentityRouter(t)
	// First set
	w := reqWithCaller(t, r, http.MethodPut, "/api/me/git-identity",
		`{"email":"alice@example.com"}`, uid)
	require.Equal(t, http.StatusOK, w.Code)
	// Then clear
	w = reqWithCaller(t, r, http.MethodPut, "/api/me/git-identity",
		`{"email":""}`, uid)
	require.Equal(t, http.StatusOK, w.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "", got["email"])
	assert.Equal(t, "alice@niuniu.local", got["address"], "fallback returns after clear")
}

func TestGitIdentityHandler_Unauthenticated(t *testing.T) {
	r, _ := setupGitIdentityRouter(t)
	// No X-Test-Caller-ID header → callerID returns 0,false → handler 401.
	req := httptest.NewRequest(http.MethodGet, "/api/me/git-identity", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// --- per-(user, repo) override (v5.1) ---

func setupRepositoryIdentityRouter(t *testing.T) (*gin.Engine, int64, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, q := niutest.SetupDBRaw(t)
	ctx := context.Background()
	u, err := q.CreateUser(ctx, store.CreateUserParams{
		Username: "alice", PasswordHash: "x", DisplayName: "Alice Niu", Role: "member",
	})
	require.NoError(t, err)
	var repoID int64
	require.NoError(t, db.QueryRowContext(ctx,
		`INSERT INTO repositories (name, path, owner_type, owner_id) VALUES ('R1', '/r', 'user', ?) RETURNING id`,
		u.ID).Scan(&repoID))

	h := api.NewGitIdentityHandler(service.NewGitIdentityService(db))
	r := gin.New()
	r.GET("/api/me/repository-identities", h.ListRepositoryIdentities)
	r.GET("/api/me/repository-identities/:repo_id", h.GetRepositoryIdentity)
	r.PUT("/api/me/repository-identities/:repo_id", h.PutRepositoryIdentity)
	r.DELETE("/api/me/repository-identities/:repo_id", h.DeleteRepositoryIdentity)
	return r, u.ID, repoID
}

func TestRepositoryIdentity_Get_NoOverrideReturnsResolvedGlobal(t *testing.T) {
	r, uid, repoID := setupRepositoryIdentityRouter(t)
	w := reqWithCaller(t, r, http.MethodGet,
		"/api/me/repository-identities/"+itoa(repoID), "", uid)
	require.Equal(t, http.StatusOK, w.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, false, got["has_override"])
	assert.Equal(t, "Alice Niu", got["resolved_name"])
	// resolved_email falls back to <username>@niuniu.local since email not set
	assert.Equal(t, "alice@niuniu.local", got["resolved_email"])
}

func TestRepositoryIdentity_Put_StoresAndEchoes(t *testing.T) {
	r, uid, repoID := setupRepositoryIdentityRouter(t)
	w := reqWithCaller(t, r, http.MethodPut,
		"/api/me/repository-identities/"+itoa(repoID),
		`{"name":"Alice (Work)","email":"alice@work.com"}`, uid)
	require.Equal(t, http.StatusOK, w.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, true, got["has_override"])
	assert.Equal(t, "Alice (Work)", got["name"])
	assert.Equal(t, "alice@work.com", got["email"])
	assert.Equal(t, "alice@work.com", got["resolved_email"])
}

func TestRepositoryIdentity_Put_RejectsMalformedEmail(t *testing.T) {
	r, uid, repoID := setupRepositoryIdentityRouter(t)
	w := reqWithCaller(t, r, http.MethodPut,
		"/api/me/repository-identities/"+itoa(repoID),
		`{"name":"x","email":"not-an-email"}`, uid)
	require.Equal(t, http.StatusBadRequest, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_INVALID_EMAIL", errObj["code"])
}

func TestRepositoryIdentity_Put_RejectsControlCharsInName(t *testing.T) {
	r, uid, repoID := setupRepositoryIdentityRouter(t)
	// JSON-escaped \n in the name; backend strips trim then rejects on
	// embedded control char.
	w := reqWithCaller(t, r, http.MethodPut,
		"/api/me/repository-identities/"+itoa(repoID),
		`{"name":"Alice\nEvil","email":"alice@x.com"}`, uid)
	require.Equal(t, http.StatusBadRequest, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_INVALID_NAME", errObj["code"],
		"name-control-char must be ERR_INVALID_NAME, not the confusing ERR_INVALID_EMAIL")
}

func TestRepositoryIdentity_Delete_Idempotent(t *testing.T) {
	r, uid, repoID := setupRepositoryIdentityRouter(t)
	// Delete with no override → 204.
	w := reqWithCaller(t, r, http.MethodDelete,
		"/api/me/repository-identities/"+itoa(repoID), "", uid)
	assert.Equal(t, http.StatusNoContent, w.Code)

	// Create + delete → 204.
	_ = reqWithCaller(t, r, http.MethodPut,
		"/api/me/repository-identities/"+itoa(repoID),
		`{"name":"X","email":"x@y.z"}`, uid)
	w = reqWithCaller(t, r, http.MethodDelete,
		"/api/me/repository-identities/"+itoa(repoID), "", uid)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestRepositoryIdentity_List_ReturnsAllOverrides(t *testing.T) {
	r, uid, repoID := setupRepositoryIdentityRouter(t)
	_ = reqWithCaller(t, r, http.MethodPut,
		"/api/me/repository-identities/"+itoa(repoID),
		`{"name":"X","email":"x@y.z"}`, uid)

	w := reqWithCaller(t, r, http.MethodGet,
		"/api/me/repository-identities", "", uid)
	require.Equal(t, http.StatusOK, w.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	items := got["items"].([]any)
	assert.Len(t, items, 1)
}

func TestRepositoryIdentity_Get_BadRepoIDReturns400(t *testing.T) {
	r, uid, _ := setupRepositoryIdentityRouter(t)
	w := reqWithCaller(t, r, http.MethodGet,
		"/api/me/repository-identities/not-a-number", "", uid)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
