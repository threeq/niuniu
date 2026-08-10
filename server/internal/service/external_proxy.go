// Package service — ExternalProxyService handles proxying HTTP requests to
// external APIs on behalf of AI (via the call_external_api MCP tool).
//
// The proxy is a security boundary with a single per-user gate per provider:
//   - `enabled` (admin) — provider on/off
//   - `write_enabled` (per-user) OFF — only the provider's curated whitelist
//     (typically `[{GET,*}]`) passes; non-whitelist calls return 403.
//   - `write_enabled` (per-user) ON — every method/path passes; the
//     whitelist is bypassed entirely.
//
// See the 2026-05-27 permission redesign: users see ONE toggle per provider;
// the whitelist is no longer something they edit directly.
//
// The service does NOT parse or interpret the response body — it returns
// raw JSON to the AI, which is responsible for understanding the external
// API's schema.
package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Sentinel errors from resolveCredential so handlers can map them to the
// right HTTP status (instead of every resolution failure falling into 502
// Bad Gateway, which makes the agent misread it as an upstream fault and
// retry). Wrapped with %w so errors.Is catches them through the fmt.Errorf
// chain that preserves the provider name + human-readable text.
var (
	ErrNoProjectSource    = errors.New("no external source configured in this project for the provider")
	ErrAmbiguousSource    = errors.New("multiple external sources for the provider; specify source_key")
	ErrSourceNoCredential = errors.New("external source has no credential bound")
)

// AmbiguousSourceError is returned when the call cannot be pinned to a single
// project binding for the provider — either because the project binds several
// sources and the caller omitted source_key, or because the supplied source_key
// matches none of them. It carries the available source_keys so the agent can
// retry with a valid one instead of dead-ending on a bare "specify source_key"
// with no way to discover the options (the exact wall a TAPD agent hits when a
// project binds two workspaces for the same credential).
//
// It Unwraps to ErrAmbiguousSource so the handler's existing
// errors.Is(err, ErrAmbiguousSource) -> 400 AMBIGUOUS_SOURCE mapping keeps
// firing unchanged.
type AmbiguousSourceError struct {
	Provider      string   // provider name, e.g. "tapd_personal"
	RequestedKey  string   // the source_key the caller supplied, "" when none
	AvailableKeys []string // every source_key bound for (project, provider)
}

func (e *AmbiguousSourceError) Error() string {
	keys := strings.Join(e.AvailableKeys, ", ")
	if e.RequestedKey != "" {
		return fmt.Sprintf("source_key %q does not match any %s source in this project; available source_key: %s",
			e.RequestedKey, e.Provider, keys)
	}
	return fmt.Sprintf("multiple %s sources in this project; specify source_key. available source_key: %s",
		e.Provider, keys)
}

func (e *AmbiguousSourceError) Unwrap() error { return ErrAmbiguousSource }

// ExternalProxyService handles proxying HTTP requests to external APIs.
type ExternalProxyService struct {
	q           *store.Queries
	db          *store.DB
	providerSvc *ExternalProviderService
	credSvc     *ExternalCredentialService
	authz       *Authz
	client      *http.Client
}

// NewExternalProxyService wires the proxy with its dependencies.
func NewExternalProxyService(q *store.Queries, rawDB *sql.DB, providerSvc *ExternalProviderService, credSvc *ExternalCredentialService, authz *Authz) *ExternalProxyService {
	return &ExternalProxyService{
		q:           q,
		db:          store.Wrap(rawDB),
		providerSvc: providerSvc,
		credSvc:     credSvc,
		authz:       authz,
		// SSRF guard: reuse the KB downloader's validating dialer so a
		// user-supplied api_base_url can't reach loopback/private/link-local/
		// cloud-metadata (169.254.169.254) addresses, and DNS rebinding + proxy
		// bypass are closed. Redirect hops re-dial through the same hook.
		client: NewSSRFGuardedClient(30*time.Second, 30*time.Second),
	}
}

// ProxyInput is the request to call_external_api.
type ProxyInput struct {
	UserID      int64
	Provider    string
	Method      string
	Path        string
	Query       map[string]any
	Body        map[string]any
	WorkspaceID int64  // >0 when called by an in-workspace MCP agent; resolve credential by project binding
	SourceKey   string // optional disambiguator when a project binds multiple sources for the same provider
}

// ProxyOutput is the response from call_external_api.
type ProxyOutput struct {
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Body      json.RawMessage   `json:"body"`
	AuthDebug *AuthDebug        `json:"auth_debug,omitempty"`
}

