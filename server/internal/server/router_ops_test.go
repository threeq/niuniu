package server

import (
	"testing"

	"github.com/gin-gonic/gin"
	apipkg "github.com/niuniu-dev/niuniu/internal/api"
)

// TestOpsRoutesRegister pins the shared /api/ops and /mcp/ops registration. gin
// panics on conflicting wildcards at registration time, so this also guards the
// route tree against a future path collision.
func TestOpsRoutesRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerOpsRoutes(r.Group("/api/ops"), &apipkg.OpsBotHandler{})
	registerOpsRoutes(r.Group("/mcp/ops"), &apipkg.OpsBotHandler{})

	want := map[string]bool{
		"GET /api/ops/bots":                          false,
		"POST /api/ops/bots":                         false,
		"GET /api/ops/bots/:id":                      false,
		"PATCH /api/ops/bots/:id":                    false,
		"POST /api/ops/bots/:id/pause":               false,
		"POST /api/ops/bots/:id/resume":              false,
		"POST /api/ops/bots/:id/disable":             false,
		"GET /api/ops/bots/:id/budget":               false,
		"GET /api/ops/bots/:id/metrics":              false,
		"GET /api/ops/bots/:id/engagements":          false,
		"GET /api/ops/bots/:id/calendar":             false,
		"GET /api/ops/packs":                         false,
		"POST /api/ops/packs/import":                 false,
		"GET /api/ops/packs/:packID/:version/export": false,
		"GET /api/ops/approvals":                     false,
		"POST /api/ops/approvals/:id/approve":        false,
		"POST /api/ops/approvals/:id/reject":         false,
		"POST /api/ops/approvals/:id/modify":         false,
		"GET /mcp/ops/bots":                          false,
		"POST /mcp/ops/approvals/:id/approve":        false,
	}
	for _, route := range r.Routes() {
		key := route.Method + " " + route.Path
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for key, seen := range want {
		if !seen {
			t.Errorf("route %q is not registered", key)
		}
	}
}

// TestOpsRoutesNilHandlerIsNoop keeps the optional wiring safe: a nil handler
// must not panic at startup.
func TestOpsRoutesNilHandlerIsNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerOpsRoutes(r.Group("/api/ops"), nil)
	if len(r.Routes()) != 0 {
		t.Fatalf("nil handler registered %d routes", len(r.Routes()))
	}
}
