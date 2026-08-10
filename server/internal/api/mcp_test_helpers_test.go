package api_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
)

// itoa formats an int64 as a decimal string. Shared across api_test handler
// tests for building request paths.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// mcpPOST / mcpGET are shared HTTP helpers for MCP endpoint tests. They were
// originally defined in mcp_phase_test.go (removed when template-run was
// decommissioned); kept here for the surviving MCP tests.
func mcpPOST(t *testing.T, r *gin.Engine, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "127.0.0.1:9999"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func mcpGET(t *testing.T, r *gin.Engine, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "127.0.0.1:9999"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
