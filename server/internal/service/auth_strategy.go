// Package service — pluggable outbound auth strategies (issue #737).
//
// The external proxy injects credentials at egress. Historically that was a
// single function (buildAuthHeader) covering the static schemes. This file
// generalises it into a small strategy family so a provider contract can name
// a strategy — `auth_strategy: oauth2_refresh | sigv4 | hmac | mtls | …` —
// while the implementation and every parameter/secret stay server-side.
//
// Design rules:
//   - Secrets live only in the encrypted credential config; the agent never
//     sees or supplies them. A strategy only writes into the outbound request
//     (headers/query) or signs it, and reports back a masked diagnostic.
//   - The contract declares the strategy NAME only; the parameters
//     (token_url, client_secret, secret_key, region, …) ride with the
//     credential and are encrypted at rest by the integration keyring.
//   - An unrecognised strategy name falls back to the static path, so a
//     provider that declares nothing keeps the exact pre-#737 behaviour.
package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrAuthStrategy is the sentinel wrapped by AuthStrategyError so handlers can
// map a strategy/config fault to 403 (a fixable integration config problem)
// rather than 502 (which the agent would misread as a transient upstream fault
// and retry).
var ErrAuthStrategy = errors.New("auth strategy failed")

// AuthStrategyError reports that a strategy could not authenticate the request:
// a missing/invalid credential parameter, a token-endpoint failure, or a
// signing error. Message is agent-facing and never carries a secret.
type AuthStrategyError struct {
	Strategy string
	Message  string
	Err      error
}

func (e *AuthStrategyError) Error() string {
	if e.Err != nil {
		return "auth strategy " + e.Strategy + ": " + e.Message + ": " + e.Err.Error()
	}
	return "auth strategy " + e.Strategy + ": " + e.Message
}

func (e *AuthStrategyError) Unwrap() error { return ErrAuthStrategy }

// AuthRequest is the outbound request a strategy authenticates. It is a thin,
// mutable view over the request the proxy is about to send: a strategy sets
// headers, rewrites the query, and (for signature schemes) reads the exact
// body bytes that will be sent.
type AuthRequest struct {
	Method   string
	URL      *url.URL
	Header   http.Header
	Body     []byte // exact bytes to be sent (nil when bodyless)
	Provider *ProviderDef
	Config   map[string]any // decrypted credential config
	// Client is a network-capable client for strategies that need I/O
	// (OAuth2 token exchange). It is the proxy's SSRF-guarded client.
	Client *http.Client
	// CredKey identifies the credential for per-credential caching (OAuth2
	// access tokens). Set by the proxy; stable across calls.
	CredKey string
	// ForceRefresh tells a caching strategy to bypass its cache (used by the
	// proxy's single retry after a 401).
	ForceRefresh bool
}

// AuthOutcome is the token-safe record of what a strategy did, for the
// structured log line and the AuthDebug block attached to failed responses.
type AuthOutcome struct {
	Strategy   string // strategy name that ran
	Scheme     string // auth scheme prefix, e.g. "Bearer"
	HeaderName string // header the value was written to
	Payload    string // injected secret (for masking only — never returned raw)
	QueryName  string // query-param name for query-param strategies
	QueryValue string // query-param value (for masking only)
	MTLS       bool   // strategy authenticates via a client certificate
}

// AuthStrategy authenticates one outbound proxy request.
type AuthStrategy interface {
	Name() string
	Apply(ctx context.Context, req *AuthRequest) (*AuthOutcome, error)
}

// AuthClientProvider is an optional interface a strategy implements when it
// needs a *different* http.Client than the proxy default — currently only
// mTLS, which must present a client certificate during the TLS handshake.
type AuthClientProvider interface {
	Client(base *http.Client, cfg map[string]any) (*http.Client, error)
}

// authStrategyFactory builds the strategy for a normalised name. It returns
// ok=false for an unknown name so the caller can fall back to static.
func authStrategyFactory(name string, cache *oauthTokenCache) (AuthStrategy, bool) {
	switch normalizeStrategyName(name) {
	case "", strategyStatic:
		return staticAuthStrategy{}, true
	case strategyOAuth2:
		return &oauth2AuthStrategy{cache: cache}, true
	case strategyHMAC:
		return hmacAuthStrategy{}, true
	case strategySigV4:
		return sigV4AuthStrategy{}, true
	case strategyMTLS:
		return mtlsAuthStrategy{}, true
	}
	return nil, false
}

