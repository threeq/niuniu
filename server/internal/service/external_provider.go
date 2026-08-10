// Package service — ExternalProviderService manages provider definitions
// for external APIs (GitHub, TAPD, Linear, etc.) reachable via the
// call_external_api MCP tool.
//
// Each provider row stores the base URL, auth wiring (header name, prefix,
// type), an optional OpenAPI spec URL for auto-discovery, a JSON whitelist
// of curated (method, path-pattern) pairs (the "default safe set" used
// when the per-user write toggle is off), and a JSON profile (endpoint
// schemas) that AI can query at runtime via get_provider_schema.
//
// Whitelist matching is bundled here because it is a pure function of the
// provider row — no network, no DB — so the proxy service just delegates
// to IsAllowed. The proxy only invokes IsAllowed when the caller's
// per-user write_enabled toggle is off; once writes are enabled, every
// method/path passes and this matcher is bypassed entirely.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// ExternalProviderService manages external API provider definitions.
type ExternalProviderService struct {
	q  *store.Queries
	db *store.DB
	// onImapWritePrefChange, when set, is invoked (async) after the imap
	// provider's write-permission is toggled, so office-mail workspaces
	// reproject their config.toml/permissions.deny to the new write state.
	onImapWritePrefChange func(ctx context.Context, userID int64)
}

// SetImapWritePrefChangeHook wires a best-effort callback fired after the imap
// provider's write-permission toggle (SetWritePref). Used to reproject the
// user's office-mail workspaces so the toggle reaches the materialized files.
func (s *ExternalProviderService) SetImapWritePrefChangeHook(fn func(ctx context.Context, userID int64)) {
	s.onImapWritePrefChange = fn
}

// NewExternalProviderService returns a service wired to the sqlc queries and
// the driver-aware *store.DB wrapper. Constructor signature follows the
// niuniu convention (raw *sql.DB in, wrapped inside).
func NewExternalProviderService(q *store.Queries, rawDB *sql.DB) *ExternalProviderService {
	return &ExternalProviderService{q: q, db: store.Wrap(rawDB)}
}

// ProviderDef is the in-memory representation of a provider row.
type ProviderDef struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Label      string `json:"label"`
	APIBaseURL string `json:"api_base_url"`
	AuthType   string `json:"auth_type"`
	AuthHeader string `json:"auth_header"`
	AuthPrefix string `json:"auth_prefix"`
	Profile    string `json:"profile"`     // JSON schema of endpoints
	OpenAPIURL string `json:"openapi_url"` // OpenAPI spec URL
	Whitelist  string `json:"whitelist"`   // JSON array of WhitelistRule
	Enabled    bool   `json:"enabled"`
	CreatedBy  string `json:"created_by"`
}

// rowToProviderDef converts a sqlc row into the service-layer type.
func rowToProviderDef(r store.ExternalProvider) ProviderDef {
	return ProviderDef{
		ID:         r.ID,
		Name:       r.Name,
		Label:      r.Label,
		APIBaseURL: r.ApiBaseUrl,
		AuthType:   r.AuthType,
		AuthHeader: r.AuthHeader,
		AuthPrefix: r.AuthPrefix,
		Profile:    r.Profile,
		OpenAPIURL: r.OpenapiUrl,
		Whitelist:  r.Whitelist,
		Enabled:    r.Enabled == 1,
		CreatedBy:  r.CreatedBy,
	}
}

