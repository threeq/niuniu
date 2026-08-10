package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProjectBlueprintHTTPFlow exercises the full issue #338 goal end-to-end over
// HTTP: save a project as a template, list templates, then create a new project
// from the template and assert it inherits the same columns.
func TestProjectBlueprintHTTPFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, _ := testutil.SetupTestServer(t)

	doJSON := func(method, path string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			require.NoError(t, json.NewEncoder(&buf).Encode(body))
		}
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		srv.Engine().ServeHTTP(w, req)
		return w
	}

	// 1. Source project (seeded with the default five columns).
	w := doJSON("POST", "/api/projects", map[string]any{"name": "source-proj"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var srcProj api.ProjectResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &srcProj))

	// 2. Save it as a template.
	w = doJSON("POST", "/api/projects/"+itoa(srcProj.ID)+"/blueprints", map[string]any{"name": "five-lane"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var saved api.ProjectBlueprintSummary
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &saved))
	assert.Equal(t, "five-lane", saved.Name)
	assert.Equal(t, 5, saved.ColumnCount)

	// 3. List templates: builtins (seeded on boot) + the saved one.
	listOf := func() []api.ProjectBlueprintSummary {
		w := doJSON("GET", "/api/project-blueprints", nil)
		require.Equal(t, http.StatusOK, w.Code)
		var l []api.ProjectBlueprintSummary
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &l))
		return l
	}
	find := func(l []api.ProjectBlueprintSummary, id int64) *api.ProjectBlueprintSummary {
		for i := range l {
			if l[i].ID == id {
				return &l[i]
			}
		}
		return nil
	}
	list := listOf()
	require.NotNil(t, find(list, saved.ID), "saved template must be listed")
	assert.False(t, find(list, saved.ID).IsBuiltin)
	builtinCount := 0
	for _, b := range list {
		if b.IsBuiltin {
			builtinCount++
		}
	}
	assert.GreaterOrEqual(t, builtinCount, 4, "builtin templates seeded on boot")

	// 4. Create a new project from the template.
	w = doJSON("POST", "/api/projects", map[string]any{"name": "from-template", "blueprint_id": saved.ID})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var newProj api.ProjectResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &newProj))

	// 5. New project inherited the five columns.
	w = doJSON("GET", "/api/projects/"+itoa(newProj.ID)+"/columns", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var cols []struct {
		Name        string `json:"name"`
		OpPrimitive string `json:"op_primitive"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &cols))
	require.Len(t, cols, 5)
	assert.Equal(t, "待办", cols[0].Name)
	assert.Equal(t, "instruct", cols[1].OpPrimitive)
	assert.Equal(t, "完成", cols[4].Name)

	// 6. Default endpoint resolves to the builtin default; creating a project
	//    without a blueprint_id applies it (template-driven by default).
	w = doJSON("GET", "/api/project-blueprints/default", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var def struct {
		BlueprintID int64 `json:"blueprint_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &def))
	require.NotZero(t, def.BlueprintID)
	defBP := find(listOf(), def.BlueprintID)
	require.NotNil(t, defBP)
	assert.Equal(t, "standard-dev", defBP.Slug)

	w = doJSON("POST", "/api/projects", map[string]any{"name": "no-template-picked"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var defaultProj api.ProjectResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &defaultProj))
	w = doJSON("GET", "/api/projects/"+itoa(defaultProj.ID)+"/columns", nil)
	var defCols []struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &defCols))
	assert.Len(t, defCols, 5)

	// 7. Set the saved template as the owner default, then verify resolution.
	w = doJSON("PUT", "/api/project-blueprints/"+itoa(saved.ID)+"/default", map[string]any{})
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	w = doJSON("GET", "/api/project-blueprints/default", nil)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &def))
	assert.Equal(t, saved.ID, def.BlueprintID)

	// 8. Delete the template; builtins remain.
	w = doJSON("DELETE", "/api/project-blueprints/"+itoa(saved.ID), nil)
	require.Equal(t, http.StatusNoContent, w.Code)
	after := listOf()
	assert.Nil(t, find(after, saved.ID))
	assert.GreaterOrEqual(t, len(after), 4)
}

