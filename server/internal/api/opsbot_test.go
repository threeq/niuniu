// opsbot_test.go — contract + authorization tests for the Ops Bot HTTP and MCP
// surfaces (design 2026-11-06 §12). The same handler methods are mounted on
// /api/ops and /mcp/ops here, exactly like production, so the parity test
// exercises the real sharing rather than a stand-in.
package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/opsbot"
	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupOpsBot(t *testing.T) (*gin.Engine, *sql.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openMCPPermTestDB(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)

	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','member')`)
	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (2,'u2','x','member')`)
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1,'p1','user',1)`)
	mustExec(t, db, `INSERT INTO columns (id, project_id, name, position) VALUES (1,1,'todo',0)`)
	mustExec(t, db, `INSERT INTO workspaces (id, name, path, owner_type, owner_id) VALUES (1,'ws1','/tmp/ws1','user',1)`)
	mustExec(t, db, `INSERT INTO workspaces (id, name, path, owner_type, owner_id) VALUES (2,'ws2','/tmp/ws2','user',2)`)

	registry := &packs.Registry{DataDir: t.TempDir()}
	require.NoError(t, packs.InstallBuiltins(registry, "user", 1))

	svc := opsbot.NewService(q, registry)
	approval := opsbot.NewApproval(store.Wrap(db), opsbot.ApprovalPolicy{})
	svc.Approve = approval
	budget := newTestBudget(q, svc)

	h := &api.OpsBotHandler{
		Svc:      svc,
		Approval: approval,
		Budget:   budget,
		Packs:    registry,
		Authz:    service.NewAuthz(q, db),
		Queries:  q,
		DB:       db,
		DataDir:  t.TempDir(),
	}

	r := gin.New()
	withIdentity := func(c *gin.Context) {
		if uid := c.GetHeader("X-Test-Uid"); uid != "" {
			if n, err := strconv.ParseInt(uid, 10, 64); err == nil {
				c.Set("auth_user_id", n)
			}
		}
		c.Next()
	}
	for _, prefix := range []string{"/api/ops", "/mcp/ops"} {
		g := r.Group(prefix)
		g.Use(withIdentity)
		registerOpsTestRoutes(g, h)
	}
	return r, db
}

// registerOpsTestRoutes mirrors server.registerOpsRoutes for the endpoints the
// tests exercise.
func registerOpsTestRoutes(g *gin.RouterGroup, h *api.OpsBotHandler) {
	g.GET("/bots", h.ListBots)
	g.POST("/bots", h.CreateBot)
	g.GET("/bots/:id", h.GetBot)
	g.PATCH("/bots/:id", h.UpdateBot)
	g.POST("/bots/:id/pause", h.PauseBot)
	g.POST("/bots/:id/resume", h.ResumeBot)
	g.POST("/bots/:id/disable", h.DisableBot)
	g.GET("/bots/:id/budget", h.GetBudget)
	g.POST("/bots/:id/budget", h.SetBudget)
	g.GET("/bots/:id/metrics", h.GetMetrics)
	g.GET("/bots/:id/engagements", h.ListEngagements)
	g.GET("/bots/:id/calendar", h.GetCalendar)
	g.POST("/bots/:id/publish", h.Publish)
	g.GET("/packs", h.ListPacks)
	g.POST("/packs/import", h.ImportPack)
	g.POST("/packs/validate", h.ValidatePack)
	g.GET("/packs/:packID/:version/export", h.ExportPack)
	g.POST("/bots/:id/browser-login", h.BrowserLogin)
	g.GET("/approvals", h.ListApprovals)
	g.POST("/approvals/:id/approve", h.ApproveApproval)
	g.POST("/approvals/:id/reject", h.RejectApproval)
	g.POST("/engagements/:id/reply", h.ReplyEngagement)
}

