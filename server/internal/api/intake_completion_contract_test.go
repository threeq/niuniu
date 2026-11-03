// intake_completion_contract_test.go — contract + authorization tests for the
// §6.9 Intake / Completion HTTP and MCP tunnel routes. The real MCPTokenAuth is
// exercised elsewhere; here a stub middleware injects auth_user_id (X-Test-Uid)
// so the tests focus on route registration, authz and response shapes.
package api_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupIntakeCompletion wires the intake/completion/brief handlers onto /api and
// /mcp groups with a stub identity middleware. Seed:
//   - user 1 owns project 1 (column 1, issue 1); user 2 owns project 2 (issue 2)
//   - one pending intake event + accept decision in project 1
//   - one accept decision in project 2 (cross-owner target)
func setupIntakeCompletion(t *testing.T) (*gin.Engine, *sql.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openMCPPermTestDB(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)

	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','member')`)
	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (2,'u2','x','member')`)
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1,'p1','user',1)`)
	mustExec(t, db, `INSERT INTO columns (id, project_id, name, position) VALUES (1,1,'todo',0)`)
	mustExec(t, db, `INSERT INTO issues (id, column_id, title, position) VALUES (1,1,'intake task',0)`)
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (2,'p2','user',2)`)
	mustExec(t, db, `INSERT INTO columns (id, project_id, name, position) VALUES (2,2,'todo',0)`)
	mustExec(t, db, `INSERT INTO issues (id, column_id, title, position) VALUES (2,2,'other tenant',0)`)

	// Pending dry-run decision in project 1 and one in project 2.
	mustExec(t, db, `INSERT INTO intake_events (id, project_id, source_kind, source_ref, event_ext_id, event_kind,  actor_ext_id, actor_name, thread_ext_id, payload_json)  VALUES (1,1,'im_bot','chat-1','evt-1','message','ou_1','Alice','thread-1','{"text":"ship it"}')`)
	mustExec(t, db, `INSERT INTO intake_events (id, project_id, source_kind, source_ref, event_ext_id, event_kind)  VALUES (2,2,'im_bot','chat-2','evt-2','message')`)
	mustExec(t, db, `INSERT INTO intake_decisions (id, event_id, project_id, mode_at_decision, strength, decision, executed, would_do_json)  VALUES (10,1,1,'observe','explicit','accept',0,'{"title":"ship it"}')`)
	mustExec(t, db, `INSERT INTO intake_decisions (id, event_id, project_id, mode_at_decision, strength, decision, executed)  VALUES (20,2,2,'observe','explicit','accept',0)`)

	authz := service.NewAuthz(q, db)
	intakeSvc := service.NewIntakeService(db, q)
	completionSvc := service.NewCompletionService(db, q)
	briefSvc := service.NewTaskBriefService(q)
	kanbanSvc := service.NewKanbanService(db, q, service.NewIssueActivityService(q), nil, nil)

	intakeHandler := api.NewIntakeHandler(intakeSvc, authz)
	completionHandler := api.NewCompletionHandler(completionSvc, authz)
	briefHandler := api.NewTaskBriefHandler(briefSvc, authz)
	kanbanHandler := api.NewKanbanHandler(kanbanSvc, service.NewIssueChecklistService(q))
	kanbanHandler.Authz = authz
	kanbanHandler.Completion = completionSvc

	r := gin.New()
	withIdentity := func(c *gin.Context) {
		if uid := c.GetHeader("X-Test-Uid"); uid != "" {
			if n, err := strconv.ParseInt(uid, 10, 64); err == nil {
				c.Set("auth_user_id", n)
			}
		}
		c.Next()
	}
	g := r.Group("/api")
	g.Use(withIdentity)
	g.GET("/projects/:id/intake/settings", intakeHandler.GetSettings)
	g.PUT("/projects/:id/intake/settings", intakeHandler.PutSettings)
	g.GET("/projects/:id/intake/decisions", intakeHandler.ListDecisions)
	g.POST("/projects/:id/intake/decisions/:did/accept", intakeHandler.AcceptDecision)
	g.POST("/projects/:id/intake/decisions/:did/ignore", intakeHandler.IgnoreDecision)
	g.GET("/projects/:id/completion/settings", completionHandler.GetSettings)
	g.PUT("/projects/:id/completion/settings", completionHandler.PutSettings)
	g.GET("/projects/:id/completion/profiles", completionHandler.ListProfiles)
	g.POST("/projects/:id/completion/profiles", completionHandler.CreateProfile)
	g.PATCH("/projects/:id/completion/profiles/:pid", completionHandler.UpdateProfile)
	g.DELETE("/projects/:id/completion/profiles/:pid", completionHandler.DeleteProfile)
	g.GET("/issues/:id/brief", briefHandler.GetBrief)
	g.PATCH("/issues/:id", kanbanHandler.UpdateIssue)

	m := r.Group("/mcp")
	m.Use(withIdentity)
	m.GET("/projects/:id/intake/settings", intakeHandler.MCPGetSettings)
	m.POST("/projects/:id/intake/mode", intakeHandler.MCPSetMode)
	m.GET("/projects/:id/intake/decisions/pending", intakeHandler.MCPListPending)
	m.POST("/decisions/:did/accept", intakeHandler.MCPAcceptDecision)
	m.POST("/decisions/:did/ignore", intakeHandler.MCPIgnoreDecision)
	m.PUT("/projects/:id/completion/settings", completionHandler.MCPSetPolicy)
	m.GET("/projects/:id/completion/profiles", completionHandler.MCPListProfiles)
	m.POST("/projects/:id/completion/profiles", completionHandler.MCPUpsertProfile)
	m.PUT("/issues/:id/completion-profile", completionHandler.MCPSetIssueProfile)
	m.GET("/issues/:id/brief", briefHandler.GetBrief)

	return r, db
}

func doJSON(t *testing.T, r *gin.Engine, method, path, uid, body string) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body == "" {
		buf = bytes.NewBuffer(nil)
	} else {
		buf = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	if uid != "" {
		req.Header.Set("X-Test-Uid", uid)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), "body: %s", w.Body.String())
	return out
}

func TestIntakeSettings_DefaultsThenWrite(t *testing.T) {
	r, _ := setupIntakeCompletion(t)

	w := doJSON(t, r, http.MethodGet, "/api/projects/1/intake/settings", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	view := decodeBody[service.IntakeSettingsView](t, w)
	assert.Equal(t, int64(1), view.ProjectID)
	require.Len(t, view.Settings, 5)
	for _, s := range view.Settings {
		assert.Equal(t, "observe", s.Mode)
		assert.Equal(t, int64(4), s.DailyCap)
		assert.NotNil(t, s.AllowList)
	}

	w = doJSON(t, r, http.MethodPut, "/api/projects/1/intake/settings", "1",
		`{"settings":[{"source_kind":"im_bot","mode":"auto","daily_cap":9,"allow_list":["ou_1"],"bot_list":[]}]}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	view = decodeBody[service.IntakeSettingsView](t, w)
	var found bool
	for _, s := range view.Settings {
		if s.SourceKind == "im_bot" {
			found = true
			assert.Equal(t, "auto", s.Mode)
			assert.Equal(t, int64(9), s.DailyCap)
			assert.Equal(t, []string{"ou_1"}, s.AllowList)
			assert.NotNil(t, s.UpdatedAt)
		}
	}
	assert.True(t, found)

	// Unknown mode is rejected before any write.
	w = doJSON(t, r, http.MethodPut, "/api/projects/1/intake/settings", "1",
		`{"settings":[{"source_kind":"im_bot","mode":"yolo"}]}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestIntakeSettings_OwnerScope(t *testing.T) {
	r, _ := setupIntakeCompletion(t)

	w := doJSON(t, r, http.MethodGet, "/api/projects/1/intake/settings", "2", "")
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	w = doJSON(t, r, http.MethodGet, "/api/projects/1/intake/settings", "", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// MCP tunnel enforces the same project scope.
	w = doJSON(t, r, http.MethodGet, "/mcp/projects/1/intake/settings", "2", "")
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestIntakeDecisions_QueueIgnoreAndAccept(t *testing.T) {
	r, _ := setupIntakeCompletion(t)

	w := doJSON(t, r, http.MethodGet, "/api/projects/1/intake/decisions?executed=0&decision=accept", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	items := decodeBody[[]service.IntakeDecisionDTO](t, w)
	require.Len(t, items, 1)
	assert.Equal(t, int64(10), items[0].ID)
	assert.Equal(t, "im_bot", items[0].SourceKind)
	assert.Equal(t, "Alice", items[0].ActorName)
	assert.Equal(t, "thread-1", items[0].ThreadExtID)
	assert.False(t, items[0].Executed)
	assert.JSONEq(t, `{"title":"ship it"}`, string(items[0].WouldDo))

	// A decision from another project is not addressable through project 1.
	w = doJSON(t, r, http.MethodPost, "/api/projects/1/intake/decisions/20/ignore", "1", `{"reason":"duplicate"}`)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())

	// Ignore requires a reason.
	w = doJSON(t, r, http.MethodPost, "/api/projects/1/intake/decisions/10/ignore", "1", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = doJSON(t, r, http.MethodPost, "/api/projects/1/intake/decisions/10/ignore", "1", `{"reason":"duplicate"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	ignored := decodeBody[service.IntakeDecisionDTO](t, w)
	assert.Equal(t, "ignore", ignored.Decision)
	assert.Equal(t, "duplicate", ignored.ReasonText)

	// Accept is wired but the pipeline is not: a clear 501, not a silent success.
	w = doJSON(t, r, http.MethodPost, "/mcp/decisions/20/accept", "2", "")
	assert.Equal(t, http.StatusNotImplemented, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "INTAKE_ACCEPTOR_UNAVAILABLE")

	// Cross-owner MCP decision access is forbidden.
	w = doJSON(t, r, http.MethodPost, "/mcp/decisions/20/accept", "1", "")
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

func TestCompletionSettings_DefaultsThenPartialUpdate(t *testing.T) {
	r, _ := setupIntakeCompletion(t)

	w := doJSON(t, r, http.MethodGet, "/api/projects/1/completion/settings", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	view := decodeBody[service.CompletionSettingsView](t, w)
	assert.True(t, view.AutohostEnabled)
	assert.Equal(t, int64(20), view.MaxRounds)
	assert.Equal(t, int64(0), view.TokenBudget)
	assert.True(t, view.StopOnDecision)
	assert.False(t, view.RequireReview)

	w = doJSON(t, r, http.MethodPut, "/api/projects/1/completion/settings", "1",
		`{"max_rounds":5,"require_review":true}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	view = decodeBody[service.CompletionSettingsView](t, w)
	assert.Equal(t, int64(5), view.MaxRounds)
	assert.True(t, view.RequireReview)
	assert.True(t, view.AutohostEnabled, "omitted field keeps its value")

	w = doJSON(t, r, http.MethodPut, "/mcp/projects/1/completion/settings", "1", `{"max_rounds":-1}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCompletionProfiles_CRUDAndReferenceGuard(t *testing.T) {
	r, _ := setupIntakeCompletion(t)

	w := doJSON(t, r, http.MethodPost, "/api/projects/1/completion/profiles", "1",
		`{"name":"快速","max_rounds":3,"require_review":false}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	created := decodeBody[service.CompletionProfileDTO](t, w)
	require.NotZero(t, created.ID)
	assert.Equal(t, "快速", created.Name)
	assert.Equal(t, int64(3), created.MaxRounds)

	// Duplicate name is rejected.
	w = doJSON(t, r, http.MethodPost, "/api/projects/1/completion/profiles", "1", `{"name":"快速"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	w = doJSON(t, r, http.MethodGet, "/api/projects/1/completion/profiles", "1", "")
	require.Equal(t, http.StatusOK, w.Code)
	profiles := decodeBody[[]service.CompletionProfileDTO](t, w)
	require.Len(t, profiles, 1)

	w = doJSON(t, r, http.MethodPatch, "/api/projects/1/completion/profiles/"+strconv.FormatInt(created.ID, 10), "1",
		`{"token_budget":1000}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	updated := decodeBody[service.CompletionProfileDTO](t, w)
	assert.Equal(t, int64(1000), updated.TokenBudget)
	assert.Equal(t, int64(3), updated.MaxRounds, "omitted field keeps its value")

	// Assign to issue 1 through the MCP tool tunnel, then deletion must 409.
	w = doJSON(t, r, http.MethodPut, "/mcp/issues/1/completion-profile", "1",
		`{"profile_id":`+strconv.FormatInt(created.ID, 10)+`}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	w = doJSON(t, r, http.MethodDelete, "/api/projects/1/completion/profiles/"+strconv.FormatInt(created.ID, 10), "1", "")
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())

	// Clearing the override releases the guard; then deletion succeeds.
	w = doJSON(t, r, http.MethodPut, "/mcp/issues/1/completion-profile", "1", `{"profile_id":null}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"completion_profile_id":null`)

	w = doJSON(t, r, http.MethodDelete, "/api/projects/1/completion/profiles/"+strconv.FormatInt(created.ID, 10), "1", "")
	assert.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
}

func TestIssueBrief_NullThenVersioned(t *testing.T) {
	r, db := setupIntakeCompletion(t)

	w := doJSON(t, r, http.MethodGet, "/api/issues/1/brief", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	view := decodeBody[service.IssueBriefView](t, w)
	assert.Nil(t, view.Brief, "non-Intake issue has no brief")
	assert.Empty(t, view.Versions)

	mustExec(t, db, `INSERT INTO task_briefs (issue_id, version, goal, acceptance_json, scope_in_json, delivery_json, provenance_json, policy_version)  VALUES (1,1,'ship it','["tests pass","docs updated"]','["server"]','{"source_kind":"im_bot","thread_ext_id":"thread-1","reply_mode":"thread"}',  '{"decision_id":10,"policy_version":1,"strength":"explicit","actor_ext_id":"ou_1","rule_ids":[3]}',1)`)

	w = doJSON(t, r, http.MethodGet, "/api/issues/1/brief", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	view = decodeBody[service.IssueBriefView](t, w)
	require.NotNil(t, view.Brief)
	assert.Equal(t, "ship it", view.Brief.Goal)
	assert.Equal(t, []string{"tests pass", "docs updated"}, view.Brief.Acceptance)
	assert.Equal(t, "im_bot", view.Brief.Delivery.SourceKind)
	assert.Equal(t, "thread-1", view.Brief.Delivery.ThreadExtID)
	assert.Equal(t, []int64{3}, view.Brief.Provenance.RuleIDs)
	require.Len(t, view.Versions, 1)

	// Cross-owner reads and unknown issues.
	w = doJSON(t, r, http.MethodGet, "/api/issues/1/brief", "2", "")
	assert.Equal(t, http.StatusForbidden, w.Code)

	w = doJSON(t, r, http.MethodGet, "/api/issues/999/brief", "1", "")
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

func TestPatchIssue_CompletionProfileField(t *testing.T) {
	r, _ := setupIntakeCompletion(t)

	w := doJSON(t, r, http.MethodPost, "/api/projects/1/completion/profiles", "1", `{"name":"深度"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	profile := decodeBody[service.CompletionProfileDTO](t, w)

	// Absent field leaves the override unset.
	w = doJSON(t, r, http.MethodPatch, "/api/issues/1", "1", `{"title":"intake task","priority":1}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"completion_profile_id":null`)

	w = doJSON(t, r, http.MethodPatch, "/api/issues/1", "1",
		`{"title":"intake task","priority":1,"completion_profile_id":`+strconv.FormatInt(profile.ID, 10)+`}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"completion_profile_id":`+strconv.FormatInt(profile.ID, 10))

	// null clears it again.
	w = doJSON(t, r, http.MethodPatch, "/api/issues/1", "1",
		`{"title":"intake task","priority":1,"completion_profile_id":null}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"completion_profile_id":null`)

	// A profile from another project is rejected.
	w = doJSON(t, r, http.MethodPost, "/api/projects/2/completion/profiles", "2", `{"name":"other"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	other := decodeBody[service.CompletionProfileDTO](t, w)
	w = doJSON(t, r, http.MethodPatch, "/api/issues/1", "1",
		`{"title":"intake task","priority":1,"completion_profile_id":`+strconv.FormatInt(other.ID, 10)+`}`)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}
