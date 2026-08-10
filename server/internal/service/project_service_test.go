package service_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectService_List(t *testing.T) {
	q := testutil.SetupTestQueries(t)
	svc := service.NewProjectService(q, nil, nil, nil)
	ctx := context.Background()

	// Create test projects - ID is auto-generated
	p1, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name:        "Project 1",
		Description: sql.NullString{String: "Description 1", Valid: true},
			OwnerType: "user",
	})
	require.NoError(t, err)

	p2, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name:        "Project 2",
		Description: sql.NullString{String: "Description 2", Valid: true},
			OwnerType: "user",
	})
	require.NoError(t, err)

	tests := []struct {
		name    string
		setup   func(*testing.T)
		wantLen int
		wantErr bool
	}{
		{
			name:    "returns all projects",
			setup:   func(*testing.T) {},
			wantLen: 2,
			wantErr: false,
		},
		{
			name: "returns empty list when no projects",
			setup: func(t *testing.T) {
				// Clean up
				_ = q.DeleteProject(ctx, p1.ID)
				_ = q.DeleteProject(ctx, p2.ID)
			},
			wantLen: 0,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			got, err := svc.List(ctx)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, got, tt.wantLen)
			}
		})
	}
}

func TestProjectService_Get(t *testing.T) {
	q := testutil.SetupTestQueries(t)
	svc := service.NewProjectService(q, nil, nil, nil)
	ctx := context.Background()

	// Create test project - ID is auto-generated
	created, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name:        "Project 1",
		Description: sql.NullString{String: "Description 1", Valid: true},
			OwnerType: "user",
	})
	require.NoError(t, err)

	tests := []struct {
		name    string
		id      int64
		want    *store.Project
		wantErr bool
		errMsg  string
	}{
		{
			name: "returns existing project",
			id:   created.ID,
			want: &store.Project{
				ID:          created.ID,
				Name:        "Project 1",
				Description: sql.NullString{String: "Description 1", Valid: true},
				CreatedAt:   created.CreatedAt,
				UpdatedAt:   created.UpdatedAt,
			},
			wantErr: false,
		},
		{
			name:    "returns error for non-existent project",
			id:      99999,
			want:    nil,
			wantErr: true,
			errMsg:  "no rows",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.Get(ctx, tt.id)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want.ID, got.ID)
				assert.Equal(t, tt.want.Name, got.Name)
				assert.Equal(t, tt.want.Description.String, got.Description.String)
			}
		})
	}
}

func TestProjectService_Create(t *testing.T) {
	q := testutil.SetupTestQueries(t)
	svc := service.NewProjectService(q, nil, nil, nil)
	ctx := context.Background()

	tests := []struct {
		name        string
		projectName string
		description string
		wantErr     bool
	}{
		{
			name:        "creates project with generated ID",
			projectName: "New Project",
			description: "New Description",
			wantErr:     false,
		},
		{
			name:        "creates project with empty description",
			projectName: "Project Without Description",
			description: "",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.Create(ctx, service.CreateProjectParams{
				Name:        tt.projectName,
				Description: tt.description,
				OwnerType:   "user",
				OwnerID:     0,
			})
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotZero(t, got.ID)
				assert.Equal(t, tt.projectName, got.Name)

				// Handle empty description case
				if tt.description == "" {
					assert.False(t, got.Description.Valid)
				} else {
					assert.True(t, got.Description.Valid)
					assert.Equal(t, tt.description, got.Description.String)
				}

				// Verify project was actually created in DB
				retrieved, err := q.GetProject(ctx, got.ID)
				assert.NoError(t, err)
				assert.Equal(t, got.ID, retrieved.ID)
				assert.Equal(t, got.Name, retrieved.Name)
			}
		})
	}
}

