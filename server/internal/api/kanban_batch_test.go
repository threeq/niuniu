package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The batch handlers run a set of cheap, service-free guards before touching
// the DB: JSON binding (issue_ids is required) and the priority range check.
// These tests exercise only those early-return paths, so the handler can be
// constructed with a nil svc/Authz -- if a guard regresses and lets the
// request through, the nil svc would panic and fail the test loudly.

func newBatchTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := &KanbanHandler{} // svc + Authz nil on purpose; guards must return first
	r := gin.New()
	r.POST("/issues/batch/move", h.BatchMoveIssues)
	r.POST("/issues/batch/priority", h.BatchUpdatePriority)
	r.POST("/issues/batch/delete", h.BatchDeleteIssues)
	return r
}

func postJSON(t *testing.T, r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestBatchMoveIssues_BadJSONReturns400(t *testing.T) {
	r := newBatchTestRouter()
	w := postJSON(t, r, "/issues/batch/move", `{}`) // missing required issue_ids/column_id
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestBatchUpdatePriority_OutOfRangeReturns400(t *testing.T) {
	r := newBatchTestRouter()
	w := postJSON(t, r, "/issues/batch/priority", `{"issue_ids":[1],"priority":5}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "priority must be between 0 and 3") {
		t.Fatalf("expected range message, got %s", w.Body.String())
	}
}

func TestBatchUpdatePriority_MissingIssueIDsReturns400(t *testing.T) {
	r := newBatchTestRouter()
	w := postJSON(t, r, "/issues/batch/priority", `{"priority":1}`) // issue_ids required
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestBatchDeleteIssues_BadJSONReturns400(t *testing.T) {
	r := newBatchTestRouter()
	w := postJSON(t, r, "/issues/batch/delete", `{}`) // missing required issue_ids
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", w.Code, w.Body.String())
	}
}
