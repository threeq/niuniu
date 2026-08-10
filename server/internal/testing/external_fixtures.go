package testing

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// tempDirOnce caches a per-env temp directory so repeated TempPath calls
// land siblings in the same directory. The directory is allocated lazily
// from the *first* testing.T that calls TempPath, and reused for all
// subsequent calls — t.TempDir() registers cleanup on the original t.
var (
	tempDirMu   sync.Mutex
	tempDirsFor = map[*IsolationEnv]string{}
)

// Queries returns a sqlc *Queries bound to e.DB. Convenience wrapper so
// fixture helpers and tests don't need to import store.New everywhere.
func (e *IsolationEnv) Queries() *store.Queries {
	return store.New(e.DB)
}

// TempPath returns a path under a per-env temp directory with the given
// basename. The directory is created lazily on first call (via t.TempDir(),
// so cleanup is automatic) and reused for subsequent calls — keeping
// related fixture files (e.g. "secret", "secret.bak") under one root.
func (e *IsolationEnv) TempPath(t *testing.T, name string) string {
	t.Helper()
	tempDirMu.Lock()
	defer tempDirMu.Unlock()
	dir, ok := tempDirsFor[e]
	if !ok {
		dir = t.TempDir()
		tempDirsFor[e] = dir
		t.Cleanup(func() {
			tempDirMu.Lock()
			delete(tempDirsFor, e)
			tempDirMu.Unlock()
		})
	}
	return filepath.Join(dir, name)
}

// NewProject creates a project owned by ownerUserID (owner_type='user') and
// returns the inserted row. Status defaults to 'active' (per CreateProject).
func (e *IsolationEnv) NewProject(t *testing.T, ownerUserID int64, name string) store.Project {
	t.Helper()
	p, err := e.Queries().CreateProject(context.Background(), store.CreateProjectParams{
		Name:      name,
		OwnerType: "user",
		OwnerID:   ownerUserID,
	})
	if err != nil {
		t.Fatalf("NewProject(%q): %v", name, err)
	}
	return p
}

// NewColumn creates a column under projectID with the given lifecycle
// mapping (e.g. "created", "in_progress", "completed"). Position defaults
// to 0; tests that need ordering should pass distinct lifecycles instead.
func (e *IsolationEnv) NewColumn(t *testing.T, projectID int64, name, lifecycle string) store.Column {
	t.Helper()
	c, err := e.Queries().CreateColumn(context.Background(), store.CreateColumnParams{
		ProjectID:        projectID,
		Name:             name,
		Position:         0,
		LifecycleMapping: lifecycle,
	})
	if err != nil {
		t.Fatalf("NewColumn(%q): %v", name, err)
	}
	return c
}

// SeedExternalIssue inserts an issue already linked to an external source
// directly via raw SQL — bypasses lifecycle/harness side-effects so tests
// can set up arbitrary external states without driving the full import path.
//
// NOTE: requires the issues table to carry external_source / external_id /
// external_url columns (added in M0.3). Until that migration ships, calling
// this helper will fail at the INSERT stage on any DB. The companion test
// (external_fixtures_test.go) is gated with a build tag for this reason.
func (e *IsolationEnv) SeedExternalIssue(t *testing.T, columnID int64, source, externalID string) store.Issue {
	t.Helper()
	ctx := context.Background()
	res, err := e.DB.ExecContext(ctx,
		`INSERT INTO issues (column_id, title, description, position, external_source, external_id, external_url)
		 VALUES (?, ?, ?, 0, ?, ?, '')`,
		columnID, "ext-"+externalID, "snapshot", source, externalID)
	if err != nil {
		t.Fatalf("SeedExternalIssue insert: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("SeedExternalIssue last insert id: %v", err)
	}
	issue, err := e.Queries().GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("SeedExternalIssue GetIssue(%d): %v", id, err)
	}
	return issue
}

// SeedComment inserts an issue_comment row directly via the sqlc
// CreateIssueComment query. If author is empty, defaults to "test-user".
func (e *IsolationEnv) SeedComment(t *testing.T, issueID int64, author, content string) store.IssueComment {
	t.Helper()
	if author == "" {
		author = "test-user"
	}
	c, err := e.Queries().CreateIssueComment(context.Background(), store.CreateIssueCommentParams{
		IssueID: issueID,
		Author:  author,
		Content: content,
	})
	if err != nil {
		t.Fatalf("SeedComment(issue=%d): %v", issueID, err)
	}
	return c
}
