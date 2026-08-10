package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

// --- GetUsage ---

type fakeUsageProvider struct {
	out *service.AccountUsage
	err error
}

func (f *fakeUsageProvider) GetForAccount(_ context.Context, _ int64, _ bool) (*service.AccountUsage, error) {
	return f.out, f.err
}

type fakeAccountAuthz struct {
	acc *service.ResolvedAccount
	err error
}

func (f *fakeAccountAuthz) CanAccessClaudeAccount(_ context.Context, _ service.CallerInfo, id int64) (*service.ResolvedAccount, error) {
	return f.acc, f.err
}

func newClaudeAccountHandlerForTest(usage usageProvider, authz accountAuthorizer) *ClaudeAccountHandler {
	h := NewClaudeAccountHandler(nil)
	h.SetUsageDeps(usage, authz)
	return h
}

func TestClaudeAccountHandler_GetUsage_OK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authz := &fakeAccountAuthz{acc: &service.ResolvedAccount{ID: 5, Name: "x"}}
	usage := &fakeUsageProvider{out: &service.AccountUsage{Windows: []service.UsageWindow{{Type: "five_hour_billing"}}}}
	h := newClaudeAccountHandlerForTest(usage, authz)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/claude-accounts/5/usage", nil)
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "member")
	c.Params = gin.Params{{Key: "id", Value: "5"}}
	h.GetUsage(c)
	if w.Code != 200 {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
}

func TestClaudeAccountHandler_GetUsage_404_ProjectsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authz := &fakeAccountAuthz{acc: &service.ResolvedAccount{ID: 5, Name: "x"}}
	usage := &fakeUsageProvider{err: service.ErrClaudeProjectsNotFound}
	h := newClaudeAccountHandlerForTest(usage, authz)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/claude-accounts/5/usage", nil)
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "member")
	c.Params = gin.Params{{Key: "id", Value: "5"}}
	h.GetUsage(c)
	if w.Code != 404 {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"projects_not_found"`) {
		t.Fatalf("body missing code: %s", w.Body.String())
	}
}

func TestClaudeAccountHandler_GetUsage_404_AccountInvisibleToCaller(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authz := &fakeAccountAuthz{err: service.ErrNotVisible}
	h := newClaudeAccountHandlerForTest(&fakeUsageProvider{}, authz)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/claude-accounts/5/usage", nil)
	c.Set("auth_user_id", int64(2))
	c.Set("auth_role", "member")
	c.Params = gin.Params{{Key: "id", Value: "5"}}
	h.GetUsage(c)
	if w.Code != 404 {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"not_found"`) {
		t.Fatalf("body missing code: %s", w.Body.String())
	}
}

func TestClaudeAccountHandler_GetUsage_400_BadID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newClaudeAccountHandlerForTest(&fakeUsageProvider{}, &fakeAccountAuthz{})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/claude-accounts/abc/usage", nil)
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "member")
	c.Params = gin.Params{{Key: "id", Value: "abc"}}
	h.GetUsage(c)
	if w.Code != 400 {
		t.Fatalf("status %d, body %s", w.Code, w.Body.String())
	}
}

// --- IssueLoginToken ---

// newClaudeAccountHandlerWithRealSvc spins up a real ClaudeAccountService
// backed by an in-memory SQLite DB so handler-level tests can exercise the
// row-status branch in IssueLoginToken end-to-end.
func newClaudeAccountHandlerWithRealSvc(t *testing.T) (*ClaudeAccountHandler, *service.ClaudeAccountService, *sql.DB) {
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
	q := store.New(db)
	svc := service.NewClaudeAccountService(store.Wrap(db), q, t.TempDir())
	return NewClaudeAccountHandler(svc), svc, db
}

// Default row (config_dir == "") cannot be deleted (ErrCannotDeleteDefault),
// so IssueLoginToken MUST accept active re-login on it — otherwise the
// "delete + recreate to switch credentials" workaround is impossible and
// the host account is stranded. See claude_account.go:IssueLoginToken doc.
func TestClaudeAccountHandler_IssueLoginToken_ActiveDefaultRow_AllowsRelogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _, db := newClaudeAccountHandlerWithRealSvc(t)

	// store.Migrate seeds the __default__ row (config_dir = ""). Find it.
	var defaultID int64
	if err := db.QueryRow(`SELECT id FROM claude_accounts WHERE config_dir = ''`).Scan(&defaultID); err != nil {
		t.Fatalf("locate default row: %v", err)
	}
	// Drive it to active (mimics post-login state).
	if _, err := db.ExecContext(context.Background(),
		`UPDATE claude_accounts SET status='active' WHERE id=?`, defaultID); err != nil {
		t.Fatalf("seed active: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/claude-accounts/0/login", nil)
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "admin")
	c.Params = gin.Params{{Key: "id", Value: itoa64(defaultID)}}
	h.IssueLoginToken(c)

	if w.Code != http.StatusOK {
		t.Fatalf("default row active re-login should succeed, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"ws_token"`) {
		t.Fatalf("expected ws_token in response, got %s", w.Body.String())
	}
}

// Managed (non-default) rows still refuse active re-login — the "delete +
// recreate" workaround is feasible for them and the spec §"多 admin 同时登录"
// guard against concurrent credential clobbering still applies. Response
// must include `code:"already_authed"` so the FE can translate.
func TestClaudeAccountHandler_IssueLoginToken_ActiveManagedRow_Refuses409(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, svc, db := newClaudeAccountHandlerWithRealSvc(t)

	created, err := svc.Create(context.Background(), service.CreateAccountInput{
		Name: "managed", Visibility: "public", CallerID: 1,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`UPDATE claude_accounts SET status='active' WHERE id=?`, created.ID); err != nil {
		t.Fatalf("seed active: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/claude-accounts/0/login", nil)
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "admin")
	c.Params = gin.Params{{Key: "id", Value: itoa64(created.ID)}}
	h.IssueLoginToken(c)

	if w.Code != http.StatusConflict {
		t.Fatalf("managed row active re-login should 409, got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"already_authed"`) {
		t.Fatalf("expected error=already_authed in body, got %s", body)
	}
	if !strings.Contains(body, `"code":"already_authed"`) {
		t.Fatalf("expected code=already_authed for FE translation, got %s", body)
	}
}

// Pending rows always succeed regardless of default-vs-managed.
func TestClaudeAccountHandler_IssueLoginToken_PendingRow_Succeeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, svc, _ := newClaudeAccountHandlerWithRealSvc(t)

	created, err := svc.Create(context.Background(), service.CreateAccountInput{
		Name: "pending", Visibility: "public", CallerID: 1,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// status='pending' as inserted by Create.

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/claude-accounts/0/login", nil)
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "admin")
	c.Params = gin.Params{{Key: "id", Value: itoa64(created.ID)}}
	h.IssueLoginToken(c)

	if w.Code != http.StatusOK {
		t.Fatalf("pending row re-login should succeed, got %d body=%s", w.Code, w.Body.String())
	}
}

func itoa64(v int64) string { return strconv.FormatInt(v, 10) }
