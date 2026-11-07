package api_test

// HTTP contract for the intake binding management API (design §10.1) and the
// one-time secret link. These are exercised through the real handler because
// the status codes (403/409/400/410/401) are the contract, not an internal
// detail: the settings UI and the MCP tools both key off them.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

type intakeSourceAPI struct {
	env     *niutest.IsolationEnv
	router  *gin.Engine
	project int64
	creds   *service.ExternalCredentialService
}

func setupIntakeSourceAPI(t *testing.T) *intakeSourceAPI {
	t.Helper()
	gin.SetMode(gin.TestMode)
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	require.NoError(t, err)
	creds := service.NewExternalCredentialService(env.Queries(), env.DB, kr, integration.NewRegistry())
	authz := service.NewAuthz(env.Queries(), env.DB)
	sources := service.NewIntakeSourceService(env.DB, env.Queries(), creds, authz, nil)

	h := api.NewIntakeHandler(nil, authz)
	h.Sources = sources

	r := gin.New()
	r.Use(func(c *gin.Context) {
		if v := c.GetHeader("X-Test-Caller-ID"); v != "" {
			id, _ := strconv.ParseInt(v, 10, 64)
			c.Set("auth_user_id", id)
		}
		c.Next()
	})
	// Public one-time link resolution.
	r.GET("/api/intake/sources/secret/:token", h.ResolveIntakeSecret)
	// Authenticated management surface.
	r.GET("/api/projects/:id/intake/sources", h.ListSources)
	r.POST("/api/projects/:id/intake/sources", h.CreateSource)
	r.POST("/api/projects/:id/intake/sources/:sid/rotate", h.RotateSource)
	r.POST("/api/projects/:id/intake/sources/:sid/test", h.TestSource)
	r.POST("/api/projects/:id/intake/sources/:sid/poll", h.PollSource)
	r.DELETE("/api/projects/:id/intake/sources/:sid", h.DeleteSource)
	// Legacy alias.
	r.GET("/api/projects/:id/intake/webhook", h.ListWebhookBindings)
	r.POST("/api/projects/:id/intake/webhook", h.CreateWebhookBinding)
	r.DELETE("/api/projects/:id/intake/webhook/:bid", h.DeleteWebhookBinding)
	// Inbound delivery, so rotation can be proven end to end.
	inbound := api.NewIntegrationWebhookHandler(service.NewWebhookSourceService(env.DB, env.Queries(), creds))
	r.POST("/api/integrations/webhook/:binding", inbound.Webhook)

	project := env.NewProject(t, env.UserA, "intake-sources-api")
	return &intakeSourceAPI{env: env, router: r, project: project.ID, creds: creds}
}