// AuthModes returns the list of credential auth modes the provider
// accepts. Modes are stable strings ("bearer", "basic", "custom_header")
// consumed by the SPA Credential dialog (to pick which field set to
// show) and by buildAuthHeader (to choose how to inject Authorization).
//
// Until external_providers grows a real `auth_modes` column, modes are
// derived statically: system providers declare what they support, and
// user-defined providers inherit a single-mode list from auth_type.
// Storing modes in the schema is the eventual home — see
// integrations spec follow-ups.
func (p *ProviderDef) AuthModes() []string {
	switch p.Name {
	case "github":
		return []string{"bearer"}
	case "jira":
		// Jira Cloud auth: email + api_token sent as Basic auth.
		return []string{"basic"}
	case "imap":
		// Mailbox connection fields (host/port/user/password/security) collected
		// for the office-mail scene; injected into MCP env via ${cred:...}, not
		// an HTTP auth header.
		return []string{"imap"}
	case "serp-api":
		// SerpAPI authenticates with an api_key carried as a URL query param;
		// the proxy injects it via buildAuthQuery (not an Authorization header).
		return []string{"query_param"}
	case "gsc":
		// Google Search Console uses an OAuth2 access token sent as a Bearer
		// header. The user pastes a token minted for the webmasters.readonly
		// scope; no OAuth dance happens inside niuniu.
		return []string{"bearer"}
	}
	// Everything else (incl. user-created TAPD providers) is single-mode,
	// driven by the provider's own AuthType. Keeping TAPD single-mode is
	// deliberate: a provider that advertised both basic+bearer made the
	// stored credential's effective scheme ambiguous. One auth scheme per
	// provider keeps it unambiguous.
	if p.AuthType == "" {
		return []string{"bearer"}
	}
	return []string{p.AuthType}
}

// Create inserts a new provider with sensible defaults. The caller should
// follow up with Update to set the whitelist, profile, and auth wiring once
// the provider has been configured.
func (s *ExternalProviderService) Create(ctx context.Context, name, label, apiBaseURL string) (*ProviderDef, error) {
	row, err := s.q.CreateProvider(ctx, store.CreateProviderParams{
		Name:       name,
		Label:      label,
		ApiBaseUrl: apiBaseURL,
		AuthType:   "bearer",
		AuthHeader: "Authorization",
		AuthPrefix: "Bearer",
		Profile:    "",
		OpenapiUrl: "",
		Whitelist:  `[{"method":"GET","path":"*"}]`, // default: read-only
		Enabled:    1,
		CreatedBy:  "user",
	})
	if err != nil {
		return nil, fmt.Errorf("create provider: %w", err)
	}
	d := rowToProviderDef(row)
	return &d, nil
}

// GetByName looks up a provider by its unique name (e.g. "github").
func (s *ExternalProviderService) GetByName(ctx context.Context, name string) (*ProviderDef, error) {
	row, err := s.q.GetProviderByName(ctx, name)
	if err != nil {
		return nil, err
	}
	d := rowToProviderDef(row)
	return &d, nil
}

// GetByID looks up a provider by its numeric primary key.
func (s *ExternalProviderService) GetByID(ctx context.Context, id int64) (*ProviderDef, error) {
	row, err := s.q.GetProviderByID(ctx, id)
	if err != nil {
		return nil, err
	}
	d := rowToProviderDef(row)
	return &d, nil
}

// List returns every provider row, ordered by name.
func (s *ExternalProviderService) List(ctx context.Context) ([]ProviderDef, error) {
	rows, err := s.q.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ProviderDef, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToProviderDef(r))
	}
	return out, nil
}

// Update overwrites the mutable fields on an existing provider row. Name is
// immutable — it is the stable identifier used by call_external_api.
func (s *ExternalProviderService) Update(ctx context.Context, id int64, label, apiBaseURL, authType, authHeader, authPrefix, whitelist, profile, openapiURL string, enabled bool) (*ProviderDef, error) {
	enabledInt := int64(0)
	if enabled {
		enabledInt = 1
	}
	row, err := s.q.UpdateProvider(ctx, store.UpdateProviderParams{
		ID:         id,
		Label:      label,
		ApiBaseUrl: apiBaseURL,
		AuthType:   authType,
		AuthHeader: authHeader,
		AuthPrefix: authPrefix,
		Profile:    profile,
		OpenapiUrl: openapiURL,
		Whitelist:  whitelist,
		Enabled:    enabledInt,
	})
	if err != nil {
		return nil, fmt.Errorf("update provider: %w", err)
	}
	d := rowToProviderDef(row)
	return &d, nil
}

// Delete removes a provider row by id. Callers should first verify that no
// credentials or project bindings reference this provider.
func (s *ExternalProviderService) Delete(ctx context.Context, id int64) error {
	return s.q.DeleteProvider(ctx, id)
}

