package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/store"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListProjects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	q := store.New(db)

	// Create test projects - ID is auto-generated
	p1, err := q.CreateProject(context.Background(), store.CreateProjectParams{
		Name:        "Project 1",
		Description: sql.NullString{String: "Description 1", Valid: true},
			OwnerType: "user",
	})
	require.NoError(t, err)

	p2, err := q.CreateProject(context.Background(), store.CreateProjectParams{
		Name:        "Project 2",
		Description: sql.NullString{String: "Description 2", Valid: true},
			OwnerType: "user",
	})
	require.NoError(t, err)

	t.Run("returns all projects", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/projects", nil)
		srv.Engine().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response []api.ProjectResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)
		assert.Len(t, response, 2)

		// Verify the projects we created are in the response
		ids := []int64{p1.ID, p2.ID}
		for _, r := range response {
			assert.Contains(t, ids, r.ID)
		}
	})

	t.Run("returns empty array when no projects", func(t *testing.T) {
		// Clean up projects
		_, _ = db.Exec("DELETE FROM projects")

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/projects", nil)
		srv.Engine().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response []api.ProjectResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)
		assert.Len(t, response, 0)
	})
}

func TestGetProject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	q := store.New(db)

	// Create test project - ID is auto-generated
	p1, err := q.CreateProject(context.Background(), store.CreateProjectParams{
		Name:        "Project 1",
		Description: sql.NullString{String: "Description 1", Valid: true},
			OwnerType: "user",
	})
	require.NoError(t, err)

	t.Run("returns existing project", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", fmt.Sprintf("/api/projects/%d", p1.ID), nil)
		srv.Engine().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response api.ProjectResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		assert.NoError(t, err)
		assert.Equal(t, p1.ID, response.ID)
		assert.Equal(t, "Project 1", response.Name)
		assert.Equal(t, "Description 1", *response.Description)
	})

	t.Run("returns 404 for non-existent project", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/projects/99999", nil)
		srv.Engine().ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestCreateProject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, _ := testutil.SetupTestServer(t)

	tests := []struct {
		name       string
		request    api.CreateProjectRequest
		wantStatus int
		wantName   string
		wantDesc   *string
	}{
		{
			name: "creates project with description",
			request: api.CreateProjectRequest{
				Name:        "New Project",
				Description: "New Description",
			},
			wantStatus: http.StatusCreated,
			wantName:   "New Project",
			wantDesc:   strPtr("New Description"),
		},
		{
			name: "creates project without description",
			request: api.CreateProjectRequest{
				Name: "Project Without Description",
			},
			wantStatus: http.StatusCreated,
			wantName:   "Project Without Description",
			wantDesc:   nil,
		},
		{
			name: "returns 400 for missing name",
			request: api.CreateProjectRequest{
				Description: "Missing Name",
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/api/projects", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			srv.Engine().ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			if tt.wantStatus == http.StatusCreated {
				var response api.ProjectResponse
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(t, err)
				assert.NotZero(t, response.ID)
				assert.Equal(t, tt.wantName, response.Name)
				assert.Equal(t, tt.wantDesc, response.Description)
			}
		})
	}
}

// TestCreateProject_DuplicateNameReturnsTakenCode guards goal-condition part 2:
// creating a same-name project must fail with a stable PROJECT_NAME_TAKEN code
// and a message that names the conflicting project, so the client can render a
// clear, localized error pointing at the exact name.
func TestCreateProject_DuplicateNameReturnsTakenCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, _ := testutil.SetupTestServer(t)

	post := func(name string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(api.CreateProjectRequest{Name: name})
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("POST", "/api/projects", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		srv.Engine().ServeHTTP(w, req)
		return w
	}

	require.Equal(t, http.StatusCreated, post("牛牛助手").Code)

	w := post("牛牛助手")
	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "PROJECT_NAME_TAKEN", resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "牛牛助手", "error must name the conflicting project")
}

