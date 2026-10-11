package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
)

// testKeyring returns a real keyring backed by a temp file — the same code path
// production uses, so the sealing round-trip is genuinely exercised.
func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	k, err := crypto.LoadOrCreate(filepath.Join(t.TempDir(), "integration_secret"))
	require.NoError(t, err)
	return k
}

// tokenOf peels the loopback prefix off a minted URL. It tolerates a nil apiBase
// (path-only URL), which the registry-focused tests use.
func tokenOf(t *testing.T, proxied string) string {
	t.Helper()
	i := strings.Index(proxied, ProxyPathPrefix)
	require.GreaterOrEqual(t, i, 0, proxied)
	return proxied[i+len(ProxyPathPrefix):]
}

func TestCredProxy_IssueAndOpen(t *testing.T) {
	p := NewCredProxyService(testKeyring(t), func() string { return "http://127.0.0.1:3000" }, nil)

	proxied, err := p.Issue(42, "kb")
	require.NoError(t, err)
	token := tokenOf(t, proxied)

	claims, err := p.openToken(token)
	require.NoError(t, err)
	assert.Equal(t, int64(42), claims.WorkspaceID)
	assert.Equal(t, "kb", claims.ServerName)
	assert.Greater(t, claims.ExpiresAt, time.Now().Unix())

	// The token names the target but carries no secret.
	assert.NotContains(t, token, "kb")

	// Forged / truncated / foreign tokens are indistinguishable from garbage.
	for _, bad := range []string{"", "ncp_", "ncp_zzzz", token[:len(token)-4]} {
		_, err := p.openToken(bad)
		assert.ErrorIs(t, err, ErrCredProxyTokenInvalid, "token %q", bad)
	}
	other := NewCredProxyService(testKeyring(t), nil, nil)
	_, err = other.openToken(token)
	assert.ErrorIs(t, err, ErrCredProxyTokenInvalid, "a foreign keyring cannot open it")
}

func TestCredProxy_IssueRejectsUnusableScope(t *testing.T) {
	p := NewCredProxyService(testKeyring(t), nil, nil)
	_, err := p.Issue(0, "kb")
	assert.Error(t, err)
	_, err = p.Issue(1, "")
	assert.Error(t, err)

	noKey := NewCredProxyService(nil, nil, nil)
	_, err = noKey.Issue(1, "kb")
	assert.Error(t, err, "no keyring means no token rather than an unauthenticated one")
}

func TestCredProxy_TokenExpires(t *testing.T) {
	p := NewCredProxyService(testKeyring(t), nil, nil)
	now := time.Unix(1000, 0)
	p.now = func() time.Time { return now }
	proxied, err := p.Issue(1, "kb")
	require.NoError(t, err)
	token := tokenOf(t, proxied)

	now = now.Add(credProxyTokenTTL - time.Minute)
	_, err = p.openToken(token)
	require.NoError(t, err)

	now = now.Add(2 * time.Minute)
	_, err = p.openToken(token)
	assert.ErrorIs(t, err, ErrCredProxyTokenInvalid)
}

