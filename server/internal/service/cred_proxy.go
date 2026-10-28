// Package service: local credential proxy for scene-projected MCP servers.
//
// Scene projection used to write decrypted ${cred:...} values straight into the
// workspace .mcp.json as an http/sse server's `headers`. The agent can read its
// own workspace, so a long-lived secret materialized there was visible to it.
//
// This service replaces that with a pointer indirection:
//
//   - At projection time the resolved secret NEVER reaches a workspace file.
//     niuniu mints a short, authenticated token that names the workspace + MCP
//     server (nothing else), and writes only the loopback proxy URL:
//
//     "url": "http://127.0.0.1:<port>/cred-proxy/<token>"
//
//     The placeholder-bearing header is removed from the config entirely; the
//     resolved value is simply discarded after resolution validated it.
//
//   - At request time the MCP client POSTs (or opens the SSE GET) against that
//     loopback URL; the proxy decrypts the token, re-resolves the CURRENT
//     credential for that workspace+server, injects the real header(s) on
//     egress, and streams the upstream response back untouched.
//
// Why a pointer rather than a captured secret: the token is stateless (sealed
// with the integration keyring, which persists), so it survives a niuniu
// restart; it re-resolves on every request, so changing or unbinding the
// credential, or removing the scene, takes effect immediately and needs no
// revocation list; and the process never holds a decrypted secret outside the
// request that uses it.
//
// What the token buys an attacker who reads .mcp.json: the ability to send
// requests to ONE scene-authored upstream with the credential attached, for as
// long as the token lives — i.e. exactly the capability the MCP server itself
// has. It does NOT expose the secret, so it cannot be replayed against another
// service, pasted into a chat, or used for anything outside that one server.
//
// Threat model, residual risks and tradeoffs:
// docs/security/2026-10-11-mcp-credential-proxy-threat-model.md
package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
)

// credProxyTokenPrefix namespaces the opaque proxy tokens so they are
// recognizable in a log line or a config dump (and cannot be mistaken for the
// niuniu session token).
const credProxyTokenPrefix = "ncp_"

// credProxyTokenTTL bounds how long a minted pointer stays valid. It is a
// secondary defense only: the token carries no authority of its own (it names a
// workspace + server), so removing the scene or unbinding the credential
// already stops it. The TTL just stops a long-forgotten .mcp.json snapshot from
// working forever.
const credProxyTokenTTL = 7 * 24 * time.Hour

var (
	// ErrCredProxyTokenInvalid is returned for a malformed, forged, or expired token.
	ErrCredProxyTokenInvalid = errors.New("credential proxy: invalid or expired token")
	// ErrCredProxyTargetUnavailable is returned when the token is valid but the
	// workspace/server it names no longer resolves to a usable credential.
	ErrCredProxyTargetUnavailable = errors.New("credential proxy: target is not available")
)

// credProxyClaims is the (secret-free) payload sealed into a token.
type credProxyClaims struct {
	WorkspaceID int64  `json:"w"`
	ServerName  string `json:"s"`
	ExpiresAt   int64  `json:"e"` // unix seconds
	// Target pins the token to the exact upstream it was minted for
	// ("scheme://host/basepath"). Resolve rejects the token when the live
	// upstream no longer matches, so a leaked or stale token cannot be
	// re-pointed at a different upstream by editing the scene config. Empty
	// means unpinned (a token minted before this field existed).
	Target string `json:"t,omitempty"`
}

// targetKey normalizes an upstream URL into the value a token is pinned to:
// scheme + host + cleaned base path (so "/a/b/", "/a/b" and "/a/b/." agree).
// An empty or unparsable input yields "" (unpinned).
func targetKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + path.Clean("/"+u.Path)
}

// CredTargetResolver re-resolves the live upstream + credential headers for a
// projected MCP server at request time. Implemented by SceneProjector.
type CredTargetResolver func(ctx context.Context, workspaceID int64, serverName string) (upstream string, headers map[string]string, err error)

// CredProxyService mints and serves the loopback credential proxy.
type CredProxyService struct {
	keyring *crypto.Keyring
	// apiBase returns the http://127.0.0.1:<port> base the workspace MCP
	// clients can reach this server on. Late-bound (the actual port is only
	// known once the listener is up / the lockfile is written).
	apiBase func() string
	resolve CredTargetResolver

	now func() time.Time // injectable clock for tests

	proxy *httputil.ReverseProxy
}

// NewCredProxyService builds the service. keyring seals/unseals the tokens;
// nil makes Issue/Resolve fail closed (never mint an unauthenticated token).
func NewCredProxyService(keyring *crypto.Keyring, apiBase func() string, resolve CredTargetResolver) *CredProxyService {
	s := &CredProxyService{
		keyring: keyring,
		apiBase: apiBase,
		resolve: resolve,
		now:     time.Now,
	}
	s.proxy = &httputil.ReverseProxy{
		Director:      s.direct,
		ErrorHandler:  s.proxyError,
		FlushInterval: 100 * time.Millisecond, // stream SSE bodies promptly
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		},
	}
	return s
}

