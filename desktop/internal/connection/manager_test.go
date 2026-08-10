package connection_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/niuniu-dev/niuniu-desktop/internal/connection"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_CheckHealth_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok","version":"dev","uptime_seconds":42}`))
		}
	}))
	defer srv.Close()
	mgr := connection.NewManager()
	health, err := mgr.CheckHealth(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "ok", health.Status)
	assert.Equal(t, "dev", health.Version)
	assert.Equal(t, 42, health.UptimeSeconds)
}

func TestManager_CheckHealth_Failure(t *testing.T) {
	mgr := connection.NewManager()
	_, err := mgr.CheckHealth("http://localhost:1")
	assert.Error(t, err)
}

func TestManager_BuildURL(t *testing.T) {
	assert.Equal(t, "http://localhost:3000", connection.BuildURL("localhost", 3000))
	assert.Equal(t, "http://192.168.1.5:8080", connection.BuildURL("192.168.1.5", 8080))
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		// bare host + explicit port (legacy LAN form)
		{"192.168.1.5", 3000, "http://192.168.1.5:3000"},
		// domain, no port → http default, no port appended
		{"niuniu.example.com", 0, "http://niuniu.example.com"},
		// full https URL with explicit :443 → default port dropped
		{"https://niuniu.dujiaoshou.pro:443", 443, "https://niuniu.dujiaoshou.pro"},
		// full https URL, no port → scheme honored, no port
		{"https://niuniu.example.com", 0, "https://niuniu.example.com"},
		// https URL with a non-default port → kept
		{"https://niuniu.example.com:8443", 0, "https://niuniu.example.com:8443"},
		// scheme-less host but port 443 → https inferred
		{"niuniu.example.com", 443, "https://niuniu.example.com"},
		// full URL with a trailing path → origin only
		{"http://x.example.com:8080/foo", 0, "http://x.example.com:8080"},
		// host:port embedded, port arg ignored when host already has one
		{"192.168.1.5:9000", 3000, "http://192.168.1.5:9000"},
		// trailing slash tolerated
		{"https://niuniu.example.com/", 0, "https://niuniu.example.com"},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, connection.NormalizeBaseURL(c.host, c.port),
			"NormalizeBaseURL(%q,%d)", c.host, c.port)
	}
}

func TestKeyFor(t *testing.T) {
	assert.Equal(t, "192.168.1.5:3000", connection.KeyFor("192.168.1.5", 3000))
	assert.Equal(t, "niuniu.dujiaoshou.pro", connection.KeyFor("https://niuniu.dujiaoshou.pro:443", 443))
	assert.Equal(t, "niuniu.example.com", connection.KeyFor("niuniu.example.com", 0))
	// Same origin via different input forms shares a key.
	assert.Equal(t, connection.KeyFor("https://niuniu.example.com", 0),
		connection.KeyFor("https://niuniu.example.com/", 0))
}