func newTestBudget(q *store.Queries, svc *opsbot.Service) *opsbot.Budget {
	b := &opsbot.Budget{}
	b.Sum = func(ctx context.Context, epicIssueID int64, from, to time.Time) (float64, error) {
		return q.SumOpsAccountCost(ctx, store.SumOpsAccountCostParams{
			ParentIssueID: sql.NullInt64{Int64: epicIssueID, Valid: epicIssueID > 0},
			CreatedAt:     from,
			CreatedAt_2:   to,
		})
	}
	b.Limit = func(ctx context.Context, botID int64) (float64, string, error) {
		bot, err := svc.Get(ctx, botID)
		if err != nil {
			return 0, "", err
		}
		cfg := map[string]any{}
		_ = json.Unmarshal([]byte(bot.BudgetJson), &cfg)
		limit, _ := cfg["monthly_limit"].(float64)
		currency, _ := cfg["currency"].(string)
		return limit, currency, nil
	}
	b.SaveLimit = func(ctx context.Context, botID int64, limit float64, currency string) error {
		bot, err := svc.Get(ctx, botID)
		if err != nil {
			return err
		}
		cfg := map[string]any{}
		_ = json.Unmarshal([]byte(bot.BudgetJson), &cfg)
		cfg["monthly_limit"] = limit
		cfg["currency"] = currency
		raw, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		body := string(raw)
		_, err = svc.UpdateConfig(ctx, botID, opsbot.ConfigPatch{BudgetJSON: &body})
		return err
	}
	b.Pause = func(context.Context, int64) error { return nil }
	b.Notify = func(context.Context, int64, string) error { return nil }
	return b
}

