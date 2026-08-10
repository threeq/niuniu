package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// These tests cover the error paths of WorkspaceMCPHandler that don't require
// a fully-wired WorkspaceService / DB. Happy-path coverage is provided by the
// e2e smoke (relay/deploy/smoke/mcp-config.sh) and the manual local server
// run documented in the work summary.
//
// Addresses code review I7: at least error-class branches get isolated unit
// coverage so a regression in input validation / authz mapping / param
// parsing fails fast in CI rather than waiting for the smoke.

// handlerWithAccountAuthz constructs a WorkspaceMCPHandler where the
// `accountAuthz` interface is fakeable. Other service deps stay nil; the
// caller must avoid invoking branches that touch them. The handler is
// designed so that account-scoped endpoints (ListAvailable, Detect) hit
// the fake authz before reaching wsSvc / acctSvc, so the nil deps don't
// matter for those code paths.
func handlerWithAccountAuthz(authz accountAuthorizer) *WorkspaceMCPHandler {
	return &WorkspaceMCPHandler{
		accountAuthz: authz,
	}
}

func TestWorkspaceMCPHandler_ListAvailable_BadID_400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handlerWithAccountAuthz(&fakeAccountAuthz{acc: &service.ResolvedAccount{ID: 5}})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/claude-accounts/abc/mcp/available", nil)
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "member")
	c.Params = gin.Params{{Key: "id", Value: "abc"}}

	h.ListAvailable(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "bad account id") {
		t.Fatalf("error envelope missing 'bad account id': %s", w.Body.String())
	}
}

func TestWorkspaceMCPHandler_ListAvailable_NotVisible_404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handlerWithAccountAuthz(&fakeAccountAuthz{err: service.ErrNotVisible})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/claude-accounts/5/mcp/available", nil)
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "member")
	c.Params = gin.Params{{Key: "id", Value: "5"}}

	h.ListAvailable(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestWorkspaceMCPHandler_ListAvailable_AccountNotFound_404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handlerWithAccountAuthz(&fakeAccountAuthz{err: service.ErrAccountNotFound})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/claude-accounts/5/mcp/available", nil)
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "member")
	c.Params = gin.Params{{Key: "id", Value: "5"}}

	h.ListAvailable(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestWorkspaceMCPHandler_Detect_BadJSONBody_400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handlerWithAccountAuthz(&fakeAccountAuthz{acc: &service.ResolvedAccount{ID: 5}})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/workspaces/mcp/detect",
		bytes.NewReader([]byte(`{this is not valid json`)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "member")

	h.Detect(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestWorkspaceMCPHandler_Detect_AccountNotVisible_404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := handlerWithAccountAuthz(&fakeAccountAuthz{err: service.ErrNotVisible})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/workspaces/mcp/detect",
		bytes.NewReader([]byte(`{"claude_account_id": 5, "repo_ids": []}`)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "member")

	h.Detect(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestWorkspaceMCPHandler_Get_BadID_400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &WorkspaceMCPHandler{} // no deps needed — param parsing fails first
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/workspaces/abc/mcp", nil)
	c.Params = gin.Params{{Key: "id", Value: "abc"}}
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "member")

	h.Get(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestWorkspaceMCPHandler_Put_BadID_400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &WorkspaceMCPHandler{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/workspaces/0/mcp",
		bytes.NewReader([]byte(`{"servers":[]}`)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "0"}}
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "member")

	h.Put(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (id=0 should fail validation); body=%s", w.Code, w.Body.String())
	}
}

func TestWorkspaceMCPHandler_Redetect_BadID_400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &WorkspaceMCPHandler{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/workspaces/-1/mcp/redetect", nil)
	c.Params = gin.Params{{Key: "id", Value: "-1"}}
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "member")

	h.Redetect(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// TestDecodeMCPServers_RoundTrip covers the JSON parsing helper across the
// shapes that flow through Get: empty / null / valid array / malformed.
func TestDecodeMCPServers_RoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{"empty", "", []string{}, false},
		{"null", "null", []string{}, false},
		{"empty array", "[]", []string{}, false},
		{"valid array", `["a","b"]`, []string{"a", "b"}, false},
		{"malformed", `{this is not json`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeMCPServers(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr {
				if len(got) != len(tc.want) {
					t.Fatalf("len = %d, want %d", len(got), len(tc.want))
				}
				for i := range got {
					if got[i] != tc.want[i] {
						t.Fatalf("got[%d] = %q, want %q", i, got[i], tc.want[i])
					}
				}
			}
		})
	}
}

// Compile-time guard: ensure the fakeAccountAuthz already used by
// claude_account_test.go also satisfies our handler's accountAuthorizer
// dependency. Same interface — this test is just an assertion that we did
// not accidentally introduce a parallel interface.
var _ accountAuthorizer = (*fakeAccountAuthz)(nil)

// silence unused-import vet check on context (used by fake interfaces in
// claude_account_test.go which we share via the same package).
var _ = context.Background