// ListWritePrefs returns enabled flags for every (user_id, provider_id) pair
// the user has explicitly touched. Provider ids absent from the result map
// are treated as disabled by the proxy (writes default to off).
func (s *ExternalProviderService) ListWritePrefs(ctx context.Context, userID int64) (map[int64]bool, error) {
	rows, err := s.q.ListAPIWritePrefsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list write prefs: %w", err)
	}
	out := make(map[int64]bool, len(rows))
	for _, r := range rows {
		out[r.ProviderID] = r.Enabled == 1
	}
	return out, nil
}

// SetWritePref upserts a single (user_id, provider_id) row to the requested
// enabled state. The proxy reads this at call time to gate non-GET methods.
func (s *ExternalProviderService) SetWritePref(ctx context.Context, userID, providerID int64, enabled bool) error {
	v := int64(0)
	if enabled {
		v = 1
	}
	if err := s.q.UpsertAPIWritePrefs(ctx, store.UpsertAPIWritePrefsParams{
		UserID:     userID,
		ProviderID: providerID,
		Enabled:    v,
	}); err != nil {
		return fmt.Errorf("upsert write pref: %w", err)
	}
	// Toggling the imap provider's write-permission must reach office-mail's
	// materialized config.toml (outgoing/SMTP) + permissions.deny, which only
	// happens on reprojection. Fire the hook (async) when this is the imap
	// provider so the user's office-mail workspaces refresh automatically.
	if s.onImapWritePrefChange != nil {
		var name string
		if err := s.db.QueryRowContext(ctx,
			`SELECT name FROM external_providers WHERE id = ?`, providerID).Scan(&name); err == nil && name == imapProviderName {
			go s.onImapWritePrefChange(context.Background(), userID)
		}
	}
	return nil
}

// CreatedBySystem is the marker stored in external_providers.created_by for
// providers seeded by SeedSystem at server boot. Used by the proxy handler
// to reject Update/Delete on built-in providers — users may toggle their
// Enabled flag through Update but cannot rename, rewire, or remove them.
const CreatedBySystem = "system"

// systemSeed is the canonical list of built-in providers materialized by
// SeedSystem on first boot. Each entry covers a provider with a globally
// stable API root and well-known auth shape. Jira intentionally absent:
// each Atlassian instance has its own site URL, so jira providers are
// per-user and the SPA expects users to add them through the Provider UI.
var systemSeed = []ProviderDef{
	{
		Name:       "github",
		Label:      "GitHub",
		APIBaseURL: "https://api.github.com",
		AuthType:   "bearer",
		AuthHeader: "Authorization",
		AuthPrefix: "Bearer",
		Whitelist: `[` +
			`{"method":"GET","path":"*"},` +
			`{"method":"POST","path":"/repos/*/*/issues"},` +
			`{"method":"POST","path":"/repos/*/*/issues/*/comments"},` +
			`{"method":"PATCH","path":"/repos/*/*/issues/*"}` +
			`]`,
		Enabled:   true,
		CreatedBy: CreatedBySystem,
	},
	{
		// imap is a credential-type anchor, not an HTTP proxy provider: it gives
		// the credential dialog a built-in "邮箱 (IMAP)" option whose fields feed
		// the office-mail scene's ${cred:mailbox.*} placeholders. No api_base_url /
		// whitelist because it is never reached via call_external_api.
		Name:      imapProviderName,
		Label:     "邮箱 (IMAP)",
		AuthType:  "imap",
		Enabled:   true,
		CreatedBy: CreatedBySystem,
	},
	{
		// SerpAPI — a globally-stable SERP scraping API used by the GEO/SEO scene
		// for keyword-ranking data. Auth is an api_key query parameter, injected
		// server-side by the proxy (buildAuthQuery), so the agent never sees it.
		// SerpAPI is entirely GET, so the default safe set is read-only GET *.
		Name:       "serp-api",
		Label:      "SerpAPI",
		APIBaseURL: "https://serpapi.com",
		AuthType:   "query_param",
		AuthHeader: "api_key", // reused as the query-param name for query_param auth
		AuthPrefix: "",
		Whitelist:  `[{"method":"GET","path":"*"}]`,
		Profile:    serpAPIProfile,
		Enabled:    true,
		CreatedBy:  CreatedBySystem,
	},
	{
		// Google Search Console (Search Console API v3). Auth is an OAuth2 access
		// token sent as Bearer. searchAnalytics/query is a POST that only READS
		// data, so it is whitelisted alongside GET; genuine writes (sitemap
		// submit, etc.) still require the per-user "Allow writes" toggle.
		Name:       "gsc",
		Label:      "Google Search Console",
		APIBaseURL: "https://searchconsole.googleapis.com",
		AuthType:   "bearer",
		AuthHeader: "Authorization",
		AuthPrefix: "Bearer",
		Whitelist: `[` +
			`{"method":"GET","path":"*"},` +
			`{"method":"POST","path":"/webmasters/v3/sites/*/searchAnalytics/query"}` +
			`]`,
		Profile:   gscProfile,
		Enabled:   true,
		CreatedBy: CreatedBySystem,
	},
	// TAPD is intentionally NOT seeded. Its API accepts both Basic
	// (api_user:api_password) and Bearer (token) auth, and bundling both into
	// one immutable system provider made the auth scheme ambiguous and
	// unfixable (the base URL / auth wiring were locked). TAPD is now a
	// regular user-created provider: create one provider per auth scheme
	// (e.g. "TAPD (Bearer)" with auth_type=bearer, "TAPD (Basic)" with
	// auth_type=basic), each single-mode and fully editable. Existing locked
	// system rows are released to the user by DemoteDeprecatedSystemProviders.
}