func (f *intakeSourceAPI) do(t *testing.T, method, path, body string, userID int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Caller-ID", strconv.FormatInt(userID, 10))
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func (f *intakeSourceAPI) sourcesPath() string {
	return "/api/projects/" + strconv.FormatInt(f.project, 10) + "/intake/sources"
}

func (f *intakeSourceAPI) sourcePath(id int64, action string) string {
	p := f.sourcesPath() + "/" + strconv.FormatInt(id, 10)
	if action != "" {
		p += "/" + action
	}
	return p
}

// createWebhook creates a generic webhook binding as UserA and returns its id
// and the one-time link.
func (f *intakeSourceAPI) createWebhook(t *testing.T, name string) (int64, string) {
	t.Helper()
	w := f.do(t, http.MethodPost, f.sourcesPath(),
		`{"source_kind":"webhook","name":"`+name+`"}`, f.env.UserA)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var body struct {
		Source struct {
			ID int64 `json:"id"`
		} `json:"source"`
		SecretLink string `json:"secret_link"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.NotZero(t, body.Source.ID)
	require.NotEmpty(t, body.SecretLink)
	return body.Source.ID, body.SecretLink
}

// redeem opens a secret link and returns the plaintext secret.
func (f *intakeSourceAPI) redeem(t *testing.T, link string) *httptest.ResponseRecorder {
	t.Helper()
	// The link is a page path; the JSON endpoint that consumes it is /api + link.
	return f.do(t, http.MethodGet, "/api"+link, "", f.env.UserA)
}

func (f *intakeSourceAPI) deliver(t *testing.T, bindingID int64, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/integrations/webhook/"+strconv.FormatInt(bindingID, 10), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Token", secret)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func TestIntakeSourceAPI_CreateReturnsLinkNotSecret(t *testing.T) {
	f := setupIntakeSourceAPI(t)

	w := f.do(t, http.MethodPost, f.sourcesPath(),
		`{"source_kind":"ci","name":"gitlab-main","mode":"push","provider":"gitlab"}`, f.env.UserA)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	var created map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.NotContains(t, created, "secret", "the plaintext secret must never be returned inline")
	link, _ := created["secret_link"].(string)
	assert.True(t, strings.HasPrefix(link, "/intake/sources/secret/"), "link = %q", link)
	source := created["source"].(map[string]any)
	assert.Equal(t, "ci", source["source_kind"])
	assert.Equal(t, "gitlab", source["provider"])
	assert.Equal(t, true, source["has_secret"])

	list := f.do(t, http.MethodGet, f.sourcesPath(), "", f.env.UserA)
	require.Equal(t, http.StatusOK, list.Code)
	var listed struct {
		Sources []map[string]any `json:"sources"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &listed))
	require.Len(t, listed.Sources, 1)
	assert.Equal(t, "gitlab-main", listed.Sources[0]["name"])
}

func TestIntakeSourceAPI_OneTimeLinkShowsSecretOnce(t *testing.T) {
	f := setupIntakeSourceAPI(t)
	_, link := f.createWebhook(t, "hooks")

	first := f.redeem(t, link)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	assert.Equal(t, "no-store, no-cache, must-revalidate, private", first.Header().Get("Cache-Control"))
	var shown struct {
		Secret      string `json:"secret"`
		InboundPath string `json:"inbound_path"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &shown))
	require.NotEmpty(t, shown.Secret)
	assert.Contains(t, shown.InboundPath, "/api/integrations/webhook/")

	// A refresh (or a second reader of a leaked link) gets nothing.
	second := f.redeem(t, link)
	assert.Equal(t, http.StatusGone, second.Code)
	assert.NotContains(t, second.Body.String(), shown.Secret)

	unknown := f.do(t, http.MethodGet, "/api/intake/sources/secret/not-a-token", "", f.env.UserA)
	assert.Equal(t, http.StatusGone, unknown.Code)
}

func TestIntakeSourceAPI_CrossOwnerForbidden(t *testing.T) {
	f := setupIntakeSourceAPI(t)
	bindingID, _ := f.createWebhook(t, "hooks")

	// UserB cannot even see UserA's project, let alone its bindings.
	list := f.do(t, http.MethodGet, f.sourcesPath(), "", f.env.UserB)
	assert.Equal(t, http.StatusForbidden, list.Code)

	create := f.do(t, http.MethodPost, f.sourcesPath(),
		`{"source_kind":"webhook","name":"sneaky"}`, f.env.UserB)
	assert.Equal(t, http.StatusForbidden, create.Code)

	assert.Equal(t, http.StatusForbidden,
		f.do(t, http.MethodPost, f.sourcePath(bindingID, "rotate"), "", f.env.UserB).Code)
	assert.Equal(t, http.StatusForbidden,
		f.do(t, http.MethodPost, f.sourcePath(bindingID, "test"), "", f.env.UserB).Code)
	assert.Equal(t, http.StatusForbidden,
		f.do(t, http.MethodPost, f.sourcePath(bindingID, "poll"), "", f.env.UserB).Code)
	assert.Equal(t, http.StatusForbidden,
		f.do(t, http.MethodDelete, f.sourcePath(bindingID, ""), "", f.env.UserB).Code)

	// The rejected delete left the binding in place.
	list = f.do(t, http.MethodGet, f.sourcesPath(), "", f.env.UserA)
	require.Equal(t, http.StatusOK, list.Code)
	var listed struct {
		Sources []map[string]any `json:"sources"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &listed))
	assert.Len(t, listed.Sources, 1)
}

