package probe

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func startHealthServer(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/health") {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return "127.0.0.1:" + u.Port()
}

func TestCheckHealthOK(t *testing.T) {
	addr := startHealthServer(t, 200, `{"version":"v1.2.3"}`)
	h, err := checkHealth(addr)
	require.NoError(t, err)
	require.Equal(t, "v1.2.3", h.Version)
}

func TestCheckHealthUnreachable(t *testing.T) {
	_, err := checkHealth("127.0.0.1:1")
	require.Error(t, err)
}

func TestCheckHealthNon200(t *testing.T) {
	addr := startHealthServer(t, 500, "err")
	_, err := checkHealth(addr)
	require.Error(t, err)
}

func TestVersionCompatible(t *testing.T) {
	require.True(t, versionCompatible("v1.2.3", "v1.2.4"), "patch diff OK")
	require.True(t, versionCompatible("v1.2.3", "v1.2.3"), "exact match OK")
	require.False(t, versionCompatible("v1.2.0", "v2.0.0"), "major diff blocks")
	require.False(t, versionCompatible("v1.2.0", "v1.3.0"), "minor diff blocks")
	require.False(t, versionCompatible("dev", "v1.2.0"), "unparseable blocks")
	require.Equal(t, true, versionCompatible("", ""), "both empty = permissive dev default")
	_ = strconv.Itoa // keep strconv import if present
}
