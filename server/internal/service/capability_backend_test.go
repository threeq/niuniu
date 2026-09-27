package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/require"
)

const capabilityTestModule = "video-gen"

func setupCapabilityBackendTest(t *testing.T) *service.CapabilityBackendService {
	t.Helper()
	prev := store.Driver
	store.Driver = "sqlite"
	t.Cleanup(func() { store.Driver = prev })
	db := testutil.SetupTestDB(t)
	return service.NewCapabilityBackendService(store.New(db), db)
}

func createCapabilityBackend(t *testing.T, svc *service.CapabilityBackendService, in service.CapabilityBackend) *service.CapabilityBackend {
	t.Helper()
	got, err := svc.Create(context.Background(), in)
	require.NoError(t, err)
	return got
}

// TestCapabilityBackendCRUD walks the whole create → list → get → update →
// delete loop, including the two key-update semantics: apiKeySet=false keeps
// the stored credential, apiKeySet=true replaces it.
func TestCapabilityBackendCRUD(t *testing.T) {
	ctx := context.Background()
	svc := setupCapabilityBackendTest(t)

	created := createCapabilityBackend(t, svc, service.CapabilityBackend{
		OwnerType: "user", OwnerID: 7, Module: capabilityTestModule, Capability: "tts",
		Backend: "openai-compat", Name: "我的TTS", BaseURL: "https://tts.example.com/v1",
		APIKey: "sk-1", ExtraConfig: map[string]string{"model": "tts-1"}, Enabled: true, Position: 2,
	})
	require.NotZero(t, created.ID)
	require.Equal(t, "sk-1", created.APIKey)
	require.Equal(t, map[string]string{"model": "tts-1"}, created.ExtraConfig)
	require.True(t, created.Enabled)

	got, err := svc.Get(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, *created, *got)

	byOwner, err := svc.List(ctx, "user", 7, capabilityTestModule)
	require.NoError(t, err)
	require.Len(t, byOwner, 1)
	require.Equal(t, created.ID, byOwner[0].ID)

	all, err := svc.List(ctx, "", 0, capabilityTestModule)
	require.NoError(t, err)
	require.Len(t, all, 1, "owner-less list covers every owner")

	other, err := svc.List(ctx, "", 0, "some-other-module")
	require.NoError(t, err)
	require.Empty(t, other, "list is scoped to the module")

	// apiKeySet=false (dialog never received the key) must keep it.
	updated, err := svc.Update(ctx, created.ID, service.CapabilityBackend{
		Module: capabilityTestModule, Capability: "tts", Backend: "openai-compat",
		Name: "我的TTS-2", BaseURL: "https://tts2.example.com/v1",
		ExtraConfig: map[string]string{"model": "tts-2"}, Enabled: false, Position: 3,
	}, false)
	require.NoError(t, err)
	require.Equal(t, "sk-1", updated.APIKey, "apiKeySet=false must preserve the stored key")
	require.Equal(t, "我的TTS-2", updated.Name)
	require.Equal(t, "https://tts2.example.com/v1", updated.BaseURL)
	require.Equal(t, map[string]string{"model": "tts-2"}, updated.ExtraConfig)
	require.False(t, updated.Enabled)
	require.Equal(t, 3, updated.Position)

	// apiKeySet=true replaces it.
	updated, err = svc.Update(ctx, created.ID, service.CapabilityBackend{
		Module: capabilityTestModule, Capability: "tts", Backend: "openai-compat",
		Name: "我的TTS-2", BaseURL: "https://tts2.example.com/v1", APIKey: "sk-2",
		ExtraConfig: map[string]string{"model": "tts-2"}, Enabled: true, Position: 3,
	}, true)
	require.NoError(t, err)
	require.Equal(t, "sk-2", updated.APIKey)

	require.NoError(t, svc.Delete(ctx, created.ID))
	_, err = svc.Get(ctx, created.ID)
	require.Error(t, err, "deleted rows are gone")
	empty, err := svc.List(ctx, "", 0, capabilityTestModule)
	require.NoError(t, err)
	require.Empty(t, empty)
}

// resolveCase is one ResolveEnv scenario: rows to seed plus the exact env the
// resolution must produce.
type resolveCase struct {
	name string
	rows []service.CapabilityBackend
	want map[string]string
}

