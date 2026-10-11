package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// testKeyringSrv returns a real keyring backed by a temp file.
func testKeyringSrv(t *testing.T) *crypto.Keyring {
	t.Helper()
	k, err := crypto.LoadOrCreate(filepath.Join(t.TempDir(), "integration_secret"))
	require.NoError(t, err)
	return k
}

// newCredProxyTestServer mounts the two loopback routes exactly as
// setupRoutes does, behind a REAL http server: gin's responseWriter implements
// CloseNotifier but the recorder it wraps does not, and httputil.ReverseProxy
// type-asserts on it — so a ResponseRecorder would panic where the production
// server does not.
func newCredProxyTestServer(t *testing.T, upstream string, headers map[string]string) (*httptest.Server, *service.CredProxyService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	keyring := testKeyringSrv(t)
	resolve := func(context.Context, int64, string) (string, map[string]string, error) {
		if upstream == "" {
			return "", nil, errors.New("no target")
		}
		return upstream, headers, nil
	}
	s := &Server{credProxySvc: service.NewCredProxyService(keyring, func() string { return "http://127.0.0.1:3000" }, resolve)}
	r := gin.New()
	r.Any("/cred-proxy/:token", s.credProxyForward)
	r.Any("/cred-proxy/:token/*rest", s.credProxyForward)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, s.credProxySvc
}

// postProxy issues the request against the test server and returns status/body.
func postProxy(t *testing.T, srv *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := srv.Client().Post(srv.URL+path, "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

func TestCredProxyRoute_ForwardsWithInjectedHeader(t *testing.T) {
	var gotAuth, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer upstream.Close()

	srv, proxy := newCredProxyTestServer(t, upstream.URL+"/mcp", map[string]string{"Authorization": "Bearer super-secret"})
	// The token path is host-agnostic, so reuse just the path portion.
	proxied, err := proxy.Issue(1, "kb")
	require.NoError(t, err)
	tokenPath := proxied[len("http://127.0.0.1:3000"):]

	// The bare form: MCP clients POST to the exact configured url.
	code, body := postProxy(t, srv, tokenPath)
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, `{"result":"ok"}`, body)
	assert.Equal(t, "Bearer super-secret", gotAuth)
	assert.Equal(t, "/mcp", gotPath)

	// The sub-path form.
	code, body = postProxy(t, srv, tokenPath+"/tools")
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "/mcp/tools", gotPath)
}

func TestCredProxyRoute_RejectsUnknownToken(t *testing.T) {
	srv, _ := newCredProxyTestServer(t, "https://kb.example.com/mcp", map[string]string{"Authorization": "Bearer x"})
	code, body := postProxy(t, srv, "/cred-proxy/ncp_deadbeef")
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Contains(t, body, "INVALID_TOKEN")
}

// A valid token presented from off-host is refused: the token is unforgeable
// but not un-stealable, so it must not be replayable over the network.
func TestCredProxyRoute_LoopbackOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{credProxySvc: service.NewCredProxyService(testKeyringSrv(t), nil, nil)}
	r := gin.New()
	r.Any("/cred-proxy/:token", s.credProxyForward)
	req := httptest.NewRequest(http.MethodPost, "/cred-proxy/ncp_whatever", nil)
	req.RemoteAddr = "203.0.113.9:41234"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req) // no ReverseProxy on this path, so a recorder is safe
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "LOOPBACK_ONLY")
}

// A valid token whose target no longer resolves is 403, not 401 — the
// difference between "your config is stale" and "the credential is gone".
func TestCredProxyRoute_UnavailableTarget(t *testing.T) {
	srv, proxy := newCredProxyTestServer(t, "", nil)
	proxied, err := proxy.Issue(1, "kb")
	require.NoError(t, err)
	code, body := postProxy(t, srv, proxied[len("http://127.0.0.1:3000"):])
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, body, "TARGET_UNAVAILABLE")
}

func TestCredProxyRoute_UnavailableWithoutService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &Server{}
	r := gin.New()
	r.Any("/cred-proxy/:token", s.credProxyForward)
	srv := httptest.NewServer(r)
	defer srv.Close()
	code, _ := postProxy(t, srv, "/cred-proxy/x")
	assert.Equal(t, http.StatusServiceUnavailable, code)
}