// ProxyPathPrefix is the loopback path every minted proxy URL starts with.
const ProxyPathPrefix = "/cred-proxy/"

// Issue mints the loopback URL a workspace MCP server should be pointed at. The
// resolved credential is NOT passed here — the token only names the
// (workspace, server) pair, and the real header is resolved per request.
//
// The optional intendedTarget is the upstream URL the projection is replacing;
// when given, the token is pinned to it (see credProxyClaims.Target) so it
// cannot later be re-pointed at a different upstream.
func (s *CredProxyService) Issue(workspaceID int64, serverName string, intendedTarget ...string) (string, error) {
	if s.keyring == nil {
		return "", fmt.Errorf("credential proxy: no keyring configured")
	}
	if workspaceID <= 0 || serverName == "" {
		return "", fmt.Errorf("credential proxy: workspace and server name are required")
	}
	target := ""
	if len(intendedTarget) > 0 {
		target = targetKey(intendedTarget[0])
	}
	payload, err := json.Marshal(credProxyClaims{
		WorkspaceID: workspaceID,
		ServerName:  serverName,
		ExpiresAt:   s.now().Add(credProxyTokenTTL).Unix(),
		Target:      target,
	})
	if err != nil {
		return "", fmt.Errorf("credential proxy: marshal claims: %w", err)
	}
	sealed, err := s.keyring.Encrypt(payload)
	if err != nil {
		return "", fmt.Errorf("credential proxy: seal token: %w", err)
	}
	token, err := toURLSafeToken(sealed)
	if err != nil {
		return "", err
	}
	base := ""
	if s.apiBase != nil {
		base = strings.TrimRight(s.apiBase(), "/")
	}
	return base + ProxyPathPrefix + token, nil
}

// openToken authenticates a token and returns the claims it names. A forged,
// truncated, foreign-key, or expired token is rejected with
// ErrCredProxyTokenInvalid — the caller learns nothing about which.
func (s *CredProxyService) openToken(token string) (credProxyClaims, error) {
	if s.keyring == nil {
		return credProxyClaims{}, ErrCredProxyTokenInvalid
	}
	sealed, err := fromURLSafeToken(token)
	if err != nil {
		return credProxyClaims{}, ErrCredProxyTokenInvalid
	}
	payload, err := s.keyring.Decrypt(sealed)
	if err != nil {
		return credProxyClaims{}, ErrCredProxyTokenInvalid
	}
	var claims credProxyClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return credProxyClaims{}, ErrCredProxyTokenInvalid
	}
	if claims.WorkspaceID <= 0 || claims.ServerName == "" ||
		s.now().Unix() > claims.ExpiresAt {
		return credProxyClaims{}, ErrCredProxyTokenInvalid
	}
	return claims, nil
}

// ProxyTarget is the resolved upstream a token names right now.
type ProxyTarget struct {
	Server   string
	Upstream *url.URL
	Headers  map[string]string
}

// Resolve authenticates the token and re-resolves its current target. It
// returns ErrCredProxyTokenInvalid for a bad token and
// ErrCredProxyTargetUnavailable when the workspace/server no longer yields a
// usable credential (scene removed, credential unbound).
func (s *CredProxyService) Resolve(ctx context.Context, token string) (ProxyTarget, error) {
	claims, err := s.openToken(token)
	if err != nil {
		return ProxyTarget{}, err
	}
	if s.resolve == nil {
		return ProxyTarget{}, ErrCredProxyTargetUnavailable
	}
	upstream, headers, err := s.resolve(ctx, claims.WorkspaceID, claims.ServerName)
	if err != nil {
		slog.Info("credential proxy: target resolve failed",
			"workspace_id", claims.WorkspaceID, "server", claims.ServerName, "err", err)
		return ProxyTarget{}, fmt.Errorf("%w: %v", ErrCredProxyTargetUnavailable, err)
	}
	u, err := url.Parse(upstream)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ProxyTarget{}, fmt.Errorf("%w: unusable upstream url", ErrCredProxyTargetUnavailable)
	}
	// The token is pinned to the upstream it was minted for: if the live scene
	// now resolves to a different upstream, a stale/leaked token must not follow
	// it. An unpinned token (minted before pinning existed) is still accepted.
	if claims.Target != "" && targetKey(u.String()) != claims.Target {
		slog.Warn("credential proxy: token target no longer matches the projected upstream",
			"server", claims.ServerName)
		return ProxyTarget{}, ErrCredProxyTokenInvalid
	}
	if len(headers) == 0 {
		return ProxyTarget{}, fmt.Errorf("%w: no credential header", ErrCredProxyTargetUnavailable)
	}
	return ProxyTarget{Server: claims.ServerName, Upstream: u, Headers: headers}, nil
}