// Canonical strategy identifiers.
const (
	strategyStatic = "static"
	strategyOAuth2 = "oauth2"
	strategyHMAC   = "hmac"
	strategySigV4  = "sigv4"
	strategyMTLS   = "mtls"
)

// normalizeStrategyName maps every accepted spelling of a strategy onto one of
// the canonical identifiers. Static schemes collapse to strategyStatic.
func normalizeStrategyName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "bearer", "basic", "custom_header", "pat", "oauth", "query_param", "imap", "static", "none":
		return strategyStatic
	case "oauth2", "oauth2_refresh", "oauth2_client_credentials", "oauth2_authorization_code", "oauth2-refresh":
		return strategyOAuth2
	case "hmac", "hmac_sha256", "hmac_sha1":
		return strategyHMAC
	case "sigv4", "aws_sigv4", "aws-sigv4", "aws_sigv4_request":
		return strategySigV4
	case "mtls", "mtls_client_cert", "tls_client_cert":
		return strategyMTLS
	}
	return strings.ToLower(strings.TrimSpace(name))
}

// resolveAuthStrategyName picks the strategy name for a call, in priority
// order:
//
//  1. the credential's own `auth_strategy` — it is the most specific statement
//     of how THIS credential authenticates, and it is the config that carries
//     the strategy parameters. Honouring it avoids the footgun where a
//     credential configured for OAuth2 is silently ignored because the
//     provider's (possibly read-only, system-seeded) contract still says a
//     static scheme;
//  2. the contract's declared `auth.strategy` / `auth_strategy` (the provider's
//     requirement, used when the credential does not name one);
//  3. the credential's legacy `auth_mode`;
//  4. the provider row's `auth_type`.
//
// This ordering keeps legacy credentials (which carry auth_mode) working
// untouched while letting a contract graduate a provider to a richer strategy.
func resolveAuthStrategyName(contract *ProviderContract, prov *ProviderDef, cfg map[string]any) string {
	if s := strings.TrimSpace(strFromCfg(cfg, "auth_strategy")); s != "" {
		return s
	}
	if contract != nil && contract.Auth != nil {
		if s := strings.TrimSpace(contract.Auth.Strategy); s != "" {
			return s
		}
	}
	if s := strings.TrimSpace(strFromCfg(cfg, "auth_mode")); s != "" {
		return s
	}
	return strings.TrimSpace(prov.AuthType)
}

// --- static strategies (bearer / basic / custom header / query) ------------

// staticAuthStrategy reproduces the pre-#737 injection exactly by delegating to
// buildAuthHeader / buildAuthQuery. Keeping the delegation (rather than
// reimplementing) is what makes the "existing static credential path has zero
// regression" guarantee enforceable: the same code composes the value.
type staticAuthStrategy struct{}

func (staticAuthStrategy) Name() string { return strategyStatic }

func (staticAuthStrategy) Apply(_ context.Context, req *AuthRequest) (*AuthOutcome, error) {
	// Query-param providers (e.g. SerpAPI) carry the secret in the URL query.
	if name, val := buildAuthQuery(req.Provider, req.Config); name != "" {
		q := req.URL.Query()
		q.Set(name, val)
		req.URL.RawQuery = q.Encode()
		return &AuthOutcome{Strategy: strategyStatic, QueryName: name, QueryValue: val}, nil
	}
	value := buildAuthHeader(req.Provider, req.Config)
	if value == "" {
		return &AuthOutcome{Strategy: strategyStatic}, nil
	}
	scheme, payload := splitAuth(value)
	req.Header.Set(req.Provider.AuthHeader, value)
	return &AuthOutcome{
		Strategy:   strategyStatic,
		Scheme:     scheme,
		HeaderName: req.Provider.AuthHeader,
		Payload:    payload,
	}, nil
}

// --- OAuth2 (client credentials / refresh token) ---------------------------

// oauth2AuthStrategy authenticates with an OAuth2 access token. The refresh
// token (or client secret) lives in the encrypted credential config and is
// exchanged for a short-lived access token server-side; the browser/agent
// never participates. Tokens are cached per credential and refreshed
// proactively before expiry, with a forced refresh available for the proxy's
// 401 retry.
type oauth2AuthStrategy struct {
	cache *oauthTokenCache
}

