// Tests for the two AI-onboarding HTTP endpoints:
//   - POST /api/projects/:id/imbot/onboarding (Endpoint A, authenticated)
//   - POST /api/imbot/onboarding/:token/credential (Endpoint B, token-auth)
package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

// setupOnboardingDB opens an in-memory SQLite database with schema applied.
func setupOnboardingDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	store.Migrate(db)
	return db
}

// onboardingTestEnv holds the minimal services for endpoint tests.
type onboardingTestEnv struct {
	db          *sql.DB
	q           *store.Queries
	svc         *service.IMBotService
	userID      int64
	projectID   int64
	workspaceID int64
	workspacePath string
	router      *gin.Engine
}

// fakeRouter is a TaskRouter that always returns a fresh PlanTarget.
type fakeRouter struct {
	lastIssueID     int64
	lastWorkspaceID int64
	err             error
	lastHint        service.RouteHint
}

func (f *fakeRouter) RouteInProject(_ context.Context, _ service.OwnerRef, _ int64, _ string, hint service.RouteHint) (service.PlanTarget, error) {
	f.lastHint = hint
	if f.err != nil {
		return service.PlanTarget{}, f.err
	}
	return service.PlanTarget{
		IssueID:     f.lastIssueID,
		WorkspaceID: f.lastWorkspaceID,
		IsNew:       true,
	}, nil
}

// fakeDeliverer records Deliver calls, capturing workDir for assertion.
type fakeDeliverer struct {
	delivered []string
	workDirs  []string
	err       error
}

func (f *fakeDeliverer) Deliver(_ context.Context, _ int64, workDir, content, _ string) (bool, int64, error) {
	f.delivered = append(f.delivered, content)
	f.workDirs = append(f.workDirs, workDir)
	return false, 0, f.err
}