func resolveRows(rows ...service.CapabilityBackend) []service.CapabilityBackend {
	for i := range rows {
		if rows[i].OwnerType == "" {
			rows[i].OwnerType = "user"
		}
		if rows[i].OwnerID == 0 {
			rows[i].OwnerID = 7
		}
		if rows[i].Module == "" {
			rows[i].Module = capabilityTestModule
		}
		rows[i].Backend = "openai-compat"
		rows[i].Enabled = true
		rows[i].Name = fmt.Sprintf("backend-%d-%d", rows[i].OwnerID, i)
	}
	return rows
}

// disabled turns a seeded row's enabled flag off (resolveRows defaults to on).
func disabled(b service.CapabilityBackend) service.CapabilityBackend {
	b.Enabled = false
	return b
}

// TestCapabilityResolveEnv pins the resolution contract: per capability the
// workspace row (owner_id = workspace) beats the user global row (owner_id =
// user) regardless of position; within one owner scope the enabled row with
// the lowest position wins; the result is NN_CAP_<CAP>_* with extra_config
// keys upper-cased under the same prefix and the API key omitted when empty.
func TestCapabilityResolveEnv(t *testing.T) {
	tests := []resolveCase{
		{
			name: "workspace row beats user global even at a higher position",
			rows: resolveRows(
				func() service.CapabilityBackend {
					r := service.CapabilityBackend{OwnerType: "user", OwnerID: 7, Capability: "tts", BaseURL: "https://global/v1", APIKey: "global-key", Position: 0}
					return r
				}(),
				service.CapabilityBackend{OwnerType: "workspace", OwnerID: 42, Capability: "tts", BaseURL: "https://ws/v1", APIKey: "ws-key", Position: 9},
			),
			want: map[string]string{
				"NN_CAP_TTS_BACKEND":  "openai-compat",
				"NN_CAP_TTS_BASE_URL": "https://ws/v1",
				"NN_CAP_TTS_API_KEY":  "ws-key",
			},
		},
		{
			name: "lowest position wins within one owner scope",
			rows: resolveRows(
				service.CapabilityBackend{OwnerType: "user", OwnerID: 7, Capability: "video", BaseURL: "https://late", APIKey: "late-key", Position: 5},
				service.CapabilityBackend{OwnerType: "user", OwnerID: 7, Capability: "video", BaseURL: "https://early", APIKey: "early-key", Position: 1},
			),
			want: map[string]string{
				"NN_CAP_VIDEO_BACKEND":  "openai-compat",
				"NN_CAP_VIDEO_BASE_URL": "https://early",
				"NN_CAP_VIDEO_API_KEY":  "early-key",
			},
		},
		{
			name: "disabled workspace row falls back to the user global row",
			rows: []service.CapabilityBackend{
				disabled(resolveRows(service.CapabilityBackend{OwnerType: "workspace", OwnerID: 42, Capability: "image", BaseURL: "https://ws-off", APIKey: "ws-key", Position: 0})[0]),
				resolveRows(service.CapabilityBackend{OwnerType: "user", OwnerID: 7, Capability: "image", BaseURL: "https://global", APIKey: "global-key", Position: 4})[0],
			},
			want: map[string]string{
				"NN_CAP_IMAGE_BACKEND":  "openai-compat",
				"NN_CAP_IMAGE_BASE_URL": "https://global",
				"NN_CAP_IMAGE_API_KEY":  "global-key",
			},
		},
		{
			name: "api key omitted when the row has none",
			rows: resolveRows(
				service.CapabilityBackend{OwnerType: "workspace", OwnerID: 42, Capability: "tts", BaseURL: "http://localhost:8080/v1", Position: 0},
			),
			want: map[string]string{
				"NN_CAP_TTS_BACKEND":  "openai-compat",
				"NN_CAP_TTS_BASE_URL": "http://localhost:8080/v1",
			},
		},
		{
			name: "extra config keys become NN_CAP_<CAP>_<KEY> upper-cased",
			rows: resolveRows(
				service.CapabilityBackend{OwnerType: "user", OwnerID: 7, Capability: "tts", BaseURL: "https://tts/v1", APIKey: "k", Position: 0,
					ExtraConfig: map[string]string{"model": "tts-1", "voice": "alloy"}},
			),
			want: map[string]string{
				"NN_CAP_TTS_BACKEND":  "openai-compat",
				"NN_CAP_TTS_BASE_URL": "https://tts/v1",
				"NN_CAP_TTS_API_KEY":  "k",
				"NN_CAP_TTS_MODEL":    "tts-1",
				"NN_CAP_TTS_VOICE":    "alloy",
			},
		},
		{
			name: "each capability resolves independently",
			rows: resolveRows(
				service.CapabilityBackend{OwnerType: "user", OwnerID: 7, Capability: "tts", BaseURL: "https://tts/v1", APIKey: "tts-key", Position: 0},
				service.CapabilityBackend{OwnerType: "user", OwnerID: 7, Capability: "image", BaseURL: "https://image/v1", APIKey: "img-key", Position: 0},
				service.CapabilityBackend{OwnerType: "workspace", OwnerID: 42, Capability: "video", BaseURL: "https://video/v1", APIKey: "vid-key", Position: 0},
			),
			want: map[string]string{
				"NN_CAP_TTS_BACKEND":    "openai-compat",
				"NN_CAP_TTS_BASE_URL":   "https://tts/v1",
				"NN_CAP_TTS_API_KEY":    "tts-key",
				"NN_CAP_IMAGE_BACKEND":  "openai-compat",
				"NN_CAP_IMAGE_BASE_URL": "https://image/v1",
				"NN_CAP_IMAGE_API_KEY":  "img-key",
				"NN_CAP_VIDEO_BACKEND":  "openai-compat",
				"NN_CAP_VIDEO_BASE_URL": "https://video/v1",
				"NN_CAP_VIDEO_API_KEY":  "vid-key",
			},
		},
		{
			name: "other owners and other modules are ignored",
			rows: resolveRows(
				service.CapabilityBackend{OwnerType: "user", OwnerID: 8, Capability: "tts", BaseURL: "https://other-user/v1", Position: 0},
				service.CapabilityBackend{OwnerType: "workspace", OwnerID: 43, Capability: "tts", BaseURL: "https://other-ws/v1", Position: 0},
				service.CapabilityBackend{OwnerType: "user", OwnerID: 7, Module: "some-other-module", Capability: "tts", BaseURL: "https://other-module/v1", Position: 0},
				service.CapabilityBackend{OwnerType: "user", OwnerID: 7, Capability: "tts", BaseURL: "https://mine/v1", APIKey: "mine-key", Position: 2},
			),
			want: map[string]string{
				"NN_CAP_TTS_BACKEND":  "openai-compat",
				"NN_CAP_TTS_BASE_URL": "https://mine/v1",
				"NN_CAP_TTS_API_KEY":  "mine-key",
			},
		},
		{
			name: "no configured backends yields an empty env",
			want: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := setupCapabilityBackendTest(t)
			for _, row := range tt.rows {
				createCapabilityBackend(t, svc, row)
			}
			env, err := svc.ResolveEnv(context.Background(), 7, 42, capabilityTestModule)
			require.NoError(t, err)
			require.Equal(t, tt.want, env)
		})
	}
}