func TestProjectService_Update(t *testing.T) {
	q := testutil.SetupTestQueries(t)
	svc := service.NewProjectService(q, nil, nil, nil)
	ctx := context.Background()

	// Create test project - ID is auto-generated
	original, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name:        "Original Name",
		Description: sql.NullString{String: "Original Description", Valid: true},
			OwnerType: "user",
	})
	require.NoError(t, err)

	tests := []struct {
		name    string
		id      int64
		newName string
		newDesc string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "updates existing project",
			id:      original.ID,
			newName: "Updated Name",
			newDesc: "Updated Description",
			wantErr: false,
		},
		{
			name:    "updates with empty description",
			id:      original.ID,
			newName: "Name With Empty Desc",
			newDesc: "",
			wantErr: false,
		},
		{
			name:    "returns error for non-existent project",
			id:      99999,
			newName: "Does Not Matter",
			newDesc: "Does Not Matter",
			wantErr: true,
			errMsg:  "no rows",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.Update(ctx, tt.id, tt.newName, tt.newDesc)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.id, got.ID)
				assert.Equal(t, tt.newName, got.Name)

				// Handle empty description case
				if tt.newDesc == "" {
					assert.False(t, got.Description.Valid)
				} else {
					assert.True(t, got.Description.Valid)
					assert.Equal(t, tt.newDesc, got.Description.String)
				}

				// Verify update persisted
				retrieved, err := q.GetProject(ctx, tt.id)
				assert.NoError(t, err)
				assert.Equal(t, got.Name, retrieved.Name)
				assert.Equal(t, got.Description.String, retrieved.Description.String)
			}
		})
	}
}

func TestProjectService_ListWithStats(t *testing.T) {
	q := testutil.SetupTestQueries(t)
	svc := service.NewProjectService(q, nil, nil, nil)
	ctx := context.Background()

	// Create project with columns and issues
	p1, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name:        "Stats Project",
		Description: sql.NullString{String: "test", Valid: true},
			OwnerType: "user",
	})
	require.NoError(t, err)

	col1, err := q.CreateColumn(ctx, store.CreateColumnParams{
		ProjectID: p1.ID,
		Name:      "待办",
		Position:  0,
	})
	require.NoError(t, err)

	col2, err := q.CreateColumn(ctx, store.CreateColumnParams{
		ProjectID: p1.ID,
		Name:      "进行中",
		Position:  1,
	})
	require.NoError(t, err)

	// Create issues in columns
	_, err = q.CreateIssue(ctx, store.CreateIssueParams{
		ColumnID: col1.ID, Title: "Issue 1", Position: 0,
	})
	require.NoError(t, err)
	_, err = q.CreateIssue(ctx, store.CreateIssueParams{
		ColumnID: col1.ID, Title: "Issue 2", Position: 1,
	})
	require.NoError(t, err)
	issue3, err := q.CreateIssue(ctx, store.CreateIssueParams{
		ColumnID: col2.ID, Title: "Issue 3", Position: 0,
	})
	require.NoError(t, err)

	// Create workspace linked to issue3
	_, err = q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID: sql.NullInt64{Int64: issue3.ID, Valid: true},
		Name:    "WS 1",
		Path:    t.TempDir(),
		Status:  "running",
			OwnerType: "user",
	})
	require.NoError(t, err)

	// Create empty project (no columns, no issues)
	_, err = q.CreateProject(ctx, store.CreateProjectParams{
		Name:        "Empty Project",
		Description: sql.NullString{Valid: false},
			OwnerType: "user",
	})
	require.NoError(t, err)

	t.Run("returns projects with correct stats", func(t *testing.T) {
		result, err := svc.ListWithStats(ctx, []string{"active"})
		require.NoError(t, err)
		require.Len(t, result, 2)

		// Find the stats project (listed first due to DESC order)
		var statsProject service.ProjectWithStats
		for _, r := range result {
			if r.Project.Name == "Stats Project" {
				statsProject = r
				break
			}
		}

		// Issue stats: 待办=2, 进行中=1
		require.Len(t, statsProject.Issues, 2)
		assert.Equal(t, "待办", statsProject.Issues[0].ColumnName)
		assert.Equal(t, int64(2), statsProject.Issues[0].Count)
		assert.Equal(t, "进行中", statsProject.Issues[1].ColumnName)
		assert.Equal(t, int64(1), statsProject.Issues[1].Count)

		// Workspace stats: running=1
		require.Len(t, statsProject.Ws, 1)
		assert.Equal(t, "running", statsProject.Ws[0].Status)
		assert.Equal(t, int64(1), statsProject.Ws[0].Count)

		// Empty project has no stats
		var emptyProject service.ProjectWithStats
		for _, r := range result {
			if r.Project.Name == "Empty Project" {
				emptyProject = r
				break
			}
		}
		assert.Empty(t, emptyProject.Issues)
		assert.Empty(t, emptyProject.Ws)
	})
}

