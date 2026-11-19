// Contract tests for the shared model library, usage and budget API
// (design §11.1). Covers the merged list, CRUD, owner permissions (403),
// immutable system rows (400), refresh preview (200/422), import accounting,
// usage grouping and budget validation (409).
package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type modelFixture struct {
	r      *gin.Engine
	db     *sql.DB
	q      *store.Queries
	userA  int64
	userB  int64
	authz  *service.Authz
	budget *service.ModelBudgetService
}

func setupModelRouter(t *testing.T) *modelFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, q := niutest.SetupDBRaw(t)
	ctx := context.Background()

	uA, err := q.CreateUser(ctx, store.CreateUserParams{
		Username: "modelA", PasswordHash: "x", DisplayName: "Model A", Role: "member",
	})
	require.NoError(t, err)
	uB, err := q.CreateUser(ctx, store.CreateUserParams{
		Username: "modelB", PasswordHash: "x", DisplayName: "Model B", Role: "member",
	})
	require.NoError(t, err)

	require.NoError(t, service.SeedBuiltinModels(ctx, q))

	authz := service.NewAuthz(q, db)
	library := service.NewModelLibraryService(q)
	h := api.NewModelHandler(
		library,
		service.NewModelRefreshService(q, library),
		service.NewModelUsageService(q),
		service.NewModelBudgetService(q, nil),
	)
	h.Authz = authz

	r := gin.New()
	r.Use(func(c *gin.Context) {
		if v := c.GetHeader("X-Test-Caller-ID"); v != "" {
			c.Set("auth_user_id", atoi64(v))
		}
		c.Next()
	})
	r.GET("/api/models", h.List)
	r.POST("/api/models", h.Upsert)
	r.PATCH("/api/models/:id", h.Update)
	r.DELETE("/api/models/:id", h.Delete)
	r.POST("/api/models/refresh-preview", h.RefreshPreview)
	r.POST("/api/models/import", h.Import)
	r.GET("/api/model-usage/summary", h.UsageSummary)
	r.GET("/api/model-budget", h.GetBudget)
	r.PUT("/api/model-budget", h.SetBudget)
	r.PUT("/api/model-budget/projects/:id", h.SetProjectBudget)
	r.DELETE("/api/model-budget/projects/:id", h.DeleteProjectBudget)

	return &modelFixture{
		r: r, db: db, q: q, userA: uA.ID, userB: uB.ID, authz: authz,
		budget: service.NewModelBudgetService(q, nil),
	}
}

func modelReq(t *testing.T, r *gin.Engine, method, path, body string, callerID int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if callerID > 0 {
		req.Header.Set("X-Test-Caller-ID", strconv.FormatInt(callerID, 10))
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeModels(t *testing.T, w *httptest.ResponseRecorder) []service.ModelDTO {
	t.Helper()
	var models []service.ModelDTO
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &models), w.Body.String())
	return models
}

func findModel(t *testing.T, models []service.ModelDTO, name string) (service.ModelDTO, bool) {
	t.Helper()
	for _, m := range models {
		if m.Name == name {
			return m, true
		}
	}
	return service.ModelDTO{}, false
}

