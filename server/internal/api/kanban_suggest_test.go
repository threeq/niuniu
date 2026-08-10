package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/agentproxy"
)

// TestSuggestGoalCondition_InvalidIssueID covers the pre-service guard: a
// non-numeric :id must return 400 before any auth / service / CLI work.
// No svc/Authz wiring required because the 400 path returns early.
func TestSuggestGoalCondition_InvalidIssueID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &KanbanHandler{}
	r := gin.New()
	r.POST("/issues/:id/suggest-goal-condition", h.SuggestGoalCondition)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/issues/not-a-number/suggest-goal-condition", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// TestSuggestGoalCondition_CLIUnavailable verifies the 503 short-circuit
// fires before the service is consulted. Critical: when claude CLI is down
// the handler must NOT call svc.GetIssue, since some deployments might
// background-recreate the CLI and we don't want to do DB work for a request
// that's about to fail anyway.
func TestSuggestGoalCondition_CLIUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	prev := agentproxy.SetClaudeCLIAvailableForTest(false)
	defer agentproxy.SetClaudeCLIAvailableForTest(prev)

	h := &KanbanHandler{} // svc nil; if 503 short-circuit fails we'll panic
	r := gin.New()
	r.POST("/issues/:id/suggest-goal-condition", h.SuggestGoalCondition)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/issues/1/suggest-goal-condition", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CLI_UNAVAILABLE") {
		t.Fatalf("expected CLI_UNAVAILABLE in body, got %s", w.Body.String())
	}
}