func (o *oauth2AuthStrategy) Name() string { return "oauth2_refresh" }

// oauthRefreshSkew refreshes an access token this long before its stated
// expiry, so a token is never used in the last moments of its life (which
// would race the target's own clock).
const oauthRefreshSkew = 60 * time.Second

// defaultOAuthTokenTTL is used when a token endpoint omits `expires_in`.
const defaultOAuthTokenTTL = time.Hour

func (o *oauth2AuthStrategy) Apply(ctx context.Context, req *AuthRequest) (*AuthOutcome, error) {
	token, err := o.token(ctx, req)
	if err != nil {
		return nil, err
	}
	header := strings.TrimSpace(strFromCfg(req.Config, "auth_header"))
	if header == "" {
		header = req.Provider.AuthHeader
	}
	if header == "" {
		header = "Authorization"
	}
	prefix := strings.TrimSpace(strFromCfg(req.Config, "auth_prefix"))
	if prefix == "" {
		if req.Provider.AuthPrefix != "" {
			prefix = req.Provider.AuthPrefix
		} else {
			prefix = "Bearer"
		}
	}
	value := token
	if prefix != "" {
		value = prefix + " " + token
	}
	req.Header.Set(header, value)
	return &AuthOutcome{
		Strategy:   "oauth2_refresh",
		Scheme:     prefix,
		HeaderName: header,
		Payload:    token,
	}, nil
}

// token returns a usable access token: a cached one when fresh, otherwise a
// freshly minted one. A credential carrying only a static `access_token` and no
// token_url (the GSC "pasted token" shape) is passed through untouched.
func (o *oauth2AuthStrategy) token(ctx context.Context, req *AuthRequest) (string, error) {
	cfg := req.Config
	tokenURL := strings.TrimSpace(strFromCfg(cfg, "token_url"))
	if tokenURL == "" {
		if tok := credToken(cfg); tok != "" {
			return tok, nil
		}
		return "", &AuthStrategyError{Strategy: "oauth2_refresh",
			Message: "credential needs `token_url` (to mint tokens) or a static `access_token`"}
	}
	grantType := strings.TrimSpace(strFromCfg(cfg, "grant_type"))
	if grantType == "" {
		if strFromCfg(cfg, "refresh_token") != "" {
			grantType = "refresh_token"
		} else {
			grantType = "client_credentials"
		}
	}
	key := req.CredKey
	if key == "" {
		key = oauthCacheKey(req)
	}
	return o.cache.getOrFetch(key, req.ForceRefresh, func() (string, time.Time, error) {
		tok, ttl, err := o.exchange(ctx, req, tokenURL, grantType)
		if err != nil {
			return "", time.Time{}, err
		}
		return tok, time.Now().Add(ttl), nil
	})
}