// TestModelHandler_ListMerged pins GET /api/models: system rows are always
// present, owner rows shadow same-named system rows, and disabled rows only
// appear with ?all=1.
func TestModelHandler_ListMerged(t *testing.T) {
	f := setupModelRouter(t)

	w := modelReq(t, f.r, http.MethodGet, "/api/models", "", f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	models := decodeModels(t, w)
	_, ok := findModel(t, models, "glm-5.1")
	require.True(t, ok, "seeded system model should be listed")

	// Add an owner model and hide a system model via an owner shadow row.
	w = modelReq(t, f.r, http.MethodPost, "/api/models",
		`{"name":"my-model","display_name":"My Model"}`, f.userA)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	w = modelReq(t, f.r, http.MethodPost, "/api/models",
		`{"name":"glm-5.1","display_name":"Hidden","enabled":false}`, f.userA)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	w = modelReq(t, f.r, http.MethodGet, "/api/models", "", f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	models = decodeModels(t, w)
	if m, ok := findModel(t, models, "glm-5.1"); ok {
		t.Fatalf("disabled shadow row must be hidden by default, got %+v", m)
	}
	_, ok = findModel(t, models, "my-model")
	require.True(t, ok)

	w = modelReq(t, f.r, http.MethodGet, "/api/models?all=1", "", f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	models = decodeModels(t, w)
	m, ok := findModel(t, models, "glm-5.1")
	require.True(t, ok)
	assert.False(t, m.Enabled)
	assert.Equal(t, f.userA, m.OwnerID, "the shadow row, not the system row, is returned")
}

// TestModelHandler_UpsertUpdateDelete covers create/overwrite/update/delete.
func TestModelHandler_UpsertUpdateDelete(t *testing.T) {
	f := setupModelRouter(t)

	w := modelReq(t, f.r, http.MethodPost, "/api/models", `{"name":"alpha"}`, f.userA)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created service.ModelDTO
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.Equal(t, "alpha", created.Name)
	assert.True(t, created.Enabled)

	// Duplicate name overwrites the owner row -> 200, no error.
	w = modelReq(t, f.r, http.MethodPost, "/api/models",
		`{"name":"alpha","display_name":"Alpha 2","enabled":false}`, f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	w = modelReq(t, f.r, http.MethodPatch, "/api/models/"+strconv.FormatInt(created.ID, 10),
		`{"display_name":"Renamed","enabled":true}`, f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var updated service.ModelDTO
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	assert.Equal(t, "Renamed", updated.DisplayName)
	assert.True(t, updated.Enabled)

	w = modelReq(t, f.r, http.MethodDelete, "/api/models/"+strconv.FormatInt(created.ID, 10), "", f.userA)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())

	w = modelReq(t, f.r, http.MethodPatch, "/api/models/"+strconv.FormatInt(created.ID, 10),
		`{"enabled":true}`, f.userA)
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

// TestModelHandler_SystemModelDelete400 pins the system-row contract: delete
// is rejected with 400 and a hint to hide it instead.
func TestModelHandler_SystemModelDelete400(t *testing.T) {
	f := setupModelRouter(t)

	w := modelReq(t, f.r, http.MethodGet, "/api/models", "", f.userA)
	require.Equal(t, http.StatusOK, w.Code)
	models := decodeModels(t, w)
	sys, ok := findModel(t, models, "glm-5.1")
	require.True(t, ok)
	require.EqualValues(t, 0, sys.OwnerID)

	w = modelReq(t, f.r, http.MethodDelete, "/api/models/"+strconv.FormatInt(sys.ID, 10), "", f.userA)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	w = modelReq(t, f.r, http.MethodPatch, "/api/models/"+strconv.FormatInt(sys.ID, 10),
		`{"enabled":false}`, f.userA)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// TestModelHandler_CrossOwner403 pins owner isolation: reading or mutating
// another owner's data answers 403, not 404 or silent success.
func TestModelHandler_CrossOwner403(t *testing.T) {
	f := setupModelRouter(t)

	w := modelReq(t, f.r, http.MethodPost, "/api/models", `{"name":"private-a"}`, f.userA)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created service.ModelDTO
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	// B cannot read A's library.
	w = modelReq(t, f.r, http.MethodGet,
		"/api/models?owner_type=user&owner_id="+strconv.FormatInt(f.userA, 10), "", f.userB)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	// B cannot create under A's owner.
	w = modelReq(t, f.r, http.MethodPost, "/api/models",
		`{"name":"sneaky","owner":{"type":"user","id":`+strconv.FormatInt(f.userA, 10)+`}}`, f.userB)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	// B cannot update or delete A's row by id.
	w = modelReq(t, f.r, http.MethodPatch, "/api/models/"+strconv.FormatInt(created.ID, 10),
		`{"enabled":false}`, f.userB)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	w = modelReq(t, f.r, http.MethodDelete, "/api/models/"+strconv.FormatInt(created.ID, 10), "", f.userB)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	// A still sees it.
	w = modelReq(t, f.r, http.MethodGet, "/api/models", "", f.userA)
	require.Equal(t, http.StatusOK, w.Code)
	_, ok := findModel(t, decodeModels(t, w), "private-a")
	assert.True(t, ok)
}

// TestModelHandler_OrgPermissions pins the org rules: members share the
// library, outsiders are rejected, and org usage/budget is admin-only.
func TestModelHandler_OrgPermissions(t *testing.T) {
	f := setupModelRouter(t)
	ctx := context.Background()

	org, err := f.q.CreateOrganization(ctx, store.CreateOrganizationParams{
		Slug: "model-team", Name: "Model Team", Description: "", CreatedBy: f.userA,
	})
	require.NoError(t, err)
	require.NoError(t, f.q.AddOrgMember(ctx, store.AddOrgMemberParams{
		OrgID: org.ID, UserID: f.userA, Role: "owner",
	}))
	orgOwner := `"owner":{"type":"org","id":` + strconv.FormatInt(org.ID, 10) + `}`

	// The org owner can add an org-shared model.
	w := modelReq(t, f.r, http.MethodPost, "/api/models", `{"name":"org-model",`+orgOwner+`}`, f.userA)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	// A non-member can do neither read nor write on the org owner.
	orgQuery := "/api/models?owner_type=org&owner_id=" + strconv.FormatInt(org.ID, 10)
	w = modelReq(t, f.r, http.MethodGet, orgQuery, "", f.userB)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	w = modelReq(t, f.r, http.MethodPost, "/api/models", `{"name":"sneaky",`+orgOwner+`}`, f.userB)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	// Org usage/budget is manager-only.
	usageQuery := "/api/model-usage/summary?owner_type=org&owner_id=" + strconv.FormatInt(org.ID, 10)
	w = modelReq(t, f.r, http.MethodGet, usageQuery, "", f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w = modelReq(t, f.r, http.MethodGet, usageQuery, "", f.userB)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

// TestModelHandler_RefreshPreviewAndImport pins the refresh -> import flow and
// the already_in_library annotations.
func TestModelHandler_RefreshPreviewAndImport(t *testing.T) {
	f := setupModelRouter(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.1"},{"id":"brand-new"}]}`))
	}))
	defer upstream.Close()

	provider, err := f.q.CreateEnvProvider(context.Background(), store.CreateEnvProviderParams{
		Name: "refresh-provider", Platform: "custom",
		BaseUrls: `{"openai":"` + upstream.URL + `"}`,
		Enabled:  1, OwnerType: "user", OwnerID: f.userA,
	})
	require.NoError(t, err)

	w := modelReq(t, f.r, http.MethodPost, "/api/models/refresh-preview",
		`{"provider_id":`+strconv.FormatInt(provider.ID, 10)+`}`, f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var preview struct {
		Models []service.ModelCandidate `json:"models"`
		Hint   string                   `json:"hint"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &preview))
	require.Len(t, preview.Models, 2)
	for _, c := range preview.Models {
		if c.Name == "glm-5.1" {
			assert.True(t, c.AlreadyInLibrary, "seeded system model must be flagged")
		}
		if c.Name == "brand-new" {
			assert.False(t, c.AlreadyInLibrary)
		}
	}

	w = modelReq(t, f.r, http.MethodPost, "/api/models/import",
		`{"names":["brand-new","glm-5.1","brand-new"]}`, f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var imported struct {
		Added   int `json:"added"`
		Skipped int `json:"skipped"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &imported))
	assert.Equal(t, 1, imported.Added)
	assert.Equal(t, 2, imported.Skipped)

	// A second import adds nothing.
	w = modelReq(t, f.r, http.MethodPost, "/api/models/import", `{"names":["brand-new"]}`, f.userA)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &imported))
	assert.Equal(t, 0, imported.Added)
	assert.Equal(t, 1, imported.Skipped)
}

// TestModelHandler_RefreshUnsupported422 pins the "provider has no /models"
// contract.
func TestModelHandler_RefreshUnsupported422(t *testing.T) {
	f := setupModelRouter(t)

	provider, err := f.q.CreateEnvProvider(context.Background(), store.CreateEnvProviderParams{
		Name: "no-models", Platform: "custom", BaseUrls: `{}`,
		Enabled:   1, OwnerType: "user", OwnerID: f.userA,
	})
	require.NoError(t, err)

	w := modelReq(t, f.r, http.MethodPost, "/api/models/refresh-preview",
		`{"provider_id":`+strconv.FormatInt(provider.ID, 10)+`}`, f.userA)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())

	w = modelReq(t, f.r, http.MethodPost, "/api/models/refresh-preview", `{"provider_id":99999}`, f.userA)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

// TestModelHandler_UsageSummary pins totals plus each group_by dimension.
func TestModelHandler_UsageSummary(t *testing.T) {
	f := setupModelRouter(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, row := range []store.UpsertModelUsageHourlyParams{
		{OwnerType: "user", OwnerID: f.userA, ProjectID: 2, WorkspaceID: 3,
			AgentCli: "claude", ModelName: "glm-5.1", ProviderID: 4,
			BucketHour: now, InputTokens: 100, OutputTokens: 50, InteractionCount: 2},
		{OwnerType: "user", OwnerID: f.userA, ProjectID: 2, WorkspaceID: 5,
			AgentCli: "codex", ModelName: "brand-new", ProviderID: 4,
			BucketHour: now, InputTokens: 25, InteractionCount: 1},
	} {
		require.NoError(t, f.q.UpsertModelUsageHourly(ctx, row))
	}

	// Explicit window so the assertion never races the [from,to) upper bound.
	from := now.Add(-2 * time.Hour).Format(time.RFC3339)
	to := now.Add(time.Hour).Format(time.RFC3339)
	rangeQuery := "/api/model-usage/summary?from=" + from + "&to=" + to

	w := modelReq(t, f.r, http.MethodGet, rangeQuery, "", f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var summary service.ModelUsageSummary
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &summary))
	assert.EqualValues(t, 175, summary.TotalTokens)
	assert.EqualValues(t, 3, summary.Requests)
	assert.Empty(t, summary.ByModel, "no group_by = totals only")

	w = modelReq(t, f.r, http.MethodGet, rangeQuery+"&group_by=model", "", f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &summary))
	require.Len(t, summary.ByModel, 2)
	assert.EqualValues(t, 175, summary.TotalTokens)
	assert.EqualValues(t, 3, summary.Requests)

	for _, groupBy := range []string{"provider", "project", "workspace", "cli"} {
		w = modelReq(t, f.r, http.MethodGet, rangeQuery+"&group_by="+groupBy, "", f.userA)
		require.Equal(t, http.StatusOK, w.Code, groupBy+": "+w.Body.String())
	}

	w = modelReq(t, f.r, http.MethodGet, rangeQuery+"&group_by=bogus", "", f.userA)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// Another owner's usage is invisible (and cross-owner reads are 403).
	w = modelReq(t, f.r, http.MethodGet,
		rangeQuery+"&owner_type=user&owner_id="+strconv.FormatInt(f.userA, 10), "", f.userB)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

// TestModelHandler_Budget pins read/set, the below-usage 409, unlimited (0)
// and project soft caps.
func TestModelHandler_Budget(t *testing.T) {
	f := setupModelRouter(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// The project must be accessible to the caller for the project-cap routes.
	project, err := f.q.CreateProject(ctx, store.CreateProjectParams{
		Name: "budget-proj", OwnerType: "user", OwnerID: f.userA,
	})
	require.NoError(t, err)

	w := modelReq(t, f.r, http.MethodGet, "/api/model-budget", "", f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var view service.ModelBudgetView
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &view))
	assert.EqualValues(t, 0, view.MonthlyTokens)
	assert.NotEmpty(t, view.Period)

	w = modelReq(t, f.r, http.MethodPut, "/api/model-budget", `{"monthly_tokens":1000}`, f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &view))
	assert.EqualValues(t, 1000, view.MonthlyTokens)

	// 900 tokens already used: a cap of 800 would be exceeded immediately -> 409.
	require.NoError(t, f.q.UpsertModelUsageHourly(ctx, store.UpsertModelUsageHourlyParams{
		OwnerType: "user", OwnerID: f.userA, ProjectID: project.ID, WorkspaceID: 1,
		AgentCli: "claude", ModelName: "glm-5.1", ProviderID: 1,
		BucketHour: now.Add(-time.Hour), InputTokens: 900, InteractionCount: 1,
	}))
	w = modelReq(t, f.r, http.MethodPut, "/api/model-budget", `{"monthly_tokens":800}`, f.userA)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	var conflict struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &conflict))
	assert.Equal(t, "MODEL_BUDGET_EXCEEDED", conflict.Error.Code)
	assert.EqualValues(t, 900, conflict.Error.Details["used_tokens"])

	// 0 = unlimited is always accepted, even above usage.
	w = modelReq(t, f.r, http.MethodPut, "/api/model-budget", `{"monthly_tokens":0}`, f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &view))
	assert.EqualValues(t, 0, view.MonthlyTokens)
	assert.EqualValues(t, 900, view.UsedTokens)

	// Project soft cap: set below usage (soft caps never 409), then delete.
	w = modelReq(t, f.r, http.MethodPut, "/api/model-budget/projects/"+strconv.FormatInt(project.ID, 10),
		`{"monthly_tokens":500}`, f.userA)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &view))
	require.Len(t, view.Projects, 1)
	assert.EqualValues(t, project.ID, view.Projects[0].ProjectID)
	assert.EqualValues(t, 500, view.Projects[0].MonthlyTokens)
	assert.EqualValues(t, 900, view.Projects[0].UsedTokens)

	w = modelReq(t, f.r, http.MethodDelete, "/api/model-budget/projects/"+strconv.FormatInt(project.ID, 10), "", f.userA)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	w = modelReq(t, f.r, http.MethodGet, "/api/model-budget", "", f.userA)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &view))
	assert.Empty(t, view.Projects)

	// A project the caller cannot access answers 403.
	w = modelReq(t, f.r, http.MethodPut, "/api/model-budget/projects/"+strconv.FormatInt(project.ID, 10),
		`{"monthly_tokens":10}`, f.userB)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}