// serpAPIProfile is the endpoint schema the agent fetches via get_provider_schema
// to learn how to drive SerpAPI through call_external_api. The api_key is
// injected by the proxy — the agent must NOT pass it.
const serpAPIProfile = `{
  "provider": "SerpAPI",
  "base_url": "https://serpapi.com",
  "auth": "api_key is injected automatically by the niuniu proxy as a query parameter — do NOT include it yourself.",
  "endpoints": [
    {
      "method": "GET",
      "path": "/search",
      "summary": "Run a search-engine query and get structured JSON results (organic_results, ads, related_questions, answer_box, ...).",
      "query_params": {
        "engine": "google | google_news | google_scholar | bing | baidu | duckduckgo | yahoo (default google)",
        "q": "the search query / keyword",
        "location": "geographic location name, e.g. 'Austin, Texas, United States'",
        "google_domain": "e.g. google.com",
        "gl": "country code, e.g. us, cn",
        "hl": "UI language, e.g. en, zh-cn",
        "num": "number of results (google max 100)",
        "start": "result offset for pagination",
        "device": "desktop | mobile | tablet"
      },
      "notes": "For keyword ranking, search q for the keyword then read the target domain's position from organic_results[].position. Output is JSON."
    }
  ]
}`

// gscProfile is the endpoint schema for Google Search Console (Search Console
// API v3). The OAuth2 Bearer token is injected by the proxy.
const gscProfile = `{
  "provider": "Google Search Console (Search Console API v3)",
  "base_url": "https://searchconsole.googleapis.com",
  "auth": "OAuth2 access token (Bearer) injected automatically by the niuniu proxy. Mint the token for scope https://www.googleapis.com/auth/webmasters.readonly on a Google account verified for the property.",
  "endpoints": [
    {
      "method": "GET",
      "path": "/webmasters/v3/sites",
      "summary": "List the verified properties (sites) the token can access."
    },
    {
      "method": "POST",
      "path": "/webmasters/v3/sites/{siteUrl}/searchAnalytics/query",
      "summary": "Query search analytics (clicks, impressions, ctr, position) for a property.",
      "notes": "siteUrl must be URL-encoded, e.g. https%3A%2F%2Fexample.com%2F or sc-domain%3Aexample.com. Body: {\"startDate\":\"2026-06-01\",\"endDate\":\"2026-06-30\",\"dimensions\":[\"query\"|\"page\"|\"date\"|\"country\"|\"device\"],\"rowLimit\":25000}. Returns rows[] each with keys[] + clicks/impressions/ctr/position."
    }
  ]
}`

// deprecatedSystemProviders lists provider names that USED to be system-seeded
// but no longer are. On boot, DemoteDeprecatedSystemProviders releases any
// still-locked system row for these names back to the user.
var deprecatedSystemProviders = []string{"tapd"}

