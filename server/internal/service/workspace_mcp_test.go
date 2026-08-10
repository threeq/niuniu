package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

func TestSetStrictMCP(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	q := store.New(db)

	// seed a minimal project/column/issue/workspace
	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "p-strict-mcp", OwnerType: "user"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "todo", Position: 0})
	if err != nil {
		t.Fatalf("CreateColumn: %v", err)
	}
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col.ID, Title: "t", Position: 0})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{Int64: issue.ID, Valid: true},
		Name:      "ws-strict-mcp",
		Path:      t.TempDir(),
		Status:    "created",
		OwnerType: "user",
		OwnerID:   0,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	svc := NewWorkspaceService(q, db, nil, t.TempDir(), nil, nil)

	if err := svc.SetStrictMCP(ctx, ws.ID, true); err != nil {
		t.Fatalf("SetStrictMCP: %v", err)
	}
	got, err := q.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.StrictMcpConfig != 1 {
		t.Fatalf("strict_mcp_config = %d, want 1", got.StrictMcpConfig)
	}
	if err := svc.SetStrictMCP(ctx, ws.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ = q.GetWorkspace(ctx, ws.ID)
	if got.StrictMcpConfig != 0 {
		t.Fatalf("strict_mcp_config = %d, want 0", got.StrictMcpConfig)
	}
}
