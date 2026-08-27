package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

func newTokenUsageTestHandler(t *testing.T) (*TokenUsageHandler, int64) {
	t.Helper()
	store.Driver = "sqlite"
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	q := store.New(db)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "tu-ws", Path: "/tmp/tu-ws", Status: "created", OwnerType: "user", OwnerID: 1,
	})
	if err != nil {
		t.Fatalf("create ws: %v", err)
	}
	_ = q.UpsertWorkspaceTokenHourly(context.Background(), store.UpsertWorkspaceTokenHourlyParams{
		WorkspaceID: ws.ID, BucketHour: time.Now().UTC().Truncate(time.Hour), InputTokens: 42,
	})
	return &TokenUsageHandler{Svc: service.NewTokenUsageService(q), DB: db}, ws.ID
}

func TestWorkspaceUsageSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, wsID := newTokenUsageTestHandler(t)
	r := gin.New()
	r.GET("/workspaces/:id/token-usage", h.WorkspaceUsage)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/workspaces/1/token-usage", nil)
	_ = wsID
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Buckets []service.TokenBucket `json:"buckets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Buckets) != 1 || resp.Buckets[0].InputTokens != 42 {
		t.Fatalf("buckets wrong: %+v", resp.Buckets)
	}
}

func TestWorkspaceUsageInvalidRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTokenUsageTestHandler(t)
	r := gin.New()
	r.GET("/workspaces/:id/token-usage", h.WorkspaceUsage)

	rec := httptest.NewRecorder()
	// from after to -> 400
	req := httptest.NewRequest(http.MethodGet,
		"/workspaces/1/token-usage?from=2026-06-02T00:00:00Z&to=2026-06-01T00:00:00Z", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOwnerUsageMissingOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTokenUsageTestHandler(t)
	r := gin.New()
	r.GET("/token-usage", h.OwnerUsage)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/token-usage", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// newUsageAuthzTestEnv seeds the role matrix the usage report must respect and
// wires BOTH usage handlers with a real Authz (the older helpers above leave
// Authz nil, which skips the authorization branch entirely).
//
//	user 1 — global admin
//	user 2 — org 50 'owner'   → administers org 50
//	user 3 — org 50 'member'  → plain member
//	user 4 — unrelated user
func newUsageAuthzTestEnv(t *testing.T) (*TokenUsageHandler, *ProviderUsageHandler) {
	t.Helper()
	store.Driver = "sqlite"
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	exec := func(q string, args ...any) {
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, username, password_hash, role) VALUES (1,'gadmin','x','admin')`)
	exec(`INSERT INTO users (id, username, password_hash, role) VALUES (2,'orgowner','x','member')`)
	exec(`INSERT INTO users (id, username, password_hash, role) VALUES (3,'orgmember','x','member')`)
	exec(`INSERT INTO users (id, username, password_hash, role) VALUES (4,'other','x','member')`)
	exec(`INSERT INTO organizations (id, slug, name, created_by) VALUES (50,'o50','Org 50',2)`)
	exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (50,2,'owner')`)
	exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (50,3,'member')`)

	q := store.New(db)
	authz := service.NewAuthz(q, db)
	return &TokenUsageHandler{Svc: service.NewTokenUsageService(q), Authz: authz, DB: db},
		&ProviderUsageHandler{Svc: service.NewProviderUsageService(q), Authz: authz, DB: db}
}

// usageReq issues a GET against handler fn with auth_user_id set to uid.
func usageReq(t *testing.T, fn gin.HandlerFunc, uid int64, target string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	c.Set("auth_user_id", uid)
	fn(c)
	return w
}

// TestOwnerUsage_OrgLimitedToAdmins locks the tightened read gate: usage exposes
// cost structure and per-person working patterns, so an org's numbers are for
// those who administer it — NOT every member, which is what EnsureOwnerReadable
// would have allowed.
func TestOwnerUsage_OrgLimitedToAdmins(t *testing.T) {
	tu, pu := newUsageAuthzTestEnv(t)

	cases := []struct {
		name string
		uid  int64
		want int
	}{
		{"org owner may read", 2, http.StatusOK},
		{"plain org member rejected", 3, http.StatusForbidden},
		{"non-member rejected", 4, http.StatusForbidden},
		{"global admin may read", 1, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := usageReq(t, tu.OwnerUsage, tc.uid, "/api/token-usage?owner=org:50")
			if w.Code != tc.want {
				t.Fatalf("token-usage: want %d, got %d (%s)", tc.want, w.Code, w.Body.String())
			}
			w = usageReq(t, pu.Usage, tc.uid, "/api/provider-usage?owner=org:50")
			if w.Code != tc.want {
				t.Fatalf("provider-usage: want %d, got %d (%s)", tc.want, w.Code, w.Body.String())
			}
		})
	}
}

// A user's personal usage stays private to them; a global admin may still read it
// (cross-team cost review), consistent with what the owner picker offers them.
func TestOwnerUsage_PersonalScope(t *testing.T) {
	tu, _ := newUsageAuthzTestEnv(t)

	if w := usageReq(t, tu.OwnerUsage, 2, "/api/token-usage?owner=user:2"); w.Code != http.StatusOK {
		t.Fatalf("own usage: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if w := usageReq(t, tu.OwnerUsage, 3, "/api/token-usage?owner=user:2"); w.Code != http.StatusForbidden {
		t.Fatalf("other's usage: want 403, got %d (%s)", w.Code, w.Body.String())
	}
	if w := usageReq(t, tu.OwnerUsage, 1, "/api/token-usage?owner=user:2"); w.Code != http.StatusOK {
		t.Fatalf("global admin reading another user: want 200, got %d (%s)", w.Code, w.Body.String())
	}
}

// The picker's options must match what the gate actually admits, otherwise the UI
// would offer an owner that 403s on selection.
func TestReadableOwners_MatchesGate(t *testing.T) {
	_, pu := newUsageAuthzTestEnv(t)

	decode := func(w *httptest.ResponseRecorder) []string {
		t.Helper()
		var resp struct {
			Items []struct {
				Type string `json:"type"`
				ID   int64  `json:"id"`
			} `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v (%s)", err, w.Body.String())
		}
		out := make([]string, 0, len(resp.Items))
		for _, it := range resp.Items {
			out = append(out, it.Type+":"+itoa64(it.ID))
		}
		return out
	}

	// org owner: personal + the administered org.
	got := decode(usageReq(t, pu.ReadableOwners, 2, "/api/provider-usage/owners"))
	want := []string{"user:2", "org:50"}
	if len(got) != len(want) {
		t.Fatalf("org owner options: want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("org owner options: want %v, got %v", want, got)
		}
	}

	// plain member: personal only — the org they merely belong to is not offered.
	got = decode(usageReq(t, pu.ReadableOwners, 3, "/api/provider-usage/owners"))
	if len(got) != 1 || got[0] != "user:3" {
		t.Fatalf("plain member options: want [user:3], got %v", got)
	}

	// global admin: personal first, then every org and every other user.
	got = decode(usageReq(t, pu.ReadableOwners, 1, "/api/provider-usage/owners"))
	if len(got) != 5 || got[0] != "user:1" {
		t.Fatalf("global admin options: want 5 entries starting at user:1, got %v", got)
	}
}

func itoa64(n int64) string {
	return strconv.FormatInt(n, 10)
}
