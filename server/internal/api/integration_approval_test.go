package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type approvalEnv struct {
	r      *gin.Engine
	orgID  int64
	owner  int64
	admin  int64
	member int64
}

// approvalTestRouter wires the approval routes with a caller resolved
// per-request from headers, so one test can act as several users.
func approvalTestRouter(t *testing.T) *approvalEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := setupOrgHandlerDB(t)
	q := store.New(db)
	authz := service.NewAuthz(q, db)

	orgSvc := service.NewOrgService(q, db, authz)
	approvalSvc := service.NewIntegrationApprovalService(q, authz, nil)
	projSvc := service.NewProjectMemberService(q, authz)
	h := NewIntegrationApprovalHandler(approvalSvc, projSvc, authz)

	ownerID := insertOrgHandlerUser(t, db, "ap-owner", "member")
	adminID := insertOrgHandlerUser(t, db, "ap-admin", "member")
	memberID := insertOrgHandlerUser(t, db, "ap-member", "member")
	org, err := orgSvc.Create(t.Context(), ownerID, "ap-owner", "Acme", "acme-api", "")
	require.NoError(t, err)
	require.NoError(t, orgSvc.AddMember(t.Context(), ownerID, "ap-owner", org.ID, adminID, "admin"))
	require.NoError(t, orgSvc.AddMember(t.Context(), ownerID, "ap-owner", org.ID, memberID, "member"))

	r := gin.New()
	r.Use(func(c *gin.Context) {
		uid, _ := strconv.ParseInt(c.GetHeader("X-Test-User"), 10, 64)
		c.Set("auth_user_id", uid)
		c.Set("auth_username", c.GetHeader("X-Test-Name"))
		c.Set("auth_role", "member")
		c.Next()
	})
	orgs := r.Group("/api/orgs")
	{
		orgs.POST("/:id/integration-approvals", h.Submit)
		orgs.GET("/:id/integration-approvals", h.ListByOrg)
		orgs.GET("/:id/integration-approvals/:req_id", h.Get)
		orgs.POST("/:id/integration-approvals/:req_id/decision", h.Decide)
		orgs.DELETE("/:id/integration-approvals/:req_id", h.Cancel)
	}
	_ = approvalSvc
	return &approvalEnv{r: r, orgID: org.ID, owner: ownerID, admin: adminID, member: memberID}
}