func TestProjectService_ListRepositories_Empty(t *testing.T) {
	ctx := context.Background()
	db := testutil.SetupTestDB(t)
	q := store.New(db)
	svc := service.NewProjectService(q, db, nil, nil)

	// Seed: one project, no repos.
	_, err := db.ExecContext(ctx,
		`INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'P', 'user', 1)`)
	if err != nil {
		t.Fatal(err)
	}

	bindings, err := svc.ListRepositories(ctx, 1)
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(bindings) != 0 {
		t.Fatalf("expected 0 bindings, got %d", len(bindings))
	}
}

// mustExec executes a raw SQL statement against db; fails the test on error.
func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func TestProjectService_AddRepository_SameOwner(t *testing.T) {
	ctx := context.Background()
	db := testutil.SetupTestDB(t)
	q := store.New(db)
	svc := service.NewProjectService(q, db, nil, nil)

	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (10, 'P', 'user', 7)`)
	mustExec(t, db, `INSERT INTO repositories (id, name, path, owner_type, owner_id) VALUES (100, 'R1', '/p1', 'user', 7)`)

	if err := svc.AddRepository(ctx, 10, 100, "main"); err != nil {
		t.Fatalf("AddRepository: %v", err)
	}
	bindings, _ := svc.ListRepositories(ctx, 10)
	if len(bindings) != 1 || bindings[0].DefaultBranch != "main" {
		t.Fatalf("got %+v", bindings)
	}
}

func TestProjectService_AddRepository_CrossOwnerRejected(t *testing.T) {
	ctx := context.Background()
	db := testutil.SetupTestDB(t)
	q := store.New(db)
	svc := service.NewProjectService(q, db, nil, nil)

	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (10, 'P', 'user', 7)`)
	mustExec(t, db, `INSERT INTO repositories (id, name, path, owner_type, owner_id) VALUES (200, 'R', '/p', 'user', 999)`)

	err := svc.AddRepository(ctx, 10, 200, "main")
	if !errors.Is(err, service.ErrOwnerMismatch) {
		t.Fatalf("expected ErrOwnerMismatch, got %v", err)
	}
	bindings, _ := svc.ListRepositories(ctx, 10)
	if len(bindings) != 0 {
		t.Fatalf("rollback failed, got %d bindings", len(bindings))
	}
}

func TestProjectService_AddRepository_Duplicate(t *testing.T) {
	ctx := context.Background()
	db := testutil.SetupTestDB(t)
	q := store.New(db)
	svc := service.NewProjectService(q, db, nil, nil)

	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (10, 'P', 'user', 7)`)
	mustExec(t, db, `INSERT INTO repositories (id, name, path, owner_type, owner_id) VALUES (100, 'R', '/p', 'user', 7)`)

	if err := svc.AddRepository(ctx, 10, 100, "main"); err != nil {
		t.Fatal(err)
	}
	err := svc.AddRepository(ctx, 10, 100, "develop")
	if !errors.Is(err, service.ErrAlreadyAssociated) {
		t.Fatalf("expected ErrAlreadyAssociated, got %v", err)
	}
}

func TestProjectService_RemoveRepository_NotExisting_NoOp(t *testing.T) {
	ctx := context.Background()
	db := testutil.SetupTestDB(t)
	q := store.New(db)
	svc := service.NewProjectService(q, db, nil, nil)

	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (10, 'P', 'user', 7)`)

	if err := svc.RemoveRepository(ctx, 10, 9999); err != nil {
		t.Fatalf("RemoveRepository on absent row should be no-op, got %v", err)
	}
}

