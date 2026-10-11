package server

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// newDraftAgentTestRouter wires the AGENT surface of the draft pipeline. The
// service is built over an empty in-memory database on purpose: the refusal is
// decided before any query runs, so the test proves the route itself cannot be
// used by an agent — not merely that the store rejects it.
func newDraftAgentTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	srv := &Server{integrationDraftSvc: service.NewIntegrationDraftService(store.New(store.Wrap(db)), nil, nil, nil, nil)}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("auth_user_id", int64(1)) })
	// Mirror router.go: the agent group registers publish and rollback so the
	// refusal is a real response rather than a 404.
	r.POST("/mcp/integration-drafts/:id/publish", srv.draftPublishAgent)
	r.POST("/mcp/integration-versions/:id/rollback", srv.draftRollbackAgent)
	r.POST("/api/me/integration-drafts/:id/publish", srv.draftPublish)
	return r
}

// TestDraftRoutes_AgentSurfaceRefusesAuthorizationActions pins the permission
// principle at the HTTP boundary: an agent's publish/rollback attempt is a 403
// with a machine-readable code (never a 404, and never carried out).
func TestDraftRoutes_AgentSurfaceRefusesAuthorizationActions(t *testing.T) {
	r := newDraftAgentTestRouter(t)

	for _, tc := range []struct {
		name string
		path string
	}{
		{"publish", "/mcp/integration-drafts/1/publish"},
		{"rollback", "/mcp/integration-versions/1/rollback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			r.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
			}
			if body := w.Body.String(); !strings.Contains(body, "AGENT_CANNOT_AUTHORIZE") {
				t.Fatalf("expected the AGENT_CANNOT_AUTHORIZE code, got %s", body)
			}
		})
	}
}