func TestUpdateProject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	q := store.New(db)

	// Create test project - ID is auto-generated
	p1, err := q.CreateProject(context.Background(), store.CreateProjectParams{
		Name:        "Original Name",
		Description: sql.NullString{String: "Original Description", Valid: true},
			OwnerType: "user",
	})
	require.NoError(t, err)

	tests := []struct {
		name       string
		projectID  int64
		request    api.UpdateProjectRequest
		wantStatus int
		wantName   string
		wantDesc   *string
	}{
		{
			name:      "updates existing project",
			projectID: p1.ID,
			request: api.UpdateProjectRequest{
				Name:        "Updated Name",
				Description: "Updated Description",
			},
			wantStatus: http.StatusOK,
			wantName:   "Updated Name",
			wantDesc:   strPtr("Updated Description"),
		},
		{
			name:      "updates with empty description",
			projectID: p1.ID,
			request: api.UpdateProjectRequest{
				Name: "Name With Empty Desc",
			},
			wantStatus: http.StatusOK,
			wantName:   "Name With Empty Desc",
			wantDesc:   nil,
		},
		{
			name:      "returns 404 for non-existent project",
			projectID: 99999,
			request: api.UpdateProjectRequest{
				Name: "Does Not Matter",
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:      "returns 400 for missing name",
			projectID: p1.ID,
			request: api.UpdateProjectRequest{
				Description: "Missing Name",
			},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.request)
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("PUT", fmt.Sprintf("/api/projects/%d", tt.projectID), bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			srv.Engine().ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)

			if tt.wantStatus == http.StatusOK {
				var response api.ProjectResponse
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(t, err)
				assert.Equal(t, tt.wantName, response.Name)
				assert.Equal(t, tt.wantDesc, response.Description)
			}
		})
	}
}

func TestDeleteProject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	q := store.New(db)

	// Create test project - ID is auto-generated
	p1, err := q.CreateProject(context.Background(), store.CreateProjectParams{
		Name:        "To Delete",
		Description: sql.NullString{String: "", Valid: false},
			OwnerType: "user",
	})
	require.NoError(t, err)

	t.Run("deletes existing project", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("/api/projects/%d", p1.ID), nil)
		srv.Engine().ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)

		// Verify project is deleted
		_, err := q.GetProject(context.Background(), p1.ID)
		assert.Error(t, err)
	})

	t.Run("returns 204 for non-existent project", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("DELETE", "/api/projects/99999", nil)
		srv.Engine().ServeHTTP(w, req)

		assert.Equal(t, http.StatusNoContent, w.Code)
	})
}

func TestListProjectsWithStats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	q := store.New(db)
	ctx := context.Background()

	// Create project with columns and issues
	p1, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name:        "Stats Project",
		Description: sql.NullString{String: "test", Valid: true},
			OwnerType: "user",
	})
	require.NoError(t, err)

	col1, err := q.CreateColumn(ctx, store.CreateColumnParams{
		ProjectID: p1.ID, Name: "待办", Position: 0,
	})
	require.NoError(t, err)

	col2, err := q.CreateColumn(ctx, store.CreateColumnParams{
		ProjectID: p1.ID, Name: "完成", Position: 1,
	})
	require.NoError(t, err)

	_, err = q.CreateIssue(ctx, store.CreateIssueParams{
		ColumnID: col1.ID, Title: "Issue 1", Position: 0,
	})
	require.NoError(t, err)

	issue2, err := q.CreateIssue(ctx, store.CreateIssueParams{
		ColumnID: col2.ID, Title: "Issue 2", Position: 0,
	})
	require.NoError(t, err)

	_, err = q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID: sql.NullInt64{Int64: issue2.ID, Valid: true},
		Name:    "WS 1",
		Path:    t.TempDir(),
		Status:  "running",
			OwnerType: "user",
	})
	require.NoError(t, err)

	t.Run("returns projects with stats", func(t *testing.T) {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/projects", nil)
		srv.Engine().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response []api.ProjectResponse
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)
		require.Len(t, response, 1)

		r := response[0]
		assert.Equal(t, p1.ID, r.ID)

		// Issue stats present
		require.NotNil(t, r.IssueStats)
		require.Len(t, r.IssueStats, 2)
		assert.Equal(t, "待办", r.IssueStats[0].ColumnName)
		assert.Equal(t, int64(1), r.IssueStats[0].Count)
		assert.Equal(t, "完成", r.IssueStats[1].ColumnName)
		assert.Equal(t, int64(1), r.IssueStats[1].Count)

		// Workspace stats present
		require.NotNil(t, r.WsStats)
		require.Len(t, r.WsStats, 1)
		assert.Equal(t, "running", r.WsStats[0].Status)
		assert.Equal(t, int64(1), r.WsStats[0].Count)
	})

	t.Run("returns nil stats for project with no data", func(t *testing.T) {
		// Create empty project
		_, err := q.CreateProject(ctx, store.CreateProjectParams{
			Name:        "Empty",
			Description: sql.NullString{Valid: false},
					OwnerType: "user",
		})
		require.NoError(t, err)

		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/api/projects", nil)
		srv.Engine().ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response []api.ProjectResponse
		err = json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)

		// Find the empty project
		var empty api.ProjectResponse
		for _, r := range response {
			if r.Name == "Empty" {
				empty = r
				break
			}
		}
		assert.Nil(t, empty.IssueStats)
		assert.Nil(t, empty.WsStats)
	})
}