func TestIntakeSourceAPI_DuplicateNameConflict(t *testing.T) {
	f := setupIntakeSourceAPI(t)
	f.createWebhook(t, "hooks")

	dup := f.do(t, http.MethodPost, f.sourcesPath(),
		`{"source_kind":"webhook","name":"hooks"}`, f.env.UserA)
	assert.Equal(t, http.StatusConflict, dup.Code, dup.Body.String())

	// The legacy alias shares the same namespace.
	legacyDup := f.do(t, http.MethodPost,
		"/api/projects/"+strconv.FormatInt(f.project, 10)+"/intake/webhook",
		`{"source_kind":"webhook","name":"hooks"}`, f.env.UserA)
	assert.Equal(t, http.StatusConflict, legacyDup.Code)
}

func TestIntakeSourceAPI_ImapRequiresCredential(t *testing.T) {
	f := setupIntakeSourceAPI(t)
	w := f.do(t, http.MethodPost, f.sourcesPath(),
		`{"source_kind":"email","name":"support","mode":"imap"}`, f.env.UserA)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "credential_id")

	// Nothing was written when creation failed.
	list := f.do(t, http.MethodGet, f.sourcesPath(), "", f.env.UserA)
	var listed struct {
		Sources []map[string]any `json:"sources"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &listed))
	assert.Empty(t, listed.Sources)
}

func TestIntakeSourceAPI_TestDeliveryPersistsNothing(t *testing.T) {
	f := setupIntakeSourceAPI(t)
	w := f.do(t, http.MethodPost, f.sourcesPath(),
		`{"source_kind":"ci","name":"gitlab-main","provider":"gitlab"}`, f.env.UserA)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created struct {
		Source struct {
			ID int64 `json:"id"`
		} `json:"source"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	test := f.do(t, http.MethodPost, f.sourcePath(created.Source.ID, "test"),
		`{"sample":{"object_attributes":{"status":"failed"}}}`, f.env.UserA)
	require.Equal(t, http.StatusOK, test.Code, test.Body.String())
	var result map[string]any
	require.NoError(t, json.Unmarshal(test.Body.Bytes(), &result))
	assert.Equal(t, "ci", result["source_kind"])
	assert.Contains(t, result, "allowed")
	assert.Contains(t, result, "mode")

	ctx := context.Background()
	events, err := f.env.Queries().ListIntakeEventsByProject(ctx, store.ListIntakeEventsByProjectParams{
		ProjectID: f.project,
		Limit:     20,
	})
	require.NoError(t, err)
	assert.Empty(t, events, "a test delivery must not record an intake event")

	var issues int
	require.NoError(t, f.env.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM issues i JOIN columns c ON c.id = i.column_id WHERE c.project_id = ?`,
		f.project).Scan(&issues))
	assert.Zero(t, issues, "a test delivery must not create a task")
}

func TestIntakeSourceAPI_RotationInvalidatesOldSecret(t *testing.T) {
	f := setupIntakeSourceAPI(t)
	bindingID, link := f.createWebhook(t, "hooks")

	first := f.redeem(t, link)
	require.Equal(t, http.StatusOK, first.Code)
	var shown struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &shown))
	oldSecret := shown.Secret
	require.NotEmpty(t, oldSecret)

	// The generated secret is what the inbound path accepts.
	require.Equal(t, http.StatusOK, f.deliver(t, bindingID, oldSecret, `{"text":"before rotate"}`).Code)

	rotate := f.do(t, http.MethodPost, f.sourcePath(bindingID, "rotate"), "", f.env.UserA)
	require.Equal(t, http.StatusOK, rotate.Code, rotate.Body.String())
	var rotated struct {
		SecretLink string `json:"secret_link"`
	}
	require.NoError(t, json.Unmarshal(rotate.Body.Bytes(), &rotated))
	require.NotEmpty(t, rotated.SecretLink)
	require.NotEqual(t, link, rotated.SecretLink)

	second := f.redeem(t, rotated.SecretLink)
	require.Equal(t, http.StatusOK, second.Code)
	var rotatedSecret struct {
		Secret string `json:"secret"`
	}
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &rotatedSecret))
	require.NotEqual(t, oldSecret, rotatedSecret.Secret)

	assert.Equal(t, http.StatusUnauthorized,
		f.deliver(t, bindingID, oldSecret, `{"text":"old secret"}`).Code,
		"the previous secret must stop working immediately")
	assert.Equal(t, http.StatusOK,
		f.deliver(t, bindingID, rotatedSecret.Secret, `{"text":"new secret"}`).Code)
}

func TestIntakeSourceAPI_DeleteCascadesCredential(t *testing.T) {
	f := setupIntakeSourceAPI(t)
	bindingID, _ := f.createWebhook(t, "hooks")
	ctx := context.Background()

	creds, err := f.creds.ListByProvider(ctx, "user", f.env.UserA, f.env.UserA,
		integration.ProviderName("webhook"))
	require.NoError(t, err)
	require.Len(t, creds, 1, "creating a binding writes exactly one generated credential")

	del := f.do(t, http.MethodDelete, f.sourcePath(bindingID, ""), "", f.env.UserA)
	require.Equal(t, http.StatusOK, del.Code, del.Body.String())

	list := f.do(t, http.MethodGet, f.sourcesPath(), "", f.env.UserA)
	var listed struct {
		Sources []map[string]any `json:"sources"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &listed))
	assert.Empty(t, listed.Sources)

	creds, err = f.creds.ListByProvider(ctx, "user", f.env.UserA, f.env.UserA,
		integration.ProviderName("webhook"))
	require.NoError(t, err)
	assert.Empty(t, creds, "deleting a binding must clean up the credential it owns")
}

