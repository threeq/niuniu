// Handler tests for DashboardHandler — saved-query read-only 422, dashboard
// CRUD + list-array shape, one-step pin (auto-create default dashboard +
// returned ids), and owner-scope 404. The router stamps auth_user_id from a
// test header, mirroring auth.IdentityResolver.
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

func setupDashboardRouter(t *testing.T) (*gin.Engine, int64, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, q := niutest.SetupDBRaw(t)
	alice, err := q.CreateUser(context.Background(), store.CreateUserParams{
		Username: "alice", PasswordHash: "x", DisplayName: "Alice", Role: "member",
	})
	require.NoError(t, err)
	bob, err := q.CreateUser(context.Background(), store.CreateUserParams{
		Username: "bob", PasswordHash: "x", DisplayName: "Bob", Role: "member",
	})
	require.NoError(t, err)

	kr, err := crypto.LoadOrCreate(t.TempDir() + "/integration_secret")
	require.NoError(t, err)
	reg := dataconn.NewRegistry()
	dsSvc := service.NewDataSourceService(q, db, kr, reg)
	authz := service.NewAuthz(q, db)
	proxy := service.NewDataProxyService(dsSvc, reg, nil, q)
	svc := service.NewDashboardService(q, dsSvc, reg, proxy)
	h := api.NewDashboardHandler(svc, authz)

	// Seed a data source owned by alice so saved-query / pin have a valid source.
	sourceID, err := dsSvc.Create(context.Background(), service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: alice.ID, UserID: alice.ID, Name: "analytics", Kind: "mysql",
		Config:            map[string]any{"host": "db", "port": 3306, "user": "ro", "password": "secret", "database": "a"},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	require.NoError(t, err)
	_ = sourceID

	r := gin.New()
	r.Use(func(c *gin.Context) {
		if v := c.GetHeader("X-Test-Caller-ID"); v != "" {
			c.Set("auth_user_id", atoi64(v))
		}
		c.Next()
	})
	r.GET("/api/saved-queries", h.ListSavedQueries)
	r.POST("/api/saved-queries", h.CreateSavedQuery)
	r.DELETE("/api/saved-queries/:id", h.DeleteSavedQuery)
	r.GET("/api/dashboards", h.ListDashboards)
	r.POST("/api/dashboards", h.CreateDashboard)
	r.POST("/api/dashboards/pin", h.Pin)
	r.GET("/api/dashboards/:id", h.GetDashboard)
	r.PATCH("/api/dashboards/:id", h.UpdateDashboard)
	r.DELETE("/api/dashboards/:id", h.DeleteDashboard)
	r.GET("/api/dashboards/:id/panels", h.ListPanels)
	r.POST("/api/dashboards/:id/panels", h.AddPanel)
	r.DELETE("/api/dashboards/:id/panels/:pid", h.DeletePanel)
	r.GET("/api/dashboards/:id/panels/:pid/data", h.PanelData)
	return r, alice.ID, bob.ID
}

func dashReq(t *testing.T, r *gin.Engine, method, path, body string, callerID int64) *httptest.ResponseRecorder {
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

func TestSavedQueryHandler_RejectsWrite(t *testing.T) {
	r, uid, _ := setupDashboardRouter(t)
	w := dashReq(t, r, http.MethodPost, "/api/saved-queries",
		`{"source_id":1,"name":"q","operation":{"statement":"DELETE FROM orders"}}`, uid)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
}

func TestSavedQueryHandler_AcceptsRead(t *testing.T) {
	r, uid, _ := setupDashboardRouter(t)
	w := dashReq(t, r, http.MethodPost, "/api/saved-queries",
		`{"source_id":1,"name":"q","operation":{"statement":"SELECT id FROM orders"}}`, uid)
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}

func TestDashboardHandler_PinAutoCreates(t *testing.T) {
	r, uid, _ := setupDashboardRouter(t)

	// Before pin: dashboards list empty (nav entry gated off).
	w := dashReq(t, r, http.MethodGet, "/api/dashboards", "", uid)
	require.Equal(t, http.StatusOK, w.Code)
	var listed map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listed))
	assert.Len(t, listed["items"].([]any), 0)

	// Pin (no dashboard_id) -> auto-creates default dashboard, returns ids.
	w = dashReq(t, r, http.MethodPost, "/api/dashboards/pin",
		`{"source_id":1,"name":"orders","operation":{"statement":"SELECT id FROM orders"},"chart_spec":{"type":"bar"}}`, uid)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var pin map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pin))
	assert.Greater(t, pin["dashboard_id"].(float64), float64(0))
	assert.Greater(t, pin["panel_id"].(float64), float64(0))

	// After pin: dashboards list has one entry (nav entry appears).
	w = dashReq(t, r, http.MethodGet, "/api/dashboards", "", uid)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listed))
	assert.Len(t, listed["items"].([]any), 1)
}

func TestDashboardHandler_OtherUserGets404(t *testing.T) {
	r, uid, bob := setupDashboardRouter(t)
	// alice pins -> creates a dashboard.
	w := dashReq(t, r, http.MethodPost, "/api/dashboards/pin",
		`{"source_id":1,"name":"p","operation":{"statement":"SELECT id FROM orders"}}`, uid)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var pin map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pin))
	dashID := itoa(int64(pin["dashboard_id"].(float64)))

	// bob cannot see alice's dashboard.
	w = dashReq(t, r, http.MethodGet, "/api/dashboards/"+dashID, "", bob)
	assert.Equal(t, http.StatusNotFound, w.Code)
	w = dashReq(t, r, http.MethodDelete, "/api/dashboards/"+dashID, "", bob)
	assert.Equal(t, http.StatusNotFound, w.Code)

	// bob's dashboard list is empty.
	w = dashReq(t, r, http.MethodGet, "/api/dashboards", "", bob)
	require.Equal(t, http.StatusOK, w.Code)
	var listed map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listed))
	assert.Len(t, listed["items"].([]any), 0)
}

func TestDashboardHandler_Unauthenticated(t *testing.T) {
	r, _, _ := setupDashboardRouter(t)
	w := dashReq(t, r, http.MethodGet, "/api/dashboards", "", 0)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