func TestProjectHandler_AddRepository_201(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	_, err := db.Exec(`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'P', 'user', 1)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO repositories (id, name, path, owner_type, owner_id) VALUES (1, 'R', '/p', 'user', 1)`)
	require.NoError(t, err)

	body := `{"repository_id":1,"default_branch":"main"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/projects/1/repositories", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.Engine().ServeHTTP(w, req)
	assert.Equal(t, 201, w.Code, w.Body.String())
}

func TestProjectHandler_AddRepository_OwnerMismatch400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	_, err := db.Exec(`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'P', 'user', 1)`)
	require.NoError(t, err)
	// repo owned by a different user
	_, err = db.Exec(`INSERT INTO repositories (id, name, path, owner_type, owner_id) VALUES (1, 'R', '/p', 'user', 999)`)
	require.NoError(t, err)

	body := `{"repository_id":1,"default_branch":"main"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/projects/1/repositories", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.Engine().ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code, w.Body.String())
}

func TestProjectHandler_AddRepository_Duplicate409(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	_, err := db.Exec(`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'P', 'user', 1)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO repositories (id, name, path, owner_type, owner_id) VALUES (1, 'R', '/p', 'user', 1)`)
	require.NoError(t, err)

	body := `{"repository_id":1,"default_branch":"main"}`

	// First POST — should be 201
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest("POST", "/api/projects/1/repositories", bytes.NewReader([]byte(body)))
	req1.Header.Set("Content-Type", "application/json")
	srv.Engine().ServeHTTP(w1, req1)
	require.Equal(t, 201, w1.Code, w1.Body.String())

	// Second POST — should be 409
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/api/projects/1/repositories", bytes.NewReader([]byte(body)))
	req2.Header.Set("Content-Type", "application/json")
	srv.Engine().ServeHTTP(w2, req2)
	assert.Equal(t, 409, w2.Code, w2.Body.String())
}

func TestProjectHandler_RemoveRepository_204_Idempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	_, err := db.Exec(`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'P', 'user', 1)`)
	require.NoError(t, err)

	// DELETE on a repo that was never associated — should still be 204 (idempotent)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/api/projects/1/repositories/9999", nil)
	srv.Engine().ServeHTTP(w, req)
	assert.Equal(t, 204, w.Code, w.Body.String())
}

func TestProjectHandler_PatchRepository_404OnAbsent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	_, err := db.Exec(`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'P', 'user', 1)`)
	require.NoError(t, err)

	body := `{"default_branch":"develop"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/api/projects/1/repositories/9999", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.Engine().ServeHTTP(w, req)
	assert.Equal(t, 404, w.Code, w.Body.String())
}

func TestProjectHandler_Update_RejectsOwnerField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv, db := testutil.SetupTestServer(t)
	_, err := db.Exec(`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'P', 'user', 1)`)
	require.NoError(t, err)

	body := `{"name":"P2","description":"","owner_type":"org","owner_id":5}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PUT", "/api/projects/1", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	srv.Engine().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("want 400 forbidden field, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// Helper function to create a string pointer
func strPtr(s string) *string {
	return &s
}