// TestCapabilityResolveEnvSkipsDisabledRows guards the enabled gate on its own:
// a disabled workspace row must not win, and a disabled user row must not be
// picked when the workspace has nothing either.
func TestCapabilityResolveEnvSkipsDisabledRows(t *testing.T) {
	svc := setupCapabilityBackendTest(t)
	rows := resolveRows(
		service.CapabilityBackend{OwnerType: "workspace", OwnerID: 42, Capability: "tts", BaseURL: "https://ws-off", APIKey: "ws-key", Position: 0},
		service.CapabilityBackend{OwnerType: "user", OwnerID: 7, Capability: "tts", BaseURL: "https://global-off", APIKey: "global-key", Position: 0},
	)
	for _, row := range rows {
		row.Enabled = false
		createCapabilityBackend(t, svc, row)
	}
	env, err := svc.ResolveEnv(context.Background(), 7, 42, capabilityTestModule)
	require.NoError(t, err)
	require.Equal(t, map[string]string{}, env)
}

// TestCapabilityModuleRegistry pins the module registry shape flow E renders
// from: video-gen / 视频创作 with a JSON config schema whose capabilities are
// tts (openai-compat), image (openai-compat) and video (seedance + kling),
// every entry labelled.
func TestCapabilityModuleRegistry(t *testing.T) {
	modules := service.ListCapabilityModules()
	require.Len(t, modules, 1)
	m := modules[0]
	require.Equal(t, "video-gen", m.Name)
	require.Equal(t, "视频创作", m.DisplayName)
	require.Contains(t, m.Scenes, "media-studio")
	require.True(t, json.Valid(m.ConfigSchema), "config schema must be embedded JSON")

	var schema struct {
		Capabilities []struct {
			Key      string `json:"key"`
			Label    string `json:"label"`
			Backends []struct {
				Value  string `json:"value"`
				Label  string `json:"label"`
				Fields []struct {
					Key   string `json:"key"`
					Label string `json:"label"`
					Type  string `json:"type"`
				} `json:"fields"`
			} `json:"backends"`
		} `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal(m.ConfigSchema, &schema))

	backends := map[string][]string{}
	secretFields := map[string]bool{}
	for _, c := range schema.Capabilities {
		require.NotEmpty(t, c.Label, "capability %q needs a label", c.Key)
		require.NotEmpty(t, c.Backends, "capability %q needs backends", c.Key)
		for _, b := range c.Backends {
			backends[c.Key] = append(backends[c.Key], b.Value)
			require.NotEmpty(t, b.Label, "backend %q needs a label", b.Value)
			require.NotEmpty(t, b.Fields, "backend %q needs fields", b.Value)
			for _, f := range b.Fields {
				require.NotEmpty(t, f.Key)
				require.NotEmpty(t, f.Label)
				require.NotEmpty(t, f.Type)
				if f.Key == "api_key" {
					secretFields[c.Key] = f.Type == "secret"
				}
			}
		}
	}
	require.ElementsMatch(t, []string{"openai-compat"}, backends["tts"])
	require.ElementsMatch(t, []string{"openai-compat"}, backends["image"])
	require.ElementsMatch(t, []string{"seedance", "kling"}, backends["video"])
	require.Equal(t, map[string]bool{"tts": true, "image": true, "video": true}, secretFields,
		"every capability must declare api_key as a secret field")
}

// TestCapabilityBackendsAPIContract drives the REST layer (§1.1) and pins the
// masking rule: responses carry has_api_key, never the plaintext credential,
// and a PUT without api_key keeps the stored one.
func TestCapabilityBackendsAPIContract(t *testing.T) {
	svc := setupCapabilityBackendTest(t)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := api.NewCapabilityBackendHandler(svc)
	router.GET("/api/capability-modules", h.ListModules)
	router.GET("/api/capability-backends", h.List)
	router.POST("/api/capability-backends", h.Create)
	router.PUT("/api/capability-backends/:id", h.Update)

	// Module registry envelope: { "modules": [...] }.
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/capability-modules", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var modulesResp struct {
		Modules []service.CapabilityModule `json:"modules"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &modulesResp))
	require.Len(t, modulesResp.Modules, 1)
	require.Equal(t, "video-gen", modulesResp.Modules[0].Name)

	const secret = "sk-plaintext-secret"
	body := `{"module":"video-gen","capability":"tts","backend":"openai-compat",` +
		`"name":"我的TTS","base_url":"https://tts.example.com/v1","api_key":"` + secret + `",` +
		`"extra_config":{"model":"tts-1"}}`
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/capability-backends", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	require.NotContains(t, w.Body.String(), secret, "the create response must not echo the key")

	var created api.CapabilityBackendResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.NotZero(t, created.ID)
	require.Equal(t, "video-gen", created.Module)
	require.Equal(t, "tts", created.Capability)
	require.Equal(t, "openai-compat", created.Backend)
	require.Equal(t, "我的TTS", created.Name)
	require.Equal(t, "https://tts.example.com/v1", created.BaseURL)
	require.True(t, created.HasAPIKey)
	require.Equal(t, map[string]string{"model": "tts-1"}, created.ExtraConfig)
	require.True(t, created.Enabled)

	// List envelope: { "backends": [...] } — still no plaintext key.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/capability-backends?module=video-gen", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), secret)
	var listResp struct {
		Backends []api.CapabilityBackendResponse `json:"backends"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listResp))
	require.Len(t, listResp.Backends, 1)
	require.True(t, listResp.Backends[0].HasAPIKey)

	// PUT without api_key keeps the stored credential.
	putBody := `{"module":"video-gen","capability":"tts","backend":"openai-compat",` +
		`"name":"我的TTS-2","base_url":"https://tts2.example.com/v1","extra_config":{"model":"tts-2"}}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/capability-backends/%d", created.ID), strings.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), secret)
	require.Contains(t, w.Body.String(), `"has_api_key":true`)

	stored, err := svc.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, secret, stored.APIKey, "an omitted api_key must not clear the stored credential")
	require.Equal(t, "我的TTS-2", stored.Name)

	// Missing module filter is a client error.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/capability-backends", nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
}