// ServeHTTP proxies one already-resolved request. rest is the sub-path after
// the token (/cred-proxy/:token/*rest), appended to the upstream's own path.
func (s *CredProxyService) ServeHTTP(w http.ResponseWriter, r *http.Request, target ProxyTarget, rest string) {
	// The caller controls rest, so confine it to the projected upstream path:
	// the token must not be usable as a general-purpose credential for the
	// whole host (e.g. /.. escapes, or a sibling endpoint outside the base).
	if !withinUpstreamPath(target.Upstream.Path, rest) {
		slog.Warn("credential proxy: refused a request that escapes the projected upstream path",
			"server", target.Server)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"credential proxy: path outside the projected upstream"}`))
		return
	}
	ctx := context.WithValue(r.Context(), credProxyTargetKey{}, target)
	ctx = context.WithValue(ctx, credProxyRestKey{}, rest)
	s.proxy.ServeHTTP(w, r.WithContext(ctx))
}

// withinUpstreamPath reports whether appending the caller-supplied rest to the
// upstream base path keeps the request inside that base. It blocks ".."
// traversal out of the projected endpoint. A bare-host upstream (empty base
// path) has no base to confine to, so any path on that host is allowed — that
// is the projected target's own scope.
func withinUpstreamPath(basePath, rest string) bool {
	base := path.Clean("/" + strings.Trim(basePath, "/"))
	if base == "/" {
		return true
	}
	joined := path.Clean(joinProxyPath(basePath, rest))
	return joined == base || strings.HasPrefix(joined, base+"/")
}

type credProxyTargetKey struct{}
type credProxyRestKey struct{}

// direct rewrites the inbound loopback request into the upstream one and
// injects the real credential headers. Everything else (method, body, most
// headers) is carried through so JSON-RPC and SSE both work unchanged.
func (s *CredProxyService) direct(r *http.Request) {
	target, _ := r.Context().Value(credProxyTargetKey{}).(ProxyTarget)
	rest, _ := r.Context().Value(credProxyRestKey{}).(string)
	up := target.Upstream

	r.URL.Scheme = up.Scheme
	r.URL.Host = up.Host
	r.URL.Path = joinProxyPath(up.Path, rest)
	r.URL.RawQuery = joinProxyQuery(up.RawQuery, r.URL.RawQuery)
	r.Host = up.Host

	// The proxy token IS the inbound auth; whatever the client sent is dropped
	// so the upstream only ever sees the injected credential.
	r.Header.Del("Authorization")
	for k, v := range target.Headers {
		r.Header.Set(k, v)
	}
	// The upstream is reached from the niuniu host, not from the agent's
	// machine identity — don't leak the loopback origin chain. ReverseProxy
	// re-adds X-Forwarded-For AFTER the Director runs, and a nil (present but
	// empty) slice is the documented way to opt out; Del alone would not stick.
	r.Header["X-Forwarded-For"] = nil
	r.Header["X-Forwarded-Host"] = nil
	r.Header["X-Forwarded-Proto"] = nil
}

// proxyError keeps upstream failures from echoing anything about the request,
// and never logs headers.
func (s *CredProxyService) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	target, _ := r.Context().Value(credProxyTargetKey{}).(ProxyTarget)
	status := http.StatusBadGateway
	if errors.Is(err, context.Canceled) {
		status = 499 // client went away; nothing useful to report
	}
	slog.Warn("credential proxy: upstream request failed", "server", target.Server, "err", err)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"credential proxy: upstream request failed"}`))
}

// joinProxyPath appends the inbound sub-path to the upstream's base path,
// keeping exactly one separating slash.
func joinProxyPath(base, rest string) string {
	if rest == "" {
		if base == "" {
			return "/"
		}
		return base
	}
	if !strings.HasPrefix(rest, "/") {
		rest = "/" + rest
	}
	return strings.TrimRight(base, "/") + rest
}

// joinProxyQuery merges the upstream's own query string with the inbound one
// (inbound wins on conflict), so a scene-authored `?tenant=x` survives while
// MCP's own params still reach the upstream.
func joinProxyQuery(upstreamQuery, inboundQuery string) string {
	switch {
	case upstreamQuery == "":
		return inboundQuery
	case inboundQuery == "":
		return upstreamQuery
	default:
		return upstreamQuery + "&" + inboundQuery
	}
}

// toURLSafeToken re-encodes the keyring's standard-base64 ciphertext as
// base64url, because the token rides in a URL path segment: '+' and '/' would
// otherwise be mangled or split the segment.
func toURLSafeToken(sealed []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(string(sealed))
	if err != nil {
		return "", fmt.Errorf("credential proxy: decode sealed token: %w", err)
	}
	return credProxyTokenPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// fromURLSafeToken reverses toURLSafeToken back into what Keyring.Decrypt takes.
func fromURLSafeToken(token string) ([]byte, error) {
	token = strings.TrimPrefix(token, credProxyTokenPrefix)
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	out := make([]byte, base64.StdEncoding.EncodedLen(len(raw)))
	base64.StdEncoding.Encode(out, raw)
	return out, nil
}