func newOnboardingEnv(t *testing.T) *onboardingTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := setupOnboardingDB(t)
	q := store.New(db)
	ctx := context.Background()

	// Create a user
	res, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, role) VALUES ('tester', 'x', 'admin')`)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, _ := res.LastInsertId()

	// Create a project owned by that user
	proj, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name:      "test-proj",
		OwnerType: "user",
		OwnerID:   userID,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Create a workspace row so WorkspacePath() can look it up by ID.
	// The path must be non-empty — AgentProxy.Deliver returns immediately for
	// an empty workDir, which is the exact bug we guard against in tests.
	wsPath := t.TempDir()
	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{},
		Name:      "onboarding-ws",
		Path:      wsPath,
		Status:    "idle",
		OwnerType: "user",
		OwnerID:   userID,
		CreatedBy: sql.NullInt64{Int64: userID, Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// Build the IMBotService
	kr, err := crypto.LoadOrCreate(t.TempDir() + "/secret")
	if err != nil {
		t.Fatalf("LoadOrCreate keyring: %v", err)
	}
	adapters := map[imbot.ChannelType]imbot.ChannelAdapter{
		imbot.ChannelLark: &onboardStubAdapter{},
	}
	authz := service.NewAuthz(q, db)
	svc := service.NewIMBotService(q, db, kr, authz, adapters)

	return &onboardingTestEnv{
		db:            db,
		q:             q,
		svc:           svc,
		userID:        userID,
		projectID:     proj.ID,
		workspaceID:   ws.ID,
		workspacePath: wsPath,
	}
}

// onboardStubAdapter is a no-op adapter for tests.
type onboardStubAdapter struct{}

func (a *onboardStubAdapter) Type() imbot.ChannelType { return imbot.ChannelLark }
func (a *onboardStubAdapter) Connect(_ context.Context, _ imbot.Credential, _ imbot.InboundHandler) error {
	return nil
}
func (a *onboardStubAdapter) Push(_ context.Context, _ imbot.Credential, _ imbot.OutboundMessage) error {
	return nil
}
func (a *onboardStubAdapter) VerifyWebhook(_ *http.Request, _ imbot.Credential) (imbot.InboundEvent, error) {
	return imbot.InboundEvent{}, nil
}
func (a *onboardStubAdapter) Challenge(_ *http.Request) ([]byte, bool) { return nil, false }

// buildOnboardingRouter wires the handler and returns a gin engine for testing.
// The fakeRouter records calls; fakeDeliverer records deliver calls.
func buildOnboardingRouter(t *testing.T, env *onboardingTestEnv, router *fakeRouter, deliverer *fakeDeliverer) *gin.Engine {
	t.Helper()
	authz := service.NewAuthz(env.q, env.db)
	h := api.NewIMBotHandler(env.svc, authz, env.db)
	h.SetDispatch(router)
	h.SetDeliverer(deliverer)

	r := gin.New()

	// Endpoint A — project-scoped, simulating auth middleware by pre-setting auth_user_id
	projectGroup := r.Group("/api/projects")
	projectGroup.Use(func(c *gin.Context) {
		c.Set("auth_user_id", env.userID)
		c.Next()
	})
	projectGroup.POST("/:id/imbot/onboarding", h.StartOnboarding)

	// Endpoint B — public (no auth middleware)
	r.POST("/api/imbot/onboarding/:token/credential", h.SubmitOnboardingCredential)

	// Endpoint C — GET token info (public, read-only)
	r.GET("/api/imbot/onboarding/:token/info", h.GetOnboardingInfo)

	return r
}

// ─── Endpoint B tests ───────────────────────────────────────────────────────

// TestEndpointB_HappyPath: issue a token → POST credential → 200 ok + channel_id.
func TestEndpointB_HappyPath(t *testing.T) {
	env := newOnboardingEnv(t)
	r := buildOnboardingRouter(t, env, &fakeRouter{lastIssueID: 1, lastWorkspaceID: 1}, &fakeDeliverer{})

	// Issue a token for the project.
	ctx := context.Background()
	rawToken, err := env.svc.IssueOnboardingToken(ctx, env.projectID, "lark", "my-bot", "stream")
	if err != nil {
		t.Fatalf("IssueOnboardingToken: %v", err)
	}

	// POST credential
	body, _ := json.Marshal(map[string]any{"app_id": "cli_test", "app_secret": "shhh"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/imbot/onboarding/"+rawToken+"/credential", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("expected ok=true, got %v", resp["ok"])
	}
	if _, ok := resp["channel_id"]; !ok {
		t.Error("expected channel_id in response")
	}
	// Verify no secret echoed
	bodyStr := w.Body.String()
	if strings.Contains(bodyStr, "shhh") || strings.Contains(bodyStr, "cli_test") {
		t.Error("response must not echo submitted credential values")
	}
}

// TestEndpointB_InvalidToken: an unknown token → 410 Gone.
func TestEndpointB_InvalidToken(t *testing.T) {
	env := newOnboardingEnv(t)
	r := buildOnboardingRouter(t, env, &fakeRouter{lastIssueID: 1, lastWorkspaceID: 1}, &fakeDeliverer{})

	body, _ := json.Marshal(map[string]any{"app_id": "x"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/imbot/onboarding/totally-invalid-token/credential", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("expected 410, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] == nil {
		t.Error("expected error field in 410 response")
	}
}

// TestEndpointB_BadJSON: malformed JSON → 400.
func TestEndpointB_BadJSON(t *testing.T) {
	env := newOnboardingEnv(t)
	r := buildOnboardingRouter(t, env, &fakeRouter{lastIssueID: 1, lastWorkspaceID: 1}, &fakeDeliverer{})

	// Bad JSON must be rejected with 400 before any token/DB lookup.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/imbot/onboarding/sometoken/credential",
		bytes.NewBufferString(`{not valid json`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

// ─── Endpoint A tests ───────────────────────────────────────────────────────

// TestEndpointA_Returns201: start onboarding → 201 with issue_id + workspace_id.
func TestEndpointA_Returns201(t *testing.T) {
	env := newOnboardingEnv(t)
	// Use the real workspace ID so WorkspacePath() succeeds.
	fr := &fakeRouter{lastIssueID: 42, lastWorkspaceID: env.workspaceID}
	fd := &fakeDeliverer{}
	r := buildOnboardingRouter(t, env, fr, fd)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST",
		"/api/projects/"+itoa(env.projectID)+"/imbot/onboarding", nil)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["issue_id"] == nil {
		t.Error("expected issue_id in response")
	} else if int64(resp["issue_id"].(float64)) != fr.lastIssueID {
		t.Errorf("expected issue_id=%d, got %v", fr.lastIssueID, resp["issue_id"])
	}
	if resp["workspace_id"] == nil {
		t.Error("expected workspace_id in response")
	} else if int64(resp["workspace_id"].(float64)) != fr.lastWorkspaceID {
		t.Errorf("expected workspace_id=%d, got %v", fr.lastWorkspaceID, resp["workspace_id"])
	}
}

// TestEndpointA_UsesBypassPermissionsMode: onboarding is an interactive wizard,
// so it must create the workspace in "bypassPermissions" (skip prompts, but NO
// auto-continue watchdog) rather than the default "autohost" — otherwise the
// watchdog would barrel through without waiting for the user's platform choice
// and credential entry.
func TestEndpointA_UsesBypassPermissionsMode(t *testing.T) {
	env := newOnboardingEnv(t)
	fr := &fakeRouter{lastIssueID: 9, lastWorkspaceID: env.workspaceID}
	r := buildOnboardingRouter(t, env, fr, &fakeDeliverer{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST",
		"/api/projects/"+itoa(env.projectID)+"/imbot/onboarding", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	if fr.lastHint.PermissionMode != "bypassPermissions" {
		t.Errorf("onboarding must route with PermissionMode=bypassPermissions (interactive), got %q", fr.lastHint.PermissionMode)
	}
	if !fr.lastHint.ForceNew {
		t.Error("onboarding must route with ForceNew")
	}
}

// TestEndpointA_DeliversKickoffPrompt: after routing, the onboarding kickoff
// prompt is delivered to the workspace agent via the deliverer with a non-empty
// workDir. This is the guard test for C1: an empty workDir means AgentProxy drops
// the message silently, so workDir MUST be non-empty.
func TestEndpointA_DeliversKickoffPrompt(t *testing.T) {
	env := newOnboardingEnv(t)
	// Use the real workspace ID so WorkspacePath() can resolve a real path.
	fr := &fakeRouter{lastIssueID: 7, lastWorkspaceID: env.workspaceID}
	fd := &fakeDeliverer{}
	r := buildOnboardingRouter(t, env, fr, fd)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST",
		"/api/projects/"+itoa(env.projectID)+"/imbot/onboarding", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}
	if len(fd.delivered) == 0 {
		t.Error("expected kickoff prompt to be delivered to the workspace agent")
	}
	if fd.delivered[0] == "" {
		t.Error("kickoff prompt must not be empty")
	}
	// C1 guard: workDir must not be empty — AgentProxy.Deliver short-circuits on "".
	if len(fd.workDirs) == 0 || fd.workDirs[0] == "" {
		t.Error("C1 regression: Deliver was called with empty workDir; kickoff prompt would be silently dropped by AgentProxy")
	}
	if fd.workDirs[0] != env.workspacePath {
		t.Errorf("workDir = %q, want %q (the workspace path from the DB)", fd.workDirs[0], env.workspacePath)
	}
}

// TestEndpointA_WorkDirNonEmpty_Guard is the explicit RED→GREEN guard test for
// the C1 bug. It verifies that StartOnboarding fetches the real workspace path
// and delivers it to the agent. If workDir were empty (the old bug), this test
// fails because fd.workDirs[0] == "".
func TestEndpointA_WorkDirNonEmpty_Guard(t *testing.T) {
	env := newOnboardingEnv(t)
	// Point the fake router at the real workspace row created in newOnboardingEnv.
	// WorkspacePath() will look up env.workspaceID and return env.workspacePath.
	fr := &fakeRouter{lastIssueID: 1, lastWorkspaceID: env.workspaceID}
	fd := &fakeDeliverer{}
	r := buildOnboardingRouter(t, env, fr, fd)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST",
		"/api/projects/"+itoa(env.projectID)+"/imbot/onboarding", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}

	// The deliverer MUST have been called exactly once.
	if len(fd.delivered) != 1 {
		t.Fatalf("expected 1 Deliver call, got %d", len(fd.delivered))
	}
	// workDir must be non-empty (C1 fix assertion).
	if fd.workDirs[0] == "" {
		t.Fatal("C1 regression: Deliver called with empty workDir — kickoff prompt is silently dropped")
	}
	// workDir must equal the real workspace path stored in the DB.
	if fd.workDirs[0] != env.workspacePath {
		t.Errorf("workDir = %q, want env.workspacePath = %q", fd.workDirs[0], env.workspacePath)
	}
	// Content must be the kickoff prompt (non-empty).
	if fd.delivered[0] == "" {
		t.Error("kickoff prompt content must not be empty")
	}
}

// ─── Endpoint C (GET /api/imbot/onboarding/:token/info) tests ────────────────

// TestEndpointC_ValidToken: GET token info returns 200 with platform/channel_name/connection_mode
// AND the token remains usable afterward (GET does NOT consume the token).
func TestEndpointC_ValidToken(t *testing.T) {
	env := newOnboardingEnv(t)
	r := buildOnboardingRouter(t, env, &fakeRouter{lastIssueID: 1, lastWorkspaceID: 1}, &fakeDeliverer{})

	ctx := context.Background()
	rawToken, err := env.svc.IssueOnboardingToken(ctx, env.projectID, "dingtalk", "钉钉-测试群", "stream")
	if err != nil {
		t.Fatalf("IssueOnboardingToken: %v", err)
	}

	// GET token info.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/imbot/onboarding/"+rawToken+"/info", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["platform"] != "dingtalk" {
		t.Errorf("platform = %v, want dingtalk", resp["platform"])
	}
	if resp["channel_name"] != "钉钉-测试群" {
		t.Errorf("channel_name = %v, want 钉钉-测试群", resp["channel_name"])
	}
	if resp["connection_mode"] != "stream" {
		t.Errorf("connection_mode = %v, want stream", resp["connection_mode"])
	}

	// Token must still be usable — GET must NOT have consumed it.
	body, _ := json.Marshal(map[string]any{
		"client_id": "cli_x", "client_secret": "sec", "robot_code": "rc1",
	})
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/imbot/onboarding/"+rawToken+"/credential", bytes.NewBuffer(body))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("token must still be usable after GET info, got %d body=%s", w2.Code, w2.Body.String())
	}
	var resp2 map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	if resp2["ok"] != true {
		t.Errorf("expected ok=true after submit, got %v", resp2["ok"])
	}
}

// TestEndpointC_ExpiredToken: expired token → 410.
func TestEndpointC_ExpiredToken(t *testing.T) {
	env := newOnboardingEnv(t)
	r := buildOnboardingRouter(t, env, &fakeRouter{lastIssueID: 1, lastWorkspaceID: 1}, &fakeDeliverer{})

	ctx := context.Background()
	rawToken, err := env.svc.IssueOnboardingToken(ctx, env.projectID, "lark", "expired-c-bot", "stream")
	if err != nil {
		t.Fatalf("IssueOnboardingToken: %v", err)
	}

	// Age to past.
	pastStr := "2000-01-01 00:00:00"
	if _, err := env.db.ExecContext(ctx,
		`UPDATE im_bot_onboarding_tokens SET expires_at = ? WHERE channel_name = 'expired-c-bot'`, pastStr); err != nil {
		t.Fatalf("age token: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/imbot/onboarding/"+rawToken+"/info", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("expected 410, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestEndpointC_UsedToken: already-used token → 410.
func TestEndpointC_UsedToken(t *testing.T) {
	env := newOnboardingEnv(t)
	r := buildOnboardingRouter(t, env, &fakeRouter{lastIssueID: 1, lastWorkspaceID: 1}, &fakeDeliverer{})

	ctx := context.Background()
	rawToken, err := env.svc.IssueOnboardingToken(ctx, env.projectID, "lark", "used-c-bot", "stream")
	if err != nil {
		t.Fatalf("IssueOnboardingToken: %v", err)
	}

	// Consume it.
	body, _ := json.Marshal(map[string]any{"app_id": "a", "app_secret": "b"})
	w0 := httptest.NewRecorder()
	req0 := httptest.NewRequest("POST", "/api/imbot/onboarding/"+rawToken+"/credential", bytes.NewBuffer(body))
	req0.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w0, req0)
	if w0.Code != http.StatusOK {
		t.Fatalf("initial submit failed: %d body=%s", w0.Code, w0.Body.String())
	}

	// Now GET info on used token → 410.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/imbot/onboarding/"+rawToken+"/info", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("expected 410 for used token, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestEndpointC_UnknownToken: unknown token → 410.
func TestEndpointC_UnknownToken(t *testing.T) {
	env := newOnboardingEnv(t)
	r := buildOnboardingRouter(t, env, &fakeRouter{lastIssueID: 1, lastWorkspaceID: 1}, &fakeDeliverer{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/imbot/onboarding/totally-unknown-token-xyz/info", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Fatalf("expected 410 for unknown token, got %d body=%s", w.Code, w.Body.String())
	}
}

