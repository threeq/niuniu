// The "允许写入" toggle is an OWNER-scoped grant, not a per-user one: the MCP
// proxy reads it as the WORKSPACE's owner ('user' personal space / 'org'), so
// an org grant governs every member's agent running in that org's workspaces.
// Regression context: the toggle used to be keyed by the browser user, so a
// member flipping it ON left every agent running under another session user
// (or the org fallback identity) reading write_enabled=false — list_providers
// reported the provider unwritable and call_external_api rejected non-GETs.
package service

import (
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

func setupWritePrefsTest(t *testing.T) (*ExternalProviderService, *ExternalProxyService, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	store.Migrate(db)
	q := store.New(db)
	provSvc := NewExternalProviderService(q, db)
	proxySvc := &ExternalProxyService{
		q:           q,
		providerSvc: provSvc,
	}
	return provSvc, proxySvc, db
}

func seedWritePrefsProvider(t *testing.T, svc *ExternalProviderService, name string) int64 {
	t.Helper()
	p, err := svc.Create(t.Context(), name, name, "https://example.com")
	if err != nil {
		t.Fatalf("create provider %q: %v", name, err)
	}
	return p.ID
}

// Org-row and user-row grants are independent scopes: flipping the toggle for
// an org must not leak into any member's personal scope, and vice versa.
func TestAPIWritePrefs_OwnerScopeIsolation(t *testing.T) {
	svc, _, _ := setupWritePrefsTest(t)
	provID := seedWritePrefsProvider(t, svc, "scope-iso")

	if err := svc.SetWritePref(t.Context(), "org", 40, provID, true); err != nil {
		t.Fatalf("set org pref: %v", err)
	}
	if err := svc.SetWritePref(t.Context(), "user", 7, provID, false); err != nil {
		t.Fatalf("set user pref: %v", err)
	}

	orgPrefs, err := svc.ListWritePrefs(t.Context(), "org", 40)
	if err != nil {
		t.Fatalf("list org prefs: %v", err)
	}
	if !orgPrefs[provID] {
		t.Fatal("org scope must see its own enabled grant")
	}
	userPrefs, err := svc.ListWritePrefs(t.Context(), "user", 7)
	if err != nil {
		t.Fatalf("list user prefs: %v", err)
	}
	if userPrefs[provID] {
		t.Fatal("user scope must NOT inherit the org grant")
	}
	// A different org is a different scope entirely.
	otherOrgPrefs, err := svc.ListWritePrefs(t.Context(), "org", 41)
	if err != nil {
		t.Fatalf("list other-org prefs: %v", err)
	}
	if otherOrgPrefs[provID] {
		t.Fatal("another org must NOT inherit org 40's grant")
	}
}

// THE customer regression: an agent running inside an org-owned workspace
// must read the ORG's write grant, even when the calling user has no prefs
// row of their own (previously the lookup was keyed by the calling user and
// returned false).
func TestCallGate_ReadsWorkspaceOwnerScope(t *testing.T) {
	svc, proxy, db := setupWritePrefsTest(t)
	ctx := t.Context()

	provID := seedWritePrefsProvider(t, svc, "ws-owner-gate")
	// Org 40 grants writes for this provider.
	if err := svc.SetWritePref(ctx, "org", 40, provID, true); err != nil {
		t.Fatalf("set org pref: %v", err)
	}

	// An org-owned workspace whose session user (member 7) has NO prefs row.
	res, err := db.Exec(`INSERT INTO workspaces (path, owner_type, owner_id, current_session_user_id)
		VALUES ('/tmp/ws', 'org', 40, 7)`)
	if err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	wsID, _ := res.LastInsertId()

	ownerType, ownerID := proxy.writePrefScope(ctx, ProxyInput{UserID: 7, WorkspaceID: wsID})
	if ownerType != "org" || ownerID != 40 {
		t.Fatalf("writePrefScope = (%s,%d), want (org,40)", ownerType, ownerID)
	}
	prefs, err := proxy.q.GetAPIWritePrefs(ctx, store.GetAPIWritePrefsParams{
		OwnerType: ownerType, OwnerID: ownerID, ProviderID: provID,
	})
	if err != nil {
		t.Fatalf("org grant row must be found under the workspace owner scope: %v", err)
	}
	if prefs.Enabled != 1 {
		t.Fatal("the org grant must read enabled — the member's missing row must not shadow it")
	}

	// SPA direct calls (no workspace) scope to the caller's personal owner,
	// where no row exists — the whitelist fallback keeps them read-only.
	spaType, spaID := proxy.writePrefScope(ctx, ProxyInput{UserID: 7})
	if spaType != "user" || spaID != 7 {
		t.Fatalf("SPA writePrefScope = (%s,%d), want (user,7)", spaType, spaID)
	}
	if _, err := proxy.q.GetAPIWritePrefs(ctx, store.GetAPIWritePrefsParams{
		OwnerType: spaType, OwnerID: spaID, ProviderID: provID,
	}); err == nil {
		t.Fatal("SPA scope must not see the org grant")
	}
}