// DemoteDeprecatedSystemProviders converts any provider in
// deprecatedSystemProviders that still carries created_by='system' into a
// regular user-owned provider (created_by='user'), so it becomes editable and
// deletable (the proxy*Provider handlers block all writes/deletes on system
// rows). Idempotent. Credential and external-source bindings are untouched --
// they reference the provider by id/name, both unchanged.
func (s *ExternalProviderService) DemoteDeprecatedSystemProviders(ctx context.Context) error {
	for _, name := range deprecatedSystemProviders {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE external_providers SET created_by = 'user' WHERE name = ? AND created_by = ?`,
			name, CreatedBySystem); err != nil {
			return fmt.Errorf("demote deprecated system provider %q: %w", name, err)
		}
	}
	return nil
}

// SeedSystem ensures the built-in providers exist. Idempotent: an existing row
// (any created_by) is left alone so user customizations stick across restarts.
// Safe to call on every boot, including before the table is populated.
//
// Returns the number of new rows inserted. Errors propagate so the server boot
// path can decide whether to fail fast.
func (s *ExternalProviderService) SeedSystem(ctx context.Context) (int, error) {
	inserted := 0
	for _, seed := range systemSeed {
		if _, err := s.q.GetProviderByName(ctx, seed.Name); err == nil {
			continue // already exists — leave it alone
		} else if !errors.Is(err, sql.ErrNoRows) {
			return inserted, fmt.Errorf("lookup %s: %w", seed.Name, err)
		}
		enabled := int64(0)
		if seed.Enabled {
			enabled = 1
		}
		if _, err := s.q.CreateProvider(ctx, store.CreateProviderParams{
			Name:       seed.Name,
			Label:      seed.Label,
			ApiBaseUrl: seed.APIBaseURL,
			AuthType:   seed.AuthType,
			AuthHeader: seed.AuthHeader,
			AuthPrefix: seed.AuthPrefix,
			Profile:    seed.Profile,
			OpenapiUrl: seed.OpenAPIURL,
			Whitelist:  seed.Whitelist,
			Enabled:    enabled,
			CreatedBy:  seed.CreatedBy,
		}); err != nil {
			return inserted, fmt.Errorf("seed %s: %w", seed.Name, err)
		}
		inserted++
	}
	return inserted, nil
}

// WhitelistRule represents a single whitelist entry.
type WhitelistRule struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// IsAllowed checks if a method+path matches the provider's curated
// whitelist. The proxy only consults this when the caller's per-user
// write toggle is OFF — once the user opts in, every method/path passes
// regardless of whitelist content. Empty whitelist defaults to GET-only;
// malformed JSON is treated as deny-all.
func (s *ExternalProviderService) IsAllowed(whitelistJSON string, method, path string) bool {
	if whitelistJSON == "" {
		return strings.EqualFold(method, "GET")
	}
	var rules []WhitelistRule
	if err := json.Unmarshal([]byte(whitelistJSON), &rules); err != nil {
		return false
	}
	for _, rule := range rules {
		if !methodMatches(rule.Method, method) {
			continue
		}
		if pathMatches(rule.Path, path) {
			return true
		}
	}
	return false
}

// methodMatches: "*" matches any method; otherwise case-insensitive equality.
func methodMatches(ruleMethod, actualMethod string) bool {
	if ruleMethod == "*" {
		return true
	}
	return strings.EqualFold(ruleMethod, actualMethod)
}

// pathMatches does simple glob matching: `*` matches any sequence within
// a single path segment. Intentionally simpler than filepath.Match — it
// is designed for URL paths, not filesystem paths, and treats * as
// "match any non-empty sequence within one segment". Don't replace with
// stdlib path/filepath helpers; their slash semantics aren't ours.
// Examples:
//
//	"*"                  matches everything
//	"/repos/*/*/issues"  matches /repos/owner/repo/issues
//	"/repos/*"           matches /repos/foo but NOT /repos/foo/bar
func pathMatches(pattern, actual string) bool {
	if pattern == "*" {
		return true
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == actual
	}
	pos := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		idx := strings.Index(actual[pos:], part)
		if idx < 0 {
			return false
		}
		pos += idx + len(part)
		if i == 0 && !strings.HasPrefix(pattern, "*") && idx > 0 {
			return false
		}
	}
	if strings.HasSuffix(pattern, "*") {
		if strings.Contains(actual[pos:], "/") {
			return false
		}
		return true
	}
	if pos < len(actual) {
		return false
	}
	return true
}
