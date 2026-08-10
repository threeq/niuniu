// Handler tests for DataSourceHandler — exercise create/list happy paths, the
// M1 unsupported-kind 422, owner-scope 404, verify-failure 502, and the
// password-redaction contract on list. The router installs a middleware that
// stamps auth_user_id (what auth.IdentityResolver does in production).
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
	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupDataSourceRouter(t *testing.T) (*gin.Engine, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, q := niutest.SetupDBRaw(t)
	u, err := q.CreateUser(context.Background(), store.CreateUserParams{
		Username: "alice", PasswordHash: "x", DisplayName: "Alice", Role: "member",
	})
	require.NoError(t, err)

	kr, err := crypto.LoadOrCreate(t.TempDir() + "/integration_secret")
	require.NoError(t, err)
	svc := service.NewDataSourceService(q, db, kr, dataconn.NewRegistry())
	h := api.NewDataSourceHandler(svc, dataconn.NewPool())

	r := gin.New()
	// Stamp auth_user_id from X-Test-Caller-ID, mirroring IdentityResolver.
	r.Use(func(c *gin.Context) {
		if v := c.GetHeader("X-Test-Caller-ID"); v != "" {
			c.Set("auth_user_id", atoi64(v))
		}
		c.Next()
	})
	r.GET("/api/me/data-sources", h.List)
	r.POST("/api/me/data-sources", h.Create)
	r.PATCH("/api/me/data-sources/:id", h.Update)
	r.DELETE("/api/me/data-sources/:id", h.Delete)
	r.POST("/api/me/data-sources/:id/verify", h.Verify)
	return r, u.ID
}

func dsReq(t *testing.T, r *gin.Engine, method, path, body string, callerID int64) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if callerID > 0 {
		req.Header.Set("X-Test-Caller-ID", itoa(callerID))
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func atoi64(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func TestDataSourceHandler_CreateAndList(t *testing.T) {
	r, uid := setupDataSourceRouter(t)

	w := dsReq(t, r, http.MethodPost, "/api/me/data-sources",
		`{"name":"analytics","kind":"mysql","config":{"host":"db","port":3306,"password":"secret","database":"a"},"default_access_mode":"read","require_confirm":"writes_only"}`, uid)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	w = dsReq(t, r, http.MethodGet, "/api/me/data-sources", "", uid)
	require.Equal(t, http.StatusOK, w.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	items := got["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	assert.Equal(t, "analytics", item["name"])
	cfg := item["config"].(map[string]any)
	_, hasPw := cfg["password"]
	assert.False(t, hasPw, "list must not leak password")
	assert.Equal(t, "db", cfg["host"])
}

func TestDataSourceHandler_CreateUnsupportedKind(t *testing.T) {
	// "redis" stopped being a valid sample here when #354 opened up the
	// NoSQL kinds; "oracle" stays out of the registry.
	r, uid := setupDataSourceRouter(t)
	w := dsReq(t, r, http.MethodPost, "/api/me/data-sources",
		`{"name":"legacy","kind":"oracle","config":{"host":"o"}}`, uid)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
}

func TestDataSourceHandler_OtherUserGets404(t *testing.T) {
	r, uid := setupDataSourceRouter(t)
	w := dsReq(t, r, http.MethodPost, "/api/me/data-sources",
		`{"name":"src","kind":"postgres","config":{"host":"h"}}`, uid)
	require.Equal(t, http.StatusCreated, w.Code)
	var created map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := itoa(int64(created["id"].(float64)))

	// A different caller (uid+1) must get 404 on update/delete/verify.
	other := uid + 1
	w = dsReq(t, r, http.MethodPatch, "/api/me/data-sources/"+id,
		`{"name":"x","default_access_mode":"read","require_confirm":"writes_only"}`, other)
	assert.Equal(t, http.StatusNotFound, w.Code)
	w = dsReq(t, r, http.MethodDelete, "/api/me/data-sources/"+id, "", other)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDataSourceHandler_VerifyFailsConnection(t *testing.T) {
	r, uid := setupDataSourceRouter(t)
	// Point at an unreachable host so Ping fails -> 502 verify_failed.
	w := dsReq(t, r, http.MethodPost, "/api/me/data-sources",
		`{"name":"bad","kind":"postgres","config":{"host":"127.0.0.1","port":1,"user":"u","password":"p","database":"d"}}`, uid)
	require.Equal(t, http.StatusCreated, w.Code)
	var created map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := itoa(int64(created["id"].(float64)))

	w = dsReq(t, r, http.MethodPost, "/api/me/data-sources/"+id+"/verify", "", uid)
	assert.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
}

func TestDataSourceHandler_Unauthenticated(t *testing.T) {
	r, _ := setupDataSourceRouter(t)
	w := dsReq(t, r, http.MethodGet, "/api/me/data-sources", "", 0)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
