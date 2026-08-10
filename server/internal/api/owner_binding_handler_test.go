package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests guard against the bug class where a Create handler forgets to
// bind the request body's `owner` field, causing every resource to land in the
// caller's personal space regardless of the SPA's selection. The fix lives in
// quick_action.go / env_preset.go, mirroring the pattern in project.go.

func TestQuickActionHandler_Create_HonorsExplicitOrgOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)

	body := `{"label":"L","content":"C","auto_send":false,"owner":{"type":"org","id":7}}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/quick-actions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.Engine().ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp api.QuickActionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "org", resp.Owner.Type)
	assert.Equal(t, int64(7), resp.Owner.ID)

	var ownerType string
	var ownerID int64
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT owner_type, owner_id FROM quick_actions WHERE id = ?`, resp.ID).
		Scan(&ownerType, &ownerID))
	assert.Equal(t, "org", ownerType)
	assert.Equal(t, int64(7), ownerID)
}

func TestQuickActionHandler_Create_DefaultsToCallerWhenOwnerOmitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, _ := testutil.SetupTestServer(t)

	body := `{"label":"L","content":"C","auto_send":false}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/quick-actions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.Engine().ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp api.QuickActionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// SetupTestServer doesn't seed auth_user_id; the fallback branch produces
	// {user, 0}, which is exactly the personal-edition contract the handler must honor.
	assert.Equal(t, "user", resp.Owner.Type)
}

func TestQuickActionHandler_Create_RejectsInvalidOwnerType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, _ := testutil.SetupTestServer(t)

	body := `{"label":"L","content":"C","auto_send":false,"owner":{"type":"alien","id":1}}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/quick-actions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.Engine().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestEnvPresetHandler_Create_HonorsExplicitOrgOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)

	body := `{"name":"P","description":"","env":{},"owner":{"type":"org","id":7}}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/env-presets", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.Engine().ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var resp api.EnvPresetResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "org", resp.Owner.Type)
	assert.Equal(t, int64(7), resp.Owner.ID)

	var ownerType string
	var ownerID int64
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT owner_type, owner_id FROM env_presets WHERE id = ?`, resp.ID).
		Scan(&ownerType, &ownerID))
	assert.Equal(t, "org", ownerType)
	assert.Equal(t, int64(7), ownerID)
}

func TestEnvPresetHandler_Create_RejectsInvalidOwnerType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, _ := testutil.SetupTestServer(t)

	body := `{"name":"P","description":"","env":{},"owner":{"type":"alien","id":1}}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/env-presets", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.Engine().ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}