func TestIntakeSourceAPI_PollRequiresImapBinding(t *testing.T) {
	f := setupIntakeSourceAPI(t)
	w := f.do(t, http.MethodPost, f.sourcesPath(),
		`{"source_kind":"ci","name":"gitlab-main","provider":"gitlab"}`, f.env.UserA)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created struct {
		Source struct {
			ID int64 `json:"id"`
		} `json:"source"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	poll := f.do(t, http.MethodPost, f.sourcePath(created.Source.ID, "poll"), "", f.env.UserA)
	assert.Equal(t, http.StatusBadRequest, poll.Code, poll.Body.String())
}

func TestIntakeSourceAPI_LegacyWebhookAlias(t *testing.T) {
	f := setupIntakeSourceAPI(t)
	legacy := "/api/projects/" + strconv.FormatInt(f.project, 10) + "/intake/webhook"

	w := f.do(t, http.MethodPost, legacy, `{"source_kind":"email","name":"support"}`, f.env.UserA)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.NotContains(t, created, "secret")
	assert.NotEmpty(t, created["secret_link"])
	binding := created["binding"].(map[string]any)
	assert.Equal(t, "email", binding["source_kind"])
	assert.Equal(t, "inbound", binding["mode"])

	list := f.do(t, http.MethodGet, legacy, "", f.env.UserA)
	require.Equal(t, http.StatusOK, list.Code)
	var listed struct {
		Bindings []map[string]any `json:"bindings"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &listed))
	require.Len(t, listed.Bindings, 1)

	id := int64(binding["id"].(float64))
	del := f.do(t, http.MethodDelete, legacy+"/"+strconv.FormatInt(id, 10), "", f.env.UserA)
	assert.Equal(t, http.StatusOK, del.Code)
}