// exchange performs the token-endpoint round trip for one grant.
func (o *oauth2AuthStrategy) exchange(ctx context.Context, req *AuthRequest, tokenURL, grantType string) (string, time.Duration, error) {
	cfg := req.Config
	clientID := strings.TrimSpace(firstNonEmpty(strFromCfg(cfg, "client_id"), strFromCfg(cfg, "username")))
	clientSecret := firstNonEmpty(strFromCfg(cfg, "client_secret"), strFromCfg(cfg, "password"))
	scope := strings.TrimSpace(firstNonEmpty(strFromCfg(cfg, "scope"), strFromCfg(cfg, "scopes")))

	form := url.Values{}
	form.Set("grant_type", grantType)
	if clientID != "" {
		form.Set("client_id", clientID)
	}
	if scope != "" {
		form.Set("scope", scope)
	}
	switch grantType {
	case "refresh_token":
		rt := firstNonEmpty(strFromCfg(cfg, "refresh_token"), strFromCfg(cfg, "token"))
		if rt == "" {
			return "", 0, &AuthStrategyError{Strategy: "oauth2_refresh",
				Message: "refresh_token grant requires `refresh_token` in the credential"}
		}
		form.Set("refresh_token", rt)
	case "client_credentials":
		if clientID == "" {
			return "", 0, &AuthStrategyError{Strategy: "oauth2_refresh",
				Message: "client_credentials grant requires `client_id` in the credential"}
		}
	default:
		return "", 0, &AuthStrategyError{Strategy: "oauth2_refresh",
			Message: fmt.Sprintf("unsupported grant_type %q (want refresh_token or client_credentials)", grantType)}
	}

	// RFC 6749 §2.3.1: a confidential client authenticates with HTTP Basic,
	// form-url-encoding id/secret first. `token_auth: body` opts into sending
	// them in the form body instead (some providers require that).
	useBodyAuth := strings.EqualFold(strings.TrimSpace(strFromCfg(cfg, "token_auth")), "body")
	if clientSecret != "" && useBodyAuth {
		form.Set("client_secret", clientSecret)
	}

	endpoint, err := url.Parse(tokenURL)
	if err != nil {
		return "", 0, &AuthStrategyError{Strategy: "oauth2_refresh", Message: "invalid token_url", Err: err}
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return "", 0, &AuthStrategyError{Strategy: "oauth2_refresh",
			Message: "token_url must be http(s)"}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, &AuthStrategyError{Strategy: "oauth2_refresh", Message: "build token request", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")
	if clientSecret != "" && !useBodyAuth {
		httpReq.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	}

	// The token endpoint is an externally-supplied URL, so the request MUST go
	// through the proxy's SSRF-guarded client (which rejects loopback/private/
	// link-local/cloud-metadata and pins the resolved IP to defeat DNS
	// rebinding; it honours the personal edition's local-source allowance).
	// Never fall back to an unguarded default client — a nil client is a wiring
	// bug, and silently using http.DefaultClient would open the SSRF hole this
	// path is meant to be behind.
	client := req.Client
	if client == nil {
		return "", 0, &AuthStrategyError{Strategy: "oauth2_refresh",
			Message: "no SSRF-guarded client available for the token request"}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", 0, &AuthStrategyError{Strategy: "oauth2_refresh", Message: "token endpoint unreachable", Err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, &AuthStrategyError{Strategy: "oauth2_refresh", Message: "read token response", Err: err}
	}
	var parsed struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    any    `json:"expires_in"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &parsed)
	if resp.StatusCode >= 400 || parsed.AccessToken == "" {
		// Report ONLY the RFC 6749 `error` code — never the raw response body
		// or `error_description`. A hostile or misconfigured token endpoint
		// could echo the client_secret/refresh_token back in its error text,
		// and this message reaches the agent, which must never see a
		// credential. The error code alone is enough to act on.
		detail := strings.TrimSpace(parsed.Error)
		if detail == "" {
			detail = "no error code in response"
		}
		return "", 0, &AuthStrategyError{Strategy: "oauth2_refresh",
			Message: fmt.Sprintf("token endpoint returned %d (%s)", resp.StatusCode, detail)}
	}
	ttl := coerceSeconds(parsed.ExpiresIn)
	if ttl <= 0 {
		ttl = defaultOAuthTokenTTL
	}
	return parsed.AccessToken, ttl, nil
}

// oauthCacheKey derives a stable cache key when the proxy did not supply one.
func oauthCacheKey(req *AuthRequest) string {
	prov := ""
	if req.Provider != nil {
		prov = req.Provider.Name
	}
	return strings.Join([]string{
		prov,
		strFromCfg(req.Config, "token_url"),
		strFromCfg(req.Config, "client_id"),
		strFromCfg(req.Config, "scope"),
		strFromCfg(req.Config, "refresh_token"),
	}, "|")
}

// coerceSeconds accepts a JSON number or numeric string (token endpoints are
// inconsistent) and returns it as a duration in seconds.
func coerceSeconds(v any) time.Duration {
	switch n := v.(type) {
	case float64:
		return time.Duration(n) * time.Second
	case int64:
		return time.Duration(n) * time.Second
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return time.Duration(i) * time.Second
		}
	case string:
		var i int64
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &i); err == nil {
			return time.Duration(i) * time.Second
		}
	}
	return 0
}

// oauthTokenCache caches access tokens per credential. A single fetch mutex
// serialises refreshes so a burst of concurrent calls triggers one token
// exchange, not N.
type oauthTokenCache struct {
	mu      sync.Mutex
	tokens  map[string]cachedToken
	fetchMu sync.Mutex
}

type cachedToken struct {
	token  string
	expiry time.Time
}

func newOAuthTokenCache() *oauthTokenCache {
	return &oauthTokenCache{tokens: map[string]cachedToken{}}
}

func (c *oauthTokenCache) get(key string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.tokens[key]
	if !ok {
		return "", false
	}
	if now.Add(oauthRefreshSkew).After(e.expiry) {
		delete(c.tokens, key)
		return "", false
	}
	return e.token, true
}

func (c *oauthTokenCache) put(key, token string, expiry time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens[key] = cachedToken{token: token, expiry: expiry}
}

// invalidate drops a cached token so the next call re-mints (used by the
// proxy's 401 retry).
func (c *oauthTokenCache) invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tokens, key)
}

// getOrFetch returns a cached token when fresh, else calls fetch once
// (serialised) and caches the result.
func (c *oauthTokenCache) getOrFetch(key string, force bool, fetch func() (string, time.Time, error)) (string, error) {
	if !force {
		if tok, ok := c.get(key, time.Now()); ok {
			return tok, nil
		}
	}
	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()
	if !force {
		// Another goroutine may have refreshed while we waited for the lock.
		if tok, ok := c.get(key, time.Now()); ok {
			return tok, nil
		}
	}
	tok, expiry, err := fetch()
	if err != nil {
		return "", err
	}
	c.put(key, tok, expiry)
	return tok, nil
}

// --- HMAC request signing ------------------------------------------------

// hmacAuthStrategy signs the request with a shared secret (HMAC-SHA256 by
// default). Two signing modes:
//
//   - "request" (default): a canonical string of method, path, query,
//     timestamp and the SHA-256 of the body — the scheme the peer recomputes.
//   - "body": the raw body only (the GitHub/Slack webhook shape
//     `sha256=<hex>`), selected with `hmac_sign: body`.
//
// The signature is written to `hmac_header` (default X-Signature) and the
// timestamp to `hmac_timestamp_header` (default X-Timestamp), so the peer can
// reject replays outside its window.
type hmacAuthStrategy struct{}

func (hmacAuthStrategy) Name() string { return "hmac" }

func (hmacAuthStrategy) Apply(_ context.Context, req *AuthRequest) (*AuthOutcome, error) {
	cfg := req.Config
	secret := firstNonEmpty(strFromCfg(cfg, "hmac_secret"), strFromCfg(cfg, "secret_key"), strFromCfg(cfg, "secret"))
	if secret == "" {
		return nil, &AuthStrategyError{Strategy: "hmac", Message: "credential needs `hmac_secret`"}
	}
	algo := strings.ToLower(strings.TrimSpace(firstNonEmpty(strFromCfg(cfg, "hmac_algorithm"), "sha256")))
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	// Test seam: a fixed timestamp lets a known-answer vector pin the output.
	if t := strings.TrimSpace(strFromCfg(cfg, "hmac_timestamp")); t != "" {
		timestamp = t
	}
	mode := strings.ToLower(strings.TrimSpace(strFromCfg(cfg, "hmac_sign")))
	var message string
	if mode == "body" {
		message = string(req.Body)
	} else {
		message = hmacCanonicalRequest(req, timestamp)
	}
	sig, err := hmacSign(algo, secret, message)
	if err != nil {
		return nil, &AuthStrategyError{Strategy: "hmac", Message: err.Error()}
	}
	if prefix := strFromCfg(cfg, "hmac_signature_prefix"); prefix != "" {
		sig = prefix + sig
	}
	headerName := firstNonEmpty(strFromCfg(cfg, "hmac_header"), "X-Signature")
	tsHeader := firstNonEmpty(strFromCfg(cfg, "hmac_timestamp_header"), "X-Timestamp")
	req.Header.Set(headerName, sig)
	req.Header.Set(tsHeader, timestamp)
	if keyID := firstNonEmpty(strFromCfg(cfg, "hmac_key_id"), strFromCfg(cfg, "access_key")); keyID != "" {
		req.Header.Set("X-Key-Id", keyID)
	}
	scheme, payload := splitAuth(sig)
	return &AuthOutcome{Strategy: "hmac", Scheme: scheme, HeaderName: headerName, Payload: payload}, nil
}

// hmacCanonicalRequest builds the default signing string:
//
//	METHOD\n
//	/path\n
//	rawquery\n
//	timestamp\n
//	hex(sha256(body))
//
// It is deliberately flat and fully documented so an integrator can reproduce
// it in any language.
func hmacCanonicalRequest(req *AuthRequest, timestamp string) string {
	bodySum := sha256.Sum256(req.Body)
	return strings.Join([]string{
		strings.ToUpper(req.Method),
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		timestamp,
		hex.EncodeToString(bodySum[:]),
	}, "\n")
}

// hmacSign returns the lowercase hex HMAC of message under algo (sha256|sha1).
func hmacSign(algo, secret, message string) (string, error) {
	var mac hashWriter
	switch algo {
	case "sha256", "":
		mac = hmac.New(sha256.New, []byte(secret))
	case "sha1":
		mac = hmac.New(sha1.New, []byte(secret))
	default:
		return "", fmt.Errorf("unsupported hmac_algorithm %q (want sha256 or sha1)", algo)
	}
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// hashWriter is the minimal hash.Hash surface hmacSign needs.
type hashWriter interface {
	Write(p []byte) (int, error)
	Sum(b []byte) []byte
}

// --- AWS Signature Version 4 ---------------------------------------------

// sigV4AuthStrategy signs the request with AWS Signature Version 4. It
// implements the algorithm exactly as specified so the target service's own
// verifier accepts the signature (see the known-answer test using the AWS
// SigV4 test-suite vector). Credentials: `access_key`, `secret_key`, optional
// `session_token`, plus `region` and `service`.
type sigV4AuthStrategy struct{}

func (sigV4AuthStrategy) Name() string { return "sigv4" }

func (sigV4AuthStrategy) Apply(_ context.Context, req *AuthRequest) (*AuthOutcome, error) {
	cfg := req.Config
	accessKey := firstNonEmpty(strFromCfg(cfg, "access_key"), strFromCfg(cfg, "aws_access_key_id"))
	secretKey := firstNonEmpty(strFromCfg(cfg, "secret_key"), strFromCfg(cfg, "aws_secret_access_key"))
	sessionToken := firstNonEmpty(strFromCfg(cfg, "session_token"), strFromCfg(cfg, "aws_session_token"))
	region := strings.TrimSpace(strFromCfg(cfg, "region"))
	service := strings.TrimSpace(strFromCfg(cfg, "service"))
	switch {
	case accessKey == "" || secretKey == "":
		return nil, &AuthStrategyError{Strategy: "sigv4", Message: "credential needs `access_key` and `secret_key`"}
	case region == "":
		return nil, &AuthStrategyError{Strategy: "sigv4", Message: "credential needs `region`"}
	case service == "":
		return nil, &AuthStrategyError{Strategy: "sigv4", Message: "credential needs `service`"}
	}
	now := time.Now().UTC()
	// Test seam: a pinned signing time is required by known-answer vectors.
	if t := strings.TrimSpace(strFromCfg(cfg, "sigv4_time")); t != "" {
		parsed, err := parseSigV4Time(t)
		if err != nil {
			return nil, &AuthStrategyError{Strategy: "sigv4", Message: "invalid sigv4_time", Err: err}
		}
		now = parsed
	}
	signature := sigV4Sign(req, accessKey, secretKey, sessionToken, region, service, now)
	return &AuthOutcome{Strategy: "sigv4", Scheme: "AWS4-HMAC-SHA256", Payload: signature}, nil
}

// parseSigV4Time accepts either RFC3339 or the AWS basic format.
func parseSigV4Time(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Parse("20060102T150405Z", s)
}

// sigV4Sign computes and writes the SigV4 headers, returning the signature.
func sigV4Sign(req *AuthRequest, accessKey, secretKey, sessionToken, region, service string, t time.Time) string {
	amzDate := t.Format("20060102T150405Z")
	dateStamp := t.Format("20060102")
	payloadHash := sha256Hex(req.Body)

	canonicalHeaders := map[string]string{
		"host":       req.URL.Host,
		"x-amz-date": amzDate,
	}
	if sessionToken != "" {
		canonicalHeaders["x-amz-security-token"] = sessionToken
	}
	names := make([]string, 0, len(canonicalHeaders))
	for k := range canonicalHeaders {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k)
		ch.WriteByte(':')
		ch.WriteString(strings.TrimSpace(canonicalHeaders[k]))
		ch.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")

	canonicalURI := awsURIEncode(req.URL.Path, false)
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalRequest := strings.Join([]string{
		strings.ToUpper(req.Method),
		canonicalURI,
		awsCanonicalQuery(req.URL),
		ch.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := sigV4SigningKey(secretKey, dateStamp, region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	req.Header.Set("X-Amz-Date", amzDate)
	if sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", sessionToken)
	}
	// NB: no unsigned `x-amz-content-sha256`. AWS requires every `x-amz-*`
	// header present on the request to be covered by SignedHeaders, so sending
	// it unsigned invites SignatureDoesNotMatch. The payload is still committed
	// by the signature via HashedPayload in the canonical request, and omitting
	// the header is exactly what the AWS test-suite `get-vanilla` vector does
	// (our known-answer test). S3, which mandates a signed payload header, is
	// out of scope here.
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, credentialScope, signedHeaders, signature))
	return signature
}

func sigV4SigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// awsCanonicalQuery renders the canonical query string: parameters sorted by
// encoded key (then value), each key/value AWS-URI-encoded.
func awsCanonicalQuery(u *url.URL) string {
	values := u.Query()
	if len(values) == 0 {
		return ""
	}
	type kv struct{ k, v string }
	var pairs []kv
	for k, vs := range values {
		for _, v := range vs {
			pairs = append(pairs, kv{awsURIEncode(k, true), awsURIEncode(v, true)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, p.k+"="+p.v)
	}
	return strings.Join(parts, "&")
}

// awsURIEncode implements the AWS URI-encoding rule: unreserved characters
// (A-Za-z0-9-_.~) pass through, "/" passes through only when encodeSlash is
// false, everything else is %XX-encoded per byte.
func awsURIEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'),
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte('/')
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}

// --- mTLS -----------------------------------------------------------------

// mtlsAuthStrategy authenticates with a TLS client certificate. It writes no
// header — the certificate is presented during the TLS handshake — so its
// Apply only validates that the credential carries a usable pair; the proxy
// then swaps in a certificate-presenting client via Client().
type mtlsAuthStrategy struct{}

func (mtlsAuthStrategy) Name() string { return "mtls" }

func (mtlsAuthStrategy) Apply(_ context.Context, req *AuthRequest) (*AuthOutcome, error) {
	if strFromCfg(req.Config, "client_cert_pem") == "" || strFromCfg(req.Config, "client_key_pem") == "" {
		return nil, &AuthStrategyError{Strategy: "mtls",
			Message: "credential needs `client_cert_pem` and `client_key_pem`"}
	}
	return &AuthOutcome{Strategy: "mtls", MTLS: true}, nil
}

// Client returns a copy of base that presents the credential's client
// certificate. The base transport (and therefore the SSRF-guarded dialer) is
// cloned, so switching to mTLS never weakens the proxy's address protections.
func (mtlsAuthStrategy) Client(base *http.Client, cfg map[string]any) (*http.Client, error) {
	cert, err := tls.X509KeyPair(
		[]byte(strFromCfg(cfg, "client_cert_pem")),
		[]byte(strFromCfg(cfg, "client_key_pem")),
	)
	if err != nil {
		return nil, &AuthStrategyError{Strategy: "mtls", Message: "invalid client certificate/key", Err: err}
	}
	tlsCfg := &tls.Config{}
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	cloned, ok := transport.(*http.Transport)
	if !ok {
		return nil, &AuthStrategyError{Strategy: "mtls", Message: "proxy transport is not an *http.Transport"}
	}
	cloned = cloned.Clone()
	// Preserve whatever TLS config the base transport already carries (notably
	// the SSRF guard's settings, or a test server's RootCAs) and layer the
	// client certificate on top.
	if cloned.TLSClientConfig != nil {
		tlsCfg = cloned.TLSClientConfig.Clone()
	}
	if tlsCfg.MinVersion == 0 {
		tlsCfg.MinVersion = tls.VersionTLS12
	}
	tlsCfg.Certificates = append(tlsCfg.Certificates, cert)
	if ca := strFromCfg(cfg, "ca_cert_pem"); ca != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(ca)) {
			return nil, &AuthStrategyError{Strategy: "mtls", Message: "ca_cert_pem is not a valid PEM certificate"}
		}
		tlsCfg.RootCAs = pool
	}
	cloned.TLSClientConfig = tlsCfg
	return &http.Client{Transport: cloned, Timeout: base.Timeout}, nil
}
