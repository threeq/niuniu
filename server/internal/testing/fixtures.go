package testing

import (
	"database/sql"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// toNullInt64 converts int to sql.NullInt64
func toNullInt64(n int) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(n), Valid: true}
}

// TestProject returns a test project
func TestProject(id int64, name, desc string) store.Project {
	now := time.Now()
	return store.Project{
		ID:          id,
		Name:        name,
		Description: sql.NullString{String: desc, Valid: desc != ""},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// TestColumn returns a test column
func TestColumn(id, projectID int64, name string, position int32) store.Column {
	return store.Column{
		ID:        id,
		ProjectID: projectID,
		Name:      name,
		Position:  int64(position),
	}
}

// TestIssue returns a test issue
func TestIssue(id, columnID int64, title, desc string, priority int, position int32) store.Issue {
	now := time.Now()
	return store.Issue{
		ID:          id,
		ColumnID:    columnID,
		Title:       title,
		Description: sql.NullString{String: desc, Valid: desc != ""},
		Priority:    toNullInt64(priority),
		Position:    int64(position),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// TestRepository returns a test repository
func TestRepository(id int64, name, path, remote, branch string) store.Repository {
	now := time.Now()
	return store.Repository{
		ID:            id,
		Name:          name,
		Path:          path,
		GitRemote:     sql.NullString{String: remote, Valid: remote != ""},
		DefaultBranch: sql.NullString{String: branch, Valid: branch != ""},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

// TestWorkspace returns a test workspace
func TestWorkspace(id int64, issueID *int64, path, status string) store.Workspace {
	now := time.Now()
	var issueIDNull sql.NullInt64
	if issueID != nil {
		issueIDNull = sql.NullInt64{Int64: *issueID, Valid: true}
	}
	return store.Workspace{
		ID:          id,
		IssueID:     issueIDNull,
		Path:        path,
		Status:      status,
		AgentStatus: sql.NullString{String: "idle", Valid: true},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// TestWorktree returns a test worktree
func TestWorktree(id, workspaceID, repoID int64, worktreePath, branch string) store.Worktree {
	now := time.Now()
	return store.Worktree{
		ID:           id,
		WorkspaceID:  workspaceID,
		RepositoryID: repoID,
		WorktreePath: worktreePath,
		Branch:       branch,
		CreatedAt:    now,
	}
}

// TestComment returns a test comment
func TestComment(id, workspaceID int64, filePath string, lineNumber int, content string) store.Comment {
	now := time.Now()
	var lineNum sql.NullInt64
	if lineNumber >= 0 {
		lineNum = toNullInt64(lineNumber)
	}
	return store.Comment{
		ID:          id,
		WorkspaceID: workspaceID,
		FilePath:    filePath,
		LineNumber:  lineNum,
		Content:     content,
		SentToAgent: sql.NullBool{Bool: false, Valid: true},
		CreatedAt:   now,
	}
}
