package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func setupProviderSeedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	store.Driver = "sqlite"
	require.NoError(t, store.ApplySchema(db))
	store.Migrate(db)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestSeedSystem_BuiltinProviders confirms the built-in system providers are
// seeded (github + imap + serp-api + gsc) and TAPD is not (it is a regular
// user-created provider). imap is a credential-type anchor for the office-mail
// scene; serp-api + gsc are the GEO/SEO data-source connectors.
func TestSeedSystem_BuiltinProviders(t *testing.T) {
	ctx := context.Background()
	db := setupProviderSeedTestDB(t)
	svc := NewExternalProviderService(store.New(db), db)

	inserted, err := svc.SeedSystem(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, inserted, "github + imap + serp-api + gsc are seeded")

	if _, err := svc.GetByName(ctx, "tapd"); err == nil {
		t.Fatal("tapd must NOT be seeded as a system provider")
	}

	gh, err := svc.GetByName(ctx, "github")
	require.NoError(t, err)
	require.Equal(t, CreatedBySystem, gh.CreatedBy)

	imap, err := svc.GetByName(ctx, "imap")
	require.NoError(t, err)
	require.Equal(t, CreatedBySystem, imap.CreatedBy)
	require.Equal(t, []string{"imap"}, imap.AuthModes())

	// SerpAPI — query-param auth, read-only GET whitelist, stable base URL.
	serp, err := svc.GetByName(ctx, "serp-api")
	require.NoError(t, err)
	require.Equal(t, CreatedBySystem, serp.CreatedBy)
	require.Equal(t, "query_param", serp.AuthType)
	require.Equal(t, "api_key", serp.AuthHeader)
	require.Equal(t, []string{"query_param"}, serp.AuthModes())
	require.Equal(t, "https://serpapi.com", serp.APIBaseURL)

	// GSC — Bearer OAuth token; searchAnalytics/query POST is a read, allowed
	// in the default safe set.
	gsc, err := svc.GetByName(ctx, "gsc")
	require.NoError(t, err)
	require.Equal(t, CreatedBySystem, gsc.CreatedBy)
	require.Equal(t, []string{"bearer"}, gsc.AuthModes())
	require.True(t, svc.IsAllowed(gsc.Whitelist, "POST", "/webmasters/v3/sites/sc-domain:example.com/searchAnalytics/query"),
		"GSC searchAnalytics query POST must pass the read-only default gate")
	require.False(t, svc.IsAllowed(gsc.Whitelist, "POST", "/webmasters/v3/sites/sc-domain:example.com/sitemaps/x"),
		"non-query POSTs must still require the write toggle")
}

// TestSeedSystem_Idempotent confirms a second boot does not duplicate rows.
func TestSeedSystem_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := setupProviderSeedTestDB(t)
	svc := NewExternalProviderService(store.New(db), db)

	first, err := svc.SeedSystem(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, first, "github + imap + serp-api + gsc inserted on fresh DB")

	second, err := svc.SeedSystem(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, second, "no inserts on second boot")

	all, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 4)
}

// TestDemoteDeprecatedSystemProviders is the migration test: a TAPD row left
// over from when it was system-seeded must be released to the user
// (created_by='user') so it becomes editable/deletable. Idempotent, and a
// user-created TAPD row is untouched.
func TestDemoteDeprecatedSystemProviders(t *testing.T) {
	ctx := context.Background()
	db := setupProviderSeedTestDB(t)
	q := store.New(db)
	svc := NewExternalProviderService(q, db)

	// Legacy locked system TAPD row.
	_, err := q.CreateProvider(ctx, store.CreateProviderParams{
		Name:       "tapd",
		Label:      "TAPD",
		ApiBaseUrl: "https://api.tapd.cn",
		AuthType:   "basic",
		AuthHeader: "Authorization",
		AuthPrefix: "Basic",
		Whitelist:  `[{"method":"GET","path":"*"}]`,
		Enabled:    1,
		CreatedBy:  CreatedBySystem,
	})
	require.NoError(t, err)

	require.NoError(t, svc.DemoteDeprecatedSystemProviders(ctx))

	prov, err := svc.GetByName(ctx, "tapd")
	require.NoError(t, err)
	require.Equal(t, "user", prov.CreatedBy, "system TAPD row released to the user")
	// AuthType-driven single mode now (no hardcoded multi-mode for tapd).
	require.Equal(t, []string{"basic"}, prov.AuthModes())

	// Idempotent: a second run is a no-op and does not error.
	require.NoError(t, svc.DemoteDeprecatedSystemProviders(ctx))
	prov2, err := svc.GetByName(ctx, "tapd")
	require.NoError(t, err)
	require.Equal(t, "user", prov2.CreatedBy)
}

// TestTapdProviderSingleMode confirms a TAPD-named provider is single-mode
// (driven by auth_type), so basic and bearer are separate providers rather
// than one ambiguous dual-mode provider.
func TestTapdProviderSingleMode(t *testing.T) {
	bearer := ProviderDef{Name: "tapd", AuthType: "bearer"}
	require.Equal(t, []string{"bearer"}, bearer.AuthModes())

	basic := ProviderDef{Name: "tapd", AuthType: "basic"}
	require.Equal(t, []string{"basic"}, basic.AuthModes())
}