func TestCredProxy_ResolveUsesLiveTarget(t *testing.T) {
	var gotWS int64
	var gotServer, upstream string
	headers := map[string]string{}
	p := NewCredProxyService(testKeyring(t), nil, func(_ context.Context, wsID int64, server string) (string, map[string]string, error) {
		gotWS, gotServer = wsID, server
		return upstream, headers, nil
	})
	proxied, err := p.Issue(9, "wechat-mp")
	require.NoError(t, err)

	upstream, headers = "https://mp.example.com/mcp", map[string]string{"Authorization": "Bearer live"}
	target, err := p.Resolve(context.Background(), tokenOf(t, proxied))
	require.NoError(t, err)
	assert.Equal(t, int64(9), gotWS)
	assert.Equal(t, "wechat-mp", gotServer)
	assert.Equal(t, "https://mp.example.com/mcp", target.Upstream.String())
	assert.Equal(t, "Bearer live", target.Headers["Authorization"])

	// A rotated credential is picked up on the next request — no revocation step.
	headers = map[string]string{"Authorization": "Bearer rotated"}
	target, err = p.Resolve(context.Background(), tokenOf(t, proxied))
	require.NoError(t, err)
	assert.Equal(t, "Bearer rotated", target.Headers["Authorization"])

	// The scene (or the credential) going away makes the target unavailable —
	// not the token invalid, so the two failure modes stay distinguishable.
	headers = nil
	_, err = p.Resolve(context.Background(), tokenOf(t, proxied))
	assert.ErrorIs(t, err, ErrCredProxyTargetUnavailable)

	headers = map[string]string{"Authorization": "Bearer x"}
	upstream = "file:///etc/passwd"
	_, err = p.Resolve(context.Background(), tokenOf(t, proxied))
	assert.ErrorIs(t, err, ErrCredProxyTargetUnavailable, "a non-http upstream is refused")
}

// TestCredProxy_ForwardInjectsHeader is the egress contract: the agent-side
// request carries no credential, and the upstream sees the injected one.
func TestCredProxy_ForwardInjectsHeader(t *testing.T) {
	var gotAuth, gotPath, gotQuery, gotBody, gotXFF string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotXFF = r.Header.Get("X-Forwarded-For")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL + "/mcp?tenant=t1")
	require.NoError(t, err)
	p := NewCredProxyService(nil, nil, nil)
	target := ProxyTarget{Server: "kb", Upstream: u, Headers: map[string]string{"Authorization": "Bearer real-secret"}}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:3000/cred-proxy/tok?page=2", strings.NewReader(`{"jsonrpc":"2.0"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req, target, "")

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, `{"ok":true}`, rec.Body.String())
	assert.Equal(t, "Bearer real-secret", gotAuth, "the real credential is injected on egress")
	assert.Equal(t, "/mcp", gotPath)
	assert.Equal(t, "tenant=t1&page=2", gotQuery, "upstream query preserved, inbound appended")
	assert.Equal(t, `{"jsonrpc":"2.0"}`, gotBody)
	assert.Empty(t, gotXFF, "the loopback origin chain is not forwarded")
}

// TestCredProxy_ForwardDropsInboundAuth ensures a client cannot smuggle its own
// credential past the injected one.
func TestCredProxy_ForwardDropsInboundAuth(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	p := NewCredProxyService(nil, nil, nil)
	target := ProxyTarget{Server: "kb", Upstream: u, Headers: map[string]string{"Authorization": "Bearer real"}}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:3000/cred-proxy/tok", nil)
	req.Header.Set("Authorization", "Bearer attacker-supplied")
	p.ServeHTTP(httptest.NewRecorder(), req, target, "")
	assert.Equal(t, "Bearer real", gotAuth)
}

// TestCredProxy_ForwardSubPathAppendsToUpstream covers a client that treats the
// proxied url as a base and appends its own path.
func TestCredProxy_ForwardSubPathAppendsToUpstream(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL + "/base")
	p := NewCredProxyService(nil, nil, nil)
	target := ProxyTarget{Server: "kb", Upstream: u, Headers: map[string]string{"X-Api-Key": "k"}}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:3000/cred-proxy/tok/v1/tools", nil)
	p.ServeHTTP(httptest.NewRecorder(), req, target, "/v1/tools")
	assert.Equal(t, "/base/v1/tools", gotPath)
}

func TestCredProxy_ForwardUpstreamDown(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:1/mcp")
	p := NewCredProxyService(nil, nil, nil)
	target := ProxyTarget{Server: "kb", Upstream: u, Headers: map[string]string{"Authorization": "Bearer s"}}

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:3000/cred-proxy/tok", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req, target, "")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.NotContains(t, rec.Body.String(), "Bearer s", "an error path never echoes the secret")
}
