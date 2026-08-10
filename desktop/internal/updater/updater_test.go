package updater_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/niuniu-dev/niuniu-desktop/internal/updater"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckUpdate_NewVersionAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tag_name": "v1.2.0",
			"html_url": "https://github.com/test/releases/tag/v1.2.0",
			"assets": []map[string]interface{}{
				{"name": "niuniu-desktop-windows-amd64.msi", "browser_download_url": "https://example.com/win.msi"},
			},
		})
	}))
	defer srv.Close()
	u := updater.New("v1.0.0", srv.URL)
	result, err := u.Check()
	require.NoError(t, err)
	assert.True(t, result.Available)
	assert.Equal(t, "v1.2.0", result.Version)
}

func TestCheckUpdate_AlreadyLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"tag_name": "v1.0.0"})
	}))
	defer srv.Close()
	u := updater.New("v1.0.0", srv.URL)
	result, err := u.Check()
	require.NoError(t, err)
	assert.False(t, result.Available)
}

func TestCheckUpdate_SkippedVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"tag_name": "v1.2.0"})
	}))
	defer srv.Close()
	u := updater.New("v1.0.0", srv.URL)
	u.SetSkipped("v1.2.0")
	result, err := u.Check()
	require.NoError(t, err)
	assert.False(t, result.Available)
}