func doApprovalReq(t *testing.T, r *gin.Engine, method, path string, caller int64, name string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req, _ := http.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", strconv.FormatInt(caller, 10))
	req.Header.Set("X-Test-Name", name)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func submitApproval(t *testing.T, r *gin.Engine, orgID, caller int64, name string) int64 {
	t.Helper()
	w := doApprovalReq(t, r, "POST", fmt.Sprintf("/api/orgs/%d/integration-approvals", orgID), caller, name, map[string]any{
		"action":       "scope_relax",
		"subject_type": "data_source",
		"subject_id":   1,
		"diff":         `{"scope_config":{"tables_allow":["orders"]}}`,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var out struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return out.ID
}

// ACCEPTANCE: a member without the required role is rejected at the API, not
// merely hidden in the UI.
func TestApprovalHandlerMemberCannotApprove(t *testing.T) {
	e := approvalTestRouter(t)
	// The owner raises the request, so a plain member attempting to decide it
	// fails on the role check (not on the self-decide guard).
	reqID := submitApproval(t, e.r, e.orgID, e.owner, "ap-owner")

	w := doApprovalReq(t, e.r, "POST",
		fmt.Sprintf("/api/orgs/%d/integration-approvals/%d/decision", e.orgID, reqID),
		e.member, "ap-member", map[string]any{"approve": true})
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "NOT_PERMITTED")
}

func TestApprovalHandlerAdminApproves(t *testing.T) {
	e := approvalTestRouter(t)
	reqID := submitApproval(t, e.r, e.orgID, e.member, "ap-member")

	w := doApprovalReq(t, e.r, "POST",
		fmt.Sprintf("/api/orgs/%d/integration-approvals/%d/decision", e.orgID, reqID),
		e.admin, "ap-admin", map[string]any{"approve": true, "note": "ok"})
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"status":"approved"`)
}

func TestApprovalHandlerRequesterCannotSelfDecide(t *testing.T) {
	e := approvalTestRouter(t)
	// An owner who raises a publish request still cannot approve it themselves.
	w := doApprovalReq(t, e.r, "POST", fmt.Sprintf("/api/orgs/%d/integration-approvals", e.orgID), e.owner, "ap-owner", map[string]any{
		"action": "publish",
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var out struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))

	w2 := doApprovalReq(t, e.r, "POST",
		fmt.Sprintf("/api/orgs/%d/integration-approvals/%d/decision", e.orgID, out.ID),
		e.owner, "ap-owner", map[string]any{"approve": true})
	assert.Equal(t, http.StatusForbidden, w2.Code, w2.Body.String())
	assert.Contains(t, w2.Body.String(), "SELF_DECIDE")
}

func TestApprovalHandlerListForbiddenForMember(t *testing.T) {
	e := approvalTestRouter(t)
	w := doApprovalReq(t, e.r, "GET", fmt.Sprintf("/api/orgs/%d/integration-approvals", e.orgID), e.member, "ap-member", nil)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestApprovalHandlerListForAdmin(t *testing.T) {
	e := approvalTestRouter(t)
	submitApproval(t, e.r, e.orgID, e.member, "ap-member")
	w := doApprovalReq(t, e.r, "GET", fmt.Sprintf("/api/orgs/%d/integration-approvals?status=pending", e.orgID), e.admin, "ap-admin", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"action":"scope_relax"`)
}

func TestApprovalHandlerRequesterCanCancel(t *testing.T) {
	e := approvalTestRouter(t)
	reqID := submitApproval(t, e.r, e.orgID, e.member, "ap-member")

	w := doApprovalReq(t, e.r, "DELETE",
		fmt.Sprintf("/api/orgs/%d/integration-approvals/%d", e.orgID, reqID), e.member, "ap-member", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"status":"cancelled"`)
}

// A decision body without an explicit `approve` must be rejected, not treated
// as a denial (denials are terminal).
func TestApprovalHandlerDecideRequiresExplicitApprove(t *testing.T) {
	e := approvalTestRouter(t)
	reqID := submitApproval(t, e.r, e.orgID, e.member, "ap-member")

	w := doApprovalReq(t, e.r, "POST",
		fmt.Sprintf("/api/orgs/%d/integration-approvals/%d/decision", e.orgID, reqID),
		e.admin, "ap-admin", map[string]any{"note": "no verdict"})
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// Still pending — nothing was decided.
	g := doApprovalReq(t, e.r, "GET",
		fmt.Sprintf("/api/orgs/%d/integration-approvals/%d", e.orgID, reqID), e.admin, "ap-admin", nil)
	require.Equal(t, http.StatusOK, g.Code, g.Body.String())
	assert.Contains(t, g.Body.String(), `"status":"pending"`)
}

func TestApprovalHandlerCancelPermissions(t *testing.T) {
	e := approvalTestRouter(t)

	// A plain member may not withdraw the owner's request…
	ownerReq := submitApproval(t, e.r, e.orgID, e.owner, "ap-owner")
	w := doApprovalReq(t, e.r, "DELETE",
		fmt.Sprintf("/api/orgs/%d/integration-approvals/%d", e.orgID, ownerReq), e.member, "ap-member", nil)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	// …but an org admin may clear any pending request.
	memberReq := submitApproval(t, e.r, e.orgID, e.member, "ap-member")
	w2 := doApprovalReq(t, e.r, "DELETE",
		fmt.Sprintf("/api/orgs/%d/integration-approvals/%d", e.orgID, memberReq), e.admin, "ap-admin", nil)
	assert.Equal(t, http.StatusOK, w2.Code, w2.Body.String())
}