func TestProjectService_UpdateRepositoryBranch(t *testing.T) {
	ctx := context.Background()
	db := testutil.SetupTestDB(t)
	q := store.New(db)
	svc := service.NewProjectService(q, db, nil, nil)

	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (10, 'P', 'user', 7)`)
	mustExec(t, db, `INSERT INTO repositories (id, name, path, owner_type, owner_id) VALUES (100, 'R', '/p', 'user', 7)`)
	if err := svc.AddRepository(ctx, 10, 100, "main"); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateRepositoryBranch(ctx, 10, 100, "develop"); err != nil {
		t.Fatalf("UpdateRepositoryBranch: %v", err)
	}
	bindings, _ := svc.ListRepositories(ctx, 10)
	if len(bindings) != 1 || bindings[0].DefaultBranch != "develop" {
		t.Fatalf("got %+v", bindings)
	}
}

func TestProjectService_CleanupCrossOwnerByProject(t *testing.T) {
	ctx := context.Background()
	db := testutil.SetupTestDB(t)
	q := store.New(db)
	svc := service.NewProjectService(q, db, nil, nil)

	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'P', 'user', 7)`)
	mustExec(t, db, `INSERT INTO repositories (id, name, path, owner_type, owner_id) VALUES (10, 'R1', '/a', 'user', 7)`)
	mustExec(t, db, `INSERT INTO repositories (id, name, path, owner_type, owner_id) VALUES (11, 'R2', '/b', 'user', 999)`)
	mustExec(t, db, `INSERT INTO project_repositories (project_id, repository_id, default_branch) VALUES (1, 10, 'main'), (1, 11, 'main')`)

	// Simulate: project transferred from user 7 to user 8.
	n, err := svc.CleanupCrossOwnerRepositoriesByProject(ctx, 1, "user", 8)
	if err != nil {
		t.Fatal(err)
	}
	// Both repos (owner 7 & 999) != new owner (8) -> both removed.
	if n != 2 {
		t.Fatalf("want 2 deleted, got %d", n)
	}
	bindings, _ := svc.ListRepositories(ctx, 1)
	if len(bindings) != 0 {
		t.Fatalf("want 0 bindings after cleanup, got %d", len(bindings))
	}
}

func TestProjectService_CleanupCrossOwnerByRepo(t *testing.T) {
	ctx := context.Background()
	db := testutil.SetupTestDB(t)
	q := store.New(db)
	svc := service.NewProjectService(q, db, nil, nil)

	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'A', 'user', 7), (2, 'B', 'org', 5)`)
	mustExec(t, db, `INSERT INTO repositories (id, name, path, owner_type, owner_id) VALUES (10, 'R', '/p', 'user', 7)`)
	mustExec(t, db, `INSERT INTO project_repositories (project_id, repository_id, default_branch) VALUES (1, 10, 'main'), (2, 10, 'main')`)

	// Repo transferred to org 5 -> project 1 (user 7) becomes mismatched (need cleanup).
	// Project 2 (org 5) matches new owner -- should keep.
	n, err := svc.CleanupCrossOwnerRepositoriesByRepo(ctx, 10, "org", 5)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 deleted, got %d", n)
	}
}

func TestProjectService_Delete(t *testing.T) {
	q := testutil.SetupTestQueries(t)
	svc := service.NewProjectService(q, nil, nil, nil)
	ctx := context.Background()

	// Create test project - ID is auto-generated
	p1, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name:        "To Delete",
		Description: sql.NullString{String: "", Valid: false},
			OwnerType: "user",
	})
	require.NoError(t, err)

	tests := []struct {
		name    string
		id      int64
		wantErr bool
		verify  func(*testing.T, int64)
	}{
		{
			name:    "deletes existing project",
			id:      p1.ID,
			wantErr: false,
			verify: func(t *testing.T, id int64) {
				// Verify project is deleted
				_, err := q.GetProject(ctx, id)
				assert.Error(t, err)
			},
		},
		{
			name:    "returns no error for non-existent project",
			id:      99999,
			wantErr: false, // Delete may not return error for non-existent
			verify:  func(*testing.T, int64) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.Delete(ctx, tt.id)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			tt.verify(t, tt.id)
		})
	}
}