// TestProjectBlueprintCRUDHTTP exercises the settings-manager CRUD over HTTP:
// create from scratch → detail → update → duplicate → delete, plus the
// builtin-cannot-be-deleted guard.
func TestProjectBlueprintCRUDHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, _ := testutil.SetupTestServer(t)

	doJSON := func(method, path string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			require.NoError(t, json.NewEncoder(&buf).Encode(body))
		}
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		srv.Engine().ServeHTTP(w, req)
		return w
	}

	// Create from scratch (with a scene selected).
	w := doJSON("POST", "/api/project-blueprints", map[string]any{
		"name":        "manual-flow",
		"description": "made in settings",
		"columns": []map[string]any{
			{"name": "待办", "op_primitive": "none"},
			{"name": "做", "op_primitive": "instruct", "op_instruction": "go", "when_to_use": "now"},
			{"name": "好", "op_primitive": "complete"},
		},
		"scenes": []map[string]any{
			{"slug": "go-dev", "display_name": "Go", "source": "builtin"},
		},
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created api.ProjectBlueprintDetail
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.Len(t, created.Columns, 3)
	require.Len(t, created.Scenes, 1)
	assert.Equal(t, "go-dev", created.Scenes[0].Slug)
	assert.Equal(t, "manual-flow", created.Name)
	assert.False(t, created.IsBuiltin)

	// Detail.
	w = doJSON("GET", "/api/project-blueprints/"+itoa(created.ID), nil)
	require.Equal(t, http.StatusOK, w.Code)
	var detail api.ProjectBlueprintDetail
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &detail))
	assert.Equal(t, "instruct", detail.Columns[1].OpPrimitive)
	assert.Equal(t, "go", detail.Columns[1].OpInstruction)

	// Update (rename + drop to 2 columns + remove the scene via empty array).
	w = doJSON("PUT", "/api/project-blueprints/"+itoa(created.ID), map[string]any{
		"name": "manual-flow-2",
		"columns": []map[string]any{
			{"name": "待办", "op_primitive": "none"},
			{"name": "完成", "op_primitive": "complete"},
		},
		"scenes": []map[string]any{},
	})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var updated api.ProjectBlueprintDetail
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	assert.Equal(t, "manual-flow-2", updated.Name)
	require.Len(t, updated.Columns, 2)
	assert.Len(t, updated.Scenes, 0, "empty scenes array removes scenes")

	// Duplicate.
	w = doJSON("POST", "/api/project-blueprints/"+itoa(created.ID)+"/duplicate", map[string]any{"name": "manual-copy"})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var dup api.ProjectBlueprintDetail
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &dup))
	assert.NotEqual(t, created.ID, dup.ID)
	require.Len(t, dup.Columns, 2)

	// Delete the user template — OK.
	w = doJSON("DELETE", "/api/project-blueprints/"+itoa(created.ID), nil)
	require.Equal(t, http.StatusNoContent, w.Code)

	// A builtin cannot be deleted, but can be duplicated.
	listNow := func() []api.ProjectBlueprintSummary {
		w := doJSON("GET", "/api/project-blueprints", nil)
		var l []api.ProjectBlueprintSummary
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &l))
		return l
	}
	var builtinID int64
	for _, b := range listNow() {
		if b.IsBuiltin {
			builtinID = b.ID
			break
		}
	}
	require.NotZero(t, builtinID)
	w = doJSON("DELETE", "/api/project-blueprints/"+itoa(builtinID), nil)
	assert.Equal(t, http.StatusBadRequest, w.Code, "builtin delete must be rejected")
	w = doJSON("POST", "/api/project-blueprints/"+itoa(builtinID)+"/duplicate", map[string]any{})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}