// AuthDebug is a token-safe diagnostic attached ONLY to failed (>=400) proxy
// responses, so an operator (or the agent) can see exactly which auth scheme
// and credential keys were used to authenticate a request that the upstream
// rejected -- without ever exposing the secret. This turns opaque 401/403/422
// auth failures into something diagnosable (e.g. "scheme=Basic" when Bearer
// was expected, or "token_len=41" revealing trailing whitespace).
type AuthDebug struct {
	Scheme           string   `json:"scheme"`     // "Bearer" / "Basic" / "" (no auth)
	AuthMode         string   `json:"auth_mode"`  // stored credential auth_mode (may be empty)
	TokenLen         int      `json:"token_len"`  // length of the encoded auth payload that was sent
	TokenMask        string   `json:"token_mask"` // first3...last3 of the payload (never the full value)
	CredKeys         []string `json:"cred_keys"`  // sorted credential config keys (no values)
	ProviderAuthType string   `json:"provider_auth_type"`
	BaseURL          string   `json:"base_url"`
}

// Call resolves the provider + credential, checks authz, builds and executes
// the HTTP request, records an audit log, and returns the raw response to
// the AI.
func (s *ExternalProxyService) Call(ctx context.Context, in ProxyInput) (*ProxyOutput, error) {
	// 1. Resolve provider
	prov, err := s.providerSvc.GetByName(ctx, in.Provider)
	if err != nil {
		return nil, fmt.Errorf("provider %q not found: %w", in.Provider, err)
	}
	if !prov.Enabled {
		return nil, fmt.Errorf("provider %q is disabled", in.Provider)
	}

	// 2. Single per-user gate: write_enabled ON bypasses everything;
	// write_enabled OFF falls back to the provider's curated whitelist
	// (usually GET *), so reads keep working without any opt-in. Failing
	// closed on DB errors (missing-row is the normal first-time case) but
	// logging the unexpected ones so operators can spot a broken store.
	prefs, err := s.q.GetAPIWritePrefs(ctx, store.GetAPIWritePrefsParams{
		UserID:     in.UserID,
		ProviderID: prov.ID,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("external proxy write-prefs lookup failed; defaulting to write_enabled=false",
			"provider", in.Provider, "user_id", in.UserID, "error", err)
	}
	writeEnabled := err == nil && prefs.Enabled == 1
	if !s.gateAllows(writeEnabled, prov.Whitelist, in.Method, in.Path) {
		return nil, &ProxyError{
			Code:     "write_disabled",
			Message:  fmt.Sprintf("Operation %s %s on %s is not in the default safe set. Enable \"Allow writes\" for this provider in Settings -> Integrations.", in.Method, in.Path, in.Provider),
			Provider: in.Provider,
		}
	}

	// 3. Resolve the credential. In-workspace MCP agents resolve via the
	// project binding (so any member's agent uses the bound credential);
	// SPA direct calls fall back to the caller's own. See resolveCredential.
	cred, err := s.resolveCredential(ctx, in)
	if err != nil {
		return nil, err
	}

	// 4. Build URL. For query-param-auth providers (e.g. SerpAPI's ?api_key=)
	// the credential secret is injected into the query string here — server
	// side — so the agent never supplies or even sees it; it only names the
	// endpoint and the non-secret params. Header-auth providers are untouched.
	baseURL := strings.TrimRight(prov.APIBaseURL, "/")
	fullURL := baseURL + "/" + strings.TrimLeft(in.Path, "/")
	authQueryName, authQueryVal := buildAuthQuery(prov, cred.Config)
	q := url.Values{}
	for k, v := range in.Query {
		q.Set(k, fmt.Sprintf("%v", v))
	}
	if authQueryName != "" {
		q.Set(authQueryName, authQueryVal)
	}
	if len(q) > 0 {
		fullURL += "?" + q.Encode()
	}

	// 5. Build request
	var bodyReader io.Reader
	if in.Body != nil {
		bodyBytes, err := json.Marshal(in.Body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(in.Method), fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	// 6. Inject auth — emit a structured diagnostic on every call. The
	// log is intentionally token-safe: only the auth scheme prefix and a
	// short prefix/suffix mask of the encoded value are recorded, never
	// the full token. Drop the log line once the integration matures.
	authValue := buildAuthHeader(prov, cred.Config)
	mode, _ := cred.Config["auth_mode"].(string)
	cfgKeys := credentialConfigKeys(cred.Config)
	authScheme, authPayload := splitAuth(authValue)
	if authValue != "" {
		req.Header.Set(prov.AuthHeader, authValue)
		slog.Info("proxy auth injected",
			"provider", in.Provider,
			"user_id", in.UserID,
			"auth_mode", mode,
			"header", prov.AuthHeader,
			"scheme", authScheme,
			"payload_mask", maskToken(authPayload),
			"payload_len", len(authPayload),
			"cred_keys", cfgKeys,
		)
	} else if authQueryName != "" {
		// Secret delivered via the URL query string (query_param providers like
		// SerpAPI), not a header — expected, so this is not the empty-header
		// warning path. Token-safe: only the param name + a short mask.
		slog.Info("proxy auth injected via query param",
			"provider", in.Provider,
			"user_id", in.UserID,
			"auth_mode", mode,
			"param", authQueryName,
			"payload_mask", maskToken(authQueryVal),
			"payload_len", len(authQueryVal),
			"cred_keys", cfgKeys,
		)
	} else {
		slog.Warn("proxy auth NOT injected — credential produced empty header",
			"provider", in.Provider,
			"user_id", in.UserID,
			"auth_mode", mode,
			"cred_keys", cfgKeys,
			"provider_auth_type", prov.AuthType,
		)
	}
	if in.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	// 7. Execute
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("proxy request failed: %w", err)
	}
	defer resp.Body.Close()
	const maxBody = 10 << 20 // 10 MB cap
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(respBody) > maxBody {
		return nil, fmt.Errorf("response body exceeds 10 MB limit")
	}

	// 8. Record audit
	statusCode := int64(resp.StatusCode)
	if err := s.q.CreateAuditLog(ctx, store.CreateAuditLogParams{
		UserID:     in.UserID,
		ProviderID: prov.ID,
		Method:     strings.ToUpper(in.Method),
		Path:       in.Path,
		StatusCode: statusCode,
	}); err != nil {
		slog.Warn("external proxy audit log write failed", "error", err)
	}

	// 9. Collect headers
	headers := make(map[string]string)
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}

	// On an upstream failure, attach a token-safe auth diagnostic so the
	// caller can see what scheme/keys were used without the secret.
	var authDebug *AuthDebug
	if resp.StatusCode >= 400 {
		authDebug = &AuthDebug{
			Scheme:           authScheme,
			AuthMode:         strings.ToLower(strings.TrimSpace(mode)),
			TokenLen:         len(authPayload),
			TokenMask:        maskToken(authPayload),
			CredKeys:         cfgKeys,
			ProviderAuthType: prov.AuthType,
			BaseURL:          baseURL,
		}
	}

	return &ProxyOutput{
		Status:    resp.StatusCode,
		Headers:   headers,
		Body:      json.RawMessage(respBody),
		AuthDebug: authDebug,
	}, nil
}

// resolveCredential picks the credential to use for this call.
//
// In-workspace MCP agents pass WorkspaceID>0: resolve via the PROJECT BINDING
// (workspace -> project -> project_external_sources.credential_id) and load it
// with GetBoundByID (server-side decrypt, no per-caller ownership check). This
// lets any project member's agent use the one credential bound to the source —
// fixing the cross-member 401 — without ever exposing plaintext.
//
// SPA direct calls (WorkspaceID==0, settings self-test) keep the legacy
// behaviour: the caller's own credential for that provider.
func (s *ExternalProxyService) resolveCredential(ctx context.Context, in ProxyInput) (*ExternalCredentialDecoded, error) {
	if in.WorkspaceID > 0 {
		projectID, err := s.q.GetProjectIDForWorkspace(ctx, in.WorkspaceID)
		if err != nil {
			return nil, fmt.Errorf("resolve project for workspace %d: %w", in.WorkspaceID, err)
		}
		// AuthZ (spec 规约5 + IDOR hardening): the caller must be able to access
		// the project whose bound credential we're about to use server-side.
		// WorkspaceID itself is server-derived from the MCP session token, but we
		// still verify project access so a revoked/cross-project identity can't
		// drive another project's credential. userID==0 only occurs for fully
		// autonomous starts already proven by the workspace-bound token, so the
		// token->workspace->project chain is the authorization there.
		if in.UserID > 0 {
			if _, err := s.authz.CanAccessProject(ctx, in.UserID, projectID); err != nil {
				return nil, err
			}
		}
		rows, err := s.db.QueryContext(ctx,
			`SELECT credential_id, source_key FROM project_external_sources
			 WHERE project_id = ? AND provider = ?`, projectID, in.Provider)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		type binding struct {
			credID    sql.NullInt64
			sourceKey string
		}
		var all []binding
		for rows.Next() {
			var b binding
			if err := rows.Scan(&b.credID, &b.sourceKey); err != nil {
				return nil, err
			}
			all = append(all, b)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		// Filter to the requested source_key (if any) while collecting every
		// available key, so an ambiguous or unmatched result can tell the agent
		// exactly which source_keys exist instead of a dead-end.
		var matches []binding
		availableKeys := make([]string, 0, len(all))
		for _, b := range all {
			availableKeys = append(availableKeys, b.sourceKey)
			if in.SourceKey != "" && b.sourceKey != in.SourceKey {
				continue
			}
			matches = append(matches, b)
		}
		switch {
		case len(all) == 0:
			// The provider isn't bound to this project at all.
			return nil, fmt.Errorf("no %s source configured in this project: %w", in.Provider, ErrNoProjectSource)
		case len(matches) == 0:
			// A source_key was supplied but matched none of the bound sources:
			// a fixable client mistake, not a missing-source condition. List
			// the valid keys so the agent can correct itself.
			return nil, &AmbiguousSourceError{Provider: in.Provider, RequestedKey: in.SourceKey, AvailableKeys: availableKeys}
		case len(matches) > 1:
			return nil, &AmbiguousSourceError{Provider: in.Provider, AvailableKeys: availableKeys}
		}
		if !matches[0].credID.Valid {
			return nil, fmt.Errorf("%s source has no credential bound: %w", in.Provider, ErrSourceNoCredential)
		}
		cred, err := s.credSvc.GetBoundByID(ctx, matches[0].credID.Int64)
		if err != nil {
			return nil, err
		}
		// Defense in depth: the bound credential must actually belong to the
		// provider we're proxying for. A drifted binding (e.g. a credential
		// re-typed to another provider after binding) must not leak across.
		if cred.Provider != integration.ProviderName(in.Provider) {
			return nil, ErrCredentialProviderMismatch
		}
		return cred, nil
	}
	// SPA fallback: caller's own credential.
	creds, err := s.credSvc.ListByProvider(ctx, "user", in.UserID, in.UserID, integration.ProviderName(in.Provider))
	if err != nil {
		return nil, fmt.Errorf("list credentials for %s: %w", in.Provider, err)
	}
	if len(creds) == 0 {
		return nil, fmt.Errorf("no credential configured for %s", in.Provider)
	}
	return s.credSvc.GetByID(ctx, creds[0].ID, "user", in.UserID, in.UserID)
}

// gateAllows is the single permission predicate. Pure function so it can
// be exercised by unit tests without a DB — the Call() boolean wiring
// (writeEnabled = err==nil && prefs.Enabled==1) is the only piece left
// untested at the integration boundary, and it's deliberately trivial.
//
//   - writeEnabled = true  -> always allow
//   - writeEnabled = false -> fall back to the curated whitelist
//
// Don't fold this back inline. The next person who edits Call() WILL
// flip the boolean by accident otherwise (see code review 2026-05-27).
func (s *ExternalProxyService) gateAllows(writeEnabled bool, whitelistJSON, method, path string) bool {
	if writeEnabled {
		return true
	}
	return s.providerSvc.IsAllowed(whitelistJSON, method, path)
}

// ProxyError is returned for authz/whitelist failures before the HTTP call.
// Handlers should map these to appropriate HTTP status codes (typically 403).
type ProxyError struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Provider string `json:"provider"`
}

func (e *ProxyError) Error() string {
	return e.Message
}

// buildAuthHeader composes the Authorization header value for the proxy
// request. The credential's config carries an explicit `auth_mode`
// (since the per-provider auth-modes refactor); buildAuthHeader switches
// on that mode. Recognised modes:
//
//   - "bearer"        -> "<prefix> <token>"        (prefix defaults to "Bearer")
//   - "basic"         -> "<prefix> base64(user:pass)" (prefix defaults to "Basic")
//   - "custom_header" -> "<prefix> <token>" or raw <token> when prefix empty
//
// When `auth_mode` is absent (legacy credentials saved before this
// refactor), buildAuthHeader falls back to provider.AuthType + a
// best-effort key detection so existing rows keep working without a
// re-save. Returns "" when no usable credential material is present.
func buildAuthHeader(prov *ProviderDef, cfg map[string]any) string {
	// Query-param providers (e.g. SerpAPI) carry their secret in the URL query,
	// not an HTTP header — buildAuthQuery handles those. Bail here so a lone
	// api_key isn't ALSO emitted as a stray "Bearer <api_key>" header by the
	// legacy shape-detection below.
	if usesQueryParamAuth(prov, cfg) {
		return ""
	}
	mode, _ := cfg["auth_mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = legacyDetectMode(prov, cfg)
	}
	// Basic auth REQUIRES a user:password pair. If basic was selected or
	// inferred but the credential carries only a lone token (no pair), basic
	// is impossible -- base64(<token>) is not user:password, and the API
	// rejects it (TAPD answers `422 The access token provided is invalid`).
	// A single token can only authenticate as a Bearer token, so fall through
	// to bearer instead of emitting a guaranteed-bad `Basic <token>` header.
	if mode == "basic" && !hasBasicPair(cfg) && credToken(cfg) != "" {
		mode = "bearer"
	}
	switch mode {
	case "bearer", "oauth", "pat":
		// Bearer scheme always wins over provider.AuthPrefix. Without
		// this override, a TAPD credential saved in PAT mode would be
		// emitted as "Basic <raw_pat>" (TAPD's provider.AuthPrefix is
		// "Basic" by default) — malformed Basic auth, rejected with a
		// stock 401 + WWW-Authenticate: Basic realm='TAPD API'. PAT in
		// TAPD's open API is sent as Authorization: Bearer <pat>; the
		// old TAPD adapter (integration/tapd, removed in e686250e^)
		// did the same.
		return injectTokenWithPrefix(cfg, "Bearer")
	case "basic":
		return injectBasic(prov, cfg)
	case "custom_header":
		// Custom header schemes typically don't carry a scheme prefix
		// (e.g. "X-Api-Key: <token>"). prov.AuthPrefix is the override
		// hook for the rare provider that does (e.g. "Token <key>").
		prefix := prov.AuthPrefix
		return injectTokenWithPrefix(cfg, prefix)
	}
	return ""
}

// usesQueryParamAuth reports whether the effective auth mode is query_param —
// the credential secret rides in the URL query string (e.g. SerpAPI's
// ?api_key=...) rather than an Authorization header. The mode is taken from the
// credential's explicit auth_mode, falling back to the provider's AuthType for
// legacy credentials saved without a mode.
func usesQueryParamAuth(prov *ProviderDef, cfg map[string]any) bool {
	mode, _ := cfg["auth_mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(prov.AuthType))
	}
	return mode == "query_param"
}

// buildAuthQuery returns the (name, value) pair to inject into the request's
// URL query string for query_param providers, or ("","") when the
// provider/credential does not use query-param auth. Keeping the secret here —
// server-side, at URL-build time — means the agent never supplies or sees the
// api_key.
//
// The query-param NAME is the provider's AuthHeader field, reused as the param
// key for query_param providers (defaults to "api_key" when blank); the value
// is read from the credential's token/access_token/api_key aliases via
// credToken.
func buildAuthQuery(prov *ProviderDef, cfg map[string]any) (string, string) {
	if !usesQueryParamAuth(prov, cfg) {
		return "", ""
	}
	tok := credToken(cfg)
	if tok == "" {
		return "", ""
	}
	name := strings.TrimSpace(prov.AuthHeader)
	if name == "" {
		name = "api_key"
	}
	return name, tok
}

// injectTokenWithPrefix returns "<prefix> <token>" — or just "<token>"
// when prefix is empty. Token is read from token/access_token/api_key
// (in priority order) so credentials saved with any of those key names
// — explicit-mode or legacy — keep working.
func injectTokenWithPrefix(cfg map[string]any, prefix string) string {
	tok := credToken(cfg) // TrimSpace'd
	if tok == "" {
		return ""
	}
	if prefix == "" {
		return tok
	}
	return prefix + " " + tok
}

// injectBasic emits "<prefix> base64(user:pass)" — Basic auth. We accept
// both api_user/api_password (TAPD's historical names) and user/password
// (generic dialog naming) so a credential saved in either shape works.
func injectBasic(prov *ProviderDef, cfg map[string]any) string {
	user := strings.TrimSpace(firstNonEmpty(
		strFromCfg(cfg, "user"),
		strFromCfg(cfg, "api_user"),
		strFromCfg(cfg, "username"),
	))
	pass := strings.TrimSpace(firstNonEmpty(
		strFromCfg(cfg, "password"),
		strFromCfg(cfg, "api_password"),
	))
	if user == "" && pass == "" {
		// Fall back to a pre-encoded token under "token" (some users
		// paste the full base64 themselves).
		if tok := strFromCfg(cfg, "token"); tok != "" {
			prefix := prov.AuthPrefix
			if prefix == "" {
				prefix = "Basic"
			}
			return prefix + " " + tok
		}
		return ""
	}
	creds := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	prefix := prov.AuthPrefix
	if prefix == "" {
		prefix = "Basic"
	}
	return prefix + " " + creds
}

func strFromCfg(cfg map[string]any, key string) string {
	v, _ := cfg[key].(string)
	return v
}

// legacyDetectMode is the back-compat path: when a credential has no explicit
// auth_mode (saved before the multi-mode refactor), infer the mode from the
// credential SHAPE first, falling back to the provider's declared AuthType
// only when the shape is ambiguous.
//
// Shape wins because it is unambiguous: a complete user:password pair can only
// be basic; a lone token can only be bearer. Deferring to provider.AuthType
// (the old behavior) mis-sent lone-token credentials as Basic under a
// basic-typed provider like TAPD -- exactly the `Basic <raw-token>` that TAPD
// rejects with 422. The buildAuthHeader override above is the second guard for
// the case where auth_mode is explicitly (and wrongly) "basic".
func legacyDetectMode(prov *ProviderDef, cfg map[string]any) string {
	if hasBasicPair(cfg) {
		return "basic"
	}
	if credToken(cfg) != "" {
		return "bearer"
	}
	switch strings.ToLower(prov.AuthType) {
	case "basic":
		return "basic"
	case "custom_header":
		return "custom_header"
	}
	return "bearer"
}

// hasBasicPair reports whether cfg carries a usable Basic-auth user+password
// pair (under any of the accepted key aliases: user/api_user/username +
// password/api_password).
func hasBasicPair(cfg map[string]any) bool {
	user := strings.TrimSpace(firstNonEmpty(
		strFromCfg(cfg, "user"),
		strFromCfg(cfg, "api_user"),
		strFromCfg(cfg, "username"),
	))
	pass := strings.TrimSpace(firstNonEmpty(
		strFromCfg(cfg, "password"),
		strFromCfg(cfg, "api_password"),
	))
	return user != "" && pass != ""
}

// credToken returns the single-token secret (bearer / PAT / api-key shape)
// under any accepted key alias, or "" when none is present. The value is
// TrimSpace'd: a pasted token frequently carries a trailing newline/space,
// and `Bearer <token>\n` is rejected as an invalid token (TAPD: 422) even
// though the same token works when pasted cleanly into curl.
func credToken(cfg map[string]any) string {
	return strings.TrimSpace(firstNonEmpty(
		strFromCfg(cfg, "token"),
		strFromCfg(cfg, "access_token"),
		strFromCfg(cfg, "api_key"),
	))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// splitAuth splits an Authorization header value into ("scheme", "payload"),
// e.g. "Bearer ghp_abc" -> ("Bearer", "ghp_abc"). For headers without a
// space (e.g. custom_header raw token) returns ("", value).
func splitAuth(v string) (scheme, payload string) {
	if i := strings.IndexByte(v, ' '); i > 0 {
		return v[:i], v[i+1:]
	}
	return "", v
}

// maskToken keeps the first 3 + last 3 chars of a credential payload and
// elides the middle. Short payloads collapse to "***" so we never leak
// even fragmentary content. Diagnostic-only — never to be relied on for
// security.
func maskToken(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:3] + "..." + s[len(s)-3:]
}

// credentialConfigKeys returns the sorted key set of a decrypted
// credential config, with values omitted. Used by the diagnostic log so
// operators can tell which fields the SPA actually saved (e.g.
// "[auth_mode, password, user]" vs the broken "[api_user]") without
// leaking secrets.
func credentialConfigKeys(cfg map[string]any) []string {
	if len(cfg) == 0 {
		return nil
	}
	out := make([]string, 0, len(cfg))
	for k := range cfg {
		out = append(out, k)
	}
	// Stable order so the log line diffs cleanly.
	sortStrings(out)
	return out
}

// sortStrings is std sort.Strings inlined — avoiding the sort import
// here keeps the file's import block tight; the slice is tiny.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