func createTestBot(t *testing.T, r *gin.Engine, uid, accountRef string) int64 {
	t.Helper()
	w := doJSON(t, r, http.MethodPost, "/api/ops/bots", uid,
		`{"role":"account","pack_id":"wechat_mp","account_ref":"`+accountRef+`","display_name":"品牌 A"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var bot struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &bot))
	require.NotZero(t, bot.ID)
	return bot.ID
}

func TestOpsBot_CreateListAndDuplicateAccount(t *testing.T) {
	r, _ := setupOpsBot(t)

	first := createTestBot(t, r, "1", "acct-1")

	// A second account on the SAME pack is allowed.
	second := createTestBot(t, r, "1", "acct-2")
	assert.NotEqual(t, first, second)

	// The same (owner, pack, account_ref) is a conflict.
	w := doJSON(t, r, http.MethodPost, "/api/ops/bots", "1",
		`{"role":"account","pack_id":"wechat_mp","account_ref":"acct-1"}`)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "OPS_ACCOUNT_EXISTS")

	w = doJSON(t, r, http.MethodGet, "/api/ops/bots", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var list struct {
		Items []struct {
			ID         int64  `json:"id"`
			OwnerType  string `json:"owner_type"`
			OwnerID    int64  `json:"owner_id"`
			PackID     string `json:"pack_id"`
			AccountRef string `json:"account_ref"`
			Status     string `json:"status"`
		} `json:"items"`
		Count int `json:"count"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list.Items, 2)
	assert.Equal(t, 2, list.Count)
	for _, item := range list.Items {
		assert.Equal(t, "user", item.OwnerType)
		assert.Equal(t, int64(1), item.OwnerID)
		assert.Equal(t, "wechat_mp", item.PackID)
		assert.Equal(t, "active", item.Status)
	}

	// Another owner's list is empty: bots never leak across owners.
	w = doJSON(t, r, http.MethodGet, "/api/ops/bots", "2", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"count":0`)
}

func TestOpsBot_CrossOwnerForbidden(t *testing.T) {
	r, db := setupOpsBot(t)
	botID := createTestBot(t, r, "1", "acct-1")
	path := "/api/ops/bots/" + strconv.FormatInt(botID, 10)

	cases := []struct {
		method, path, body string
	}{
		{http.MethodGet, path, ""},
		{http.MethodPatch, path, `{"display_name":"hijack"}`},
		{http.MethodPost, path + "/pause", ""},
		{http.MethodPost, path + "/resume", ""},
		{http.MethodPost, path + "/disable", ""},
		{http.MethodGet, path + "/budget", ""},
		{http.MethodPost, path + "/budget", `{"monthly_limit":1}`},
		{http.MethodGet, path + "/metrics", ""},
		{http.MethodGet, path + "/engagements", ""},
		{http.MethodGet, path + "/calendar", ""},
		{http.MethodPost, path + "/publish", `{"issue_id":1,"idempotency_key":"k"}`},
		// The MCP tunnel enforces the same boundary.
		{http.MethodGet, "/mcp/ops/bots/" + strconv.FormatInt(botID, 10), ""},
	}
	for _, tc := range cases {
		w := doJSON(t, r, tc.method, tc.path, "2", tc.body)
		assert.Equal(t, http.StatusForbidden, w.Code, "%s %s: %s", tc.method, tc.path, w.Body.String())
	}

	// The owner gate is identity-based, not writability-based: even a global
	// admin does not operate on another user's personal bot (design §12 / Task 6
	// security review).
	mustExec(t, db, `UPDATE users SET role = 'admin' WHERE id = 2`)
	w := doJSON(t, r, http.MethodGet, path, "2", "")
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "OPS_FORBIDDEN")
	w = doJSON(t, r, http.MethodPost, path+"/pause", "2", "")
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	// Unauthenticated is 401, never an unrestricted default.
	w = doJSON(t, r, http.MethodGet, path, "", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// The owner can read and mutate it.
	w = doJSON(t, r, http.MethodGet, path, "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = doJSON(t, r, http.MethodPost, path+"/pause", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"status":"paused"`)
	w = doJSON(t, r, http.MethodPost, path+"/resume", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"status":"active"`)
}

func insertOpsApproval(t *testing.T, db *sql.DB, workspaceID, ownerID, botID int64) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO agent_ask_user_requests
		(workspace_id, owner_type, owner_id, session_id, questions_json, kind, metadata_json, status, expires_at)
		VALUES (?, 'user', ?, 'ops-approval', '{}', 'ops_approval', ?, 'pending', datetime('now','+1 day'))`,
		workspaceID, ownerID, `{"bot_id":`+strconv.FormatInt(botID, 10)+`,"type":"publish"}`)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

func TestOpsApproval_DuplicateDecisionConflict(t *testing.T) {
	r, db := setupOpsBot(t)
	botID := createTestBot(t, r, "1", "acct-1")
	approvalID := insertOpsApproval(t, db, 1, 1, botID)

	// Listed as pending for its owner only.
	w := doJSON(t, r, http.MethodGet, "/api/ops/approvals?status=pending", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var list struct {
		Items []struct {
			ID     int64  `json:"id"`
			BotID  int64  `json:"bot_id"`
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list.Items, 1)
	assert.Equal(t, approvalID, list.Items[0].ID)
	assert.Equal(t, botID, list.Items[0].BotID)
	assert.Equal(t, "publish", list.Items[0].Type)

	w = doJSON(t, r, http.MethodGet, "/api/ops/approvals", "2", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"count":0`)

	// A cross-owner decision is forbidden and leaves the card pending.
	path := "/api/ops/approvals/" + strconv.FormatInt(approvalID, 10) + "/approve"
	w = doJSON(t, r, http.MethodPost, path, "2", `{}`)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	// First decision wins; the second is a conflict, not a silent overwrite.
	w = doJSON(t, r, http.MethodPost, path, "1", `{}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"status":"answered"`)

	w = doJSON(t, r, http.MethodPost, path, "1", `{}`)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "OPS_APPROVAL_ALREADY_DECIDED")

	// Reject after a decision is the same conflict.
	w = doJSON(t, r, http.MethodPost, "/api/ops/approvals/"+strconv.FormatInt(approvalID, 10)+"/reject", "1", `{"reason":"no"}`)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
}

func TestOpsBudget_OverLimitConflict(t *testing.T) {
	r, db := setupOpsBot(t)
	botID := createTestBot(t, r, "1", "acct-1")

	// Give the bot a control Epic and one child issue with $20 of cost this month.
	mustExec(t, db, `INSERT INTO issues (id, column_id, title, position, issue_type) VALUES (50,1,'ops epic',0,'epic')`)
	mustExec(t, db, `INSERT INTO issues (id, column_id, title, position, parent_issue_id) VALUES (51,1,'post',0,50)`)
	mustExec(t, db, `INSERT INTO issue_exec_events (issue_id, kind, summary, cost_usd) VALUES (51,'cost','text',20)`)
	mustExec(t, db, `UPDATE ops_bots SET epic_issue_id = 50 WHERE id = ?`, botID)

	path := "/api/ops/bots/" + strconv.FormatInt(botID, 10) + "/budget"

	// A limit already exceeded by spend is a conflict.
	w := doJSON(t, r, http.MethodPost, path, "1", `{"monthly_limit":5,"currency":"USD"}`)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "OPS_BUDGET_EXCEEDED")

	// A limit above spend is accepted and the usage reflects it.
	w = doJSON(t, r, http.MethodPost, path, "1", `{"monthly_limit":100,"currency":"USD"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var usage struct {
		Limit     float64 `json:"limit"`
		Spent     float64 `json:"spent"`
		Remaining float64 `json:"remaining"`
		Currency  string  `json:"currency"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &usage))
	assert.InDelta(t, 100, usage.Limit, 0.001)
	assert.InDelta(t, 20, usage.Spent, 0.001)
	assert.InDelta(t, 80, usage.Remaining, 0.001)
	assert.Equal(t, "USD", usage.Currency)

	w = doJSON(t, r, http.MethodGet, path, "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestOpsPublish_IdempotentOnKey(t *testing.T) {
	r, _ := setupOpsBot(t)
	botID := createTestBot(t, r, "1", "acct-1")
	path := "/api/ops/bots/" + strconv.FormatInt(botID, 10) + "/publish"
	body := `{"issue_id":7,"content_version":1,"idempotency_key":"publish-7-v1"}`

	w := doJSON(t, r, http.MethodPost, path, "1", body)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var first struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &first))
	assert.Equal(t, "pending", first.Status)

	// The same key returns the same record, not a duplicate.
	w = doJSON(t, r, http.MethodPost, path, "1", body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var second struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &second))
	assert.Equal(t, first.ID, second.ID)
}

func TestOpsEngagementReply_RecordsApprovalAndAuthorizes(t *testing.T) {
	r, db := setupOpsBot(t)
	botID := createTestBot(t, r, "1", "acct-1")
	mustExec(t, db, `INSERT INTO ops_engagements (id, bot_id, platform_engagement_id, kind, author, text, status)
		VALUES (10, ?, 'c-1', 'comment', 'Alice', 'nice post', 'new')`, botID)

	// Cross-owner write is forbidden and the row keeps its status.
	w := doJSON(t, r, http.MethodPost, "/api/ops/engagements/10/reply", "2", `{"reply_text":"hijack"}`)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	w = doJSON(t, r, http.MethodPost, "/api/ops/engagements/10/reply", "1", `{"reply_text":"thanks!"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"status":"approved"`)
	assert.Contains(t, w.Body.String(), `"reply_text":"thanks!"`)

	// A missing reply body falls back to the suggested reply; with neither it is
	// a 400, and an unknown engagement is a 404.
	w = doJSON(t, r, http.MethodPost, "/api/ops/engagements/10/reply", "1", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	w = doJSON(t, r, http.MethodPost, "/api/ops/engagements/999/reply", "1", `{"reply_text":"x"}`)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

func TestOpsMetrics_ReadsDailyHistory(t *testing.T) {
	r, db := setupOpsBot(t)
	botID := createTestBot(t, r, "1", "acct-1")
	mustExec(t, db, `INSERT INTO ops_metrics_daily (bot_id, date, metrics_json, source)
		VALUES (?, '2026-11-01', '{"followers":120,"views":3000}', 'api')`, botID)

	w := doJSON(t, r, http.MethodGet,
		"/api/ops/bots/"+strconv.FormatInt(botID, 10)+"/metrics?from=2026-11-01&to=2026-11-30", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		BotID int64 `json:"bot_id"`
		Items []struct {
			Date    string          `json:"date"`
			Metrics json.RawMessage `json:"metrics"`
			Source  string          `json:"source"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, botID, body.BotID)
	require.Len(t, body.Items, 1)
	assert.Equal(t, "2026-11-01", body.Items[0].Date)
	assert.JSONEq(t, `{"followers":120,"views":3000}`, string(body.Items[0].Metrics))
	assert.Equal(t, "api", body.Items[0].Source)

	// Bad date format is a client error.
	w = doJSON(t, r, http.MethodGet,
		"/api/ops/bots/"+strconv.FormatInt(botID, 10)+"/metrics?from=11/01/2026", "1", "")
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestOpsPacks_ListValidateExport(t *testing.T) {
	r, _ := setupOpsBot(t)

	w := doJSON(t, r, http.MethodGet, "/api/ops/packs", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var list struct {
		Items []struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
			Status  string `json:"status"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.NotEmpty(t, list.Items)
	assert.Contains(t, w.Body.String(), "wechat_mp")
	assert.Contains(t, w.Body.String(), "xiaohongshu")

	w = doJSON(t, r, http.MethodPost, "/api/ops/packs/validate", "1",
		`{"pack_id":"wechat_mp","version":1}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"valid":true`)

	// Export streams a real zip for a valid single-segment pack id.
	w = doJSON(t, r, http.MethodGet, "/api/ops/packs/wechat_mp/1/export", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Header().Get("Content-Disposition"), "wechat_mp-v1.zip")
	require.Greater(t, w.Body.Len(), 1)
	assert.Equal(t, "PK", string(w.Body.Bytes()[:2]), "export body must be a zip archive")

	// Another owner has no packs and cannot see the first owner's.
	w = doJSON(t, r, http.MethodPost, "/api/ops/packs/validate", "2",
		`{"pack_id":"wechat_mp","version":1}`)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

// TestOpsPackPathAndBackendGuards pins the path-segment hardening: a pack id
// never becomes an unchecked filename, an import only accepts a regular file,
// and a browser backend is a closed enum rather than a path segment.
func TestOpsPackPathAndBackendGuards(t *testing.T) {
	r, _ := setupOpsBot(t)
	botID := createTestBot(t, r, "1", "acct-1")

	// Export rejects a traversal-shaped pack id before touching the filesystem.
	w := doJSON(t, r, http.MethodGet, "/api/ops/packs/../1/export", "1", "")
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// Import only accepts a regular file, not a directory.
	dir := t.TempDir()
	body, err := json.Marshal(map[string]string{"path": dir})
	require.NoError(t, err)
	w = doJSON(t, r, http.MethodPost, "/api/ops/packs/import", "1", string(body))
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// The browser backend is validated instead of being joined into a path.
	w = doJSON(t, r, http.MethodPost,
		"/api/ops/bots/"+strconv.FormatInt(botID, 10)+"/browser-login", "1",
		`{"backend":"../../etc"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	w = doJSON(t, r, http.MethodPost,
		"/api/ops/bots/"+strconv.FormatInt(botID, 10)+"/browser-login", "1",
		`{"backend":"local"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"backend":"local"`)
}

// TestOpsHTTPAndMCPParity pins the "MCP and HTTP behave identically" contract:
// the same request through /api/ops and /mcp/ops produces the same status and
// body for both success and the documented error cases.
func TestOpsHTTPAndMCPParity(t *testing.T) {
	r, _ := setupOpsBot(t)

	// Create one bot through each entry point.
	w := doJSON(t, r, http.MethodPost, "/api/ops/bots", "1",
		`{"role":"account","pack_id":"wechat_mp","account_ref":"acct-1","display_name":"A"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	httpBody := w.Body.String()

	w = doJSON(t, r, http.MethodPost, "/mcp/ops/bots", "1",
		`{"role":"account","pack_id":"wechat_mp","account_ref":"acct-2","display_name":"B"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	// Duplicate account: identical status + error code on both surfaces.
	dupBody := `{"role":"account","pack_id":"wechat_mp","account_ref":"acct-1"}`
	httpDup := doJSON(t, r, http.MethodPost, "/api/ops/bots", "1", dupBody)
	mcpDup := doJSON(t, r, http.MethodPost, "/mcp/ops/bots", "1", dupBody)
	assert.Equal(t, http.StatusConflict, httpDup.Code)
	assert.Equal(t, httpDup.Code, mcpDup.Code)
	assert.Equal(t, httpDup.Body.String(), mcpDup.Body.String())

	// List: the two entry points see the same collection.
	httpList := doJSON(t, r, http.MethodGet, "/api/ops/bots", "1", "")
	mcpList := doJSON(t, r, http.MethodGet, "/mcp/ops/bots", "1", "")
	require.Equal(t, http.StatusOK, httpList.Code)
	assert.Equal(t, httpList.Body.String(), mcpList.Body.String())

	// Cross-owner writes match on both surfaces.
	botPath := "/bots/" + strconv.Itoa(jsonID(t, httpBody))
	httpPause := doJSON(t, r, http.MethodPost, "/api/ops"+botPath+"/pause", "2", "")
	mcpPause := doJSON(t, r, http.MethodPost, "/mcp/ops"+botPath+"/pause", "2", "")
	assert.Equal(t, http.StatusForbidden, httpPause.Code)
	assert.Equal(t, httpPause.Code, mcpPause.Code)
	assert.Equal(t, httpPause.Body.String(), mcpPause.Body.String())
}

func jsonID(t *testing.T, body string) int {
	t.Helper()
	var v struct {
		ID int `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &v))
	return v.ID
}
