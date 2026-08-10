package agentproxy

import (
	"context"
	"math/rand"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// newCondTestSession builds a minimal WorkspaceSession wired enough for
// readGoalCondition unit tests. Reuses setupDispatchDB (defined in
// proxy_dispatch_test.go) which applies the full schema and migrations against
// an in-memory SQLite DB.
func newCondTestSession(t *testing.T) (*WorkspaceSession, *store.Queries) {
	t.Helper()
	q := setupDispatchDB(t)
	// Create a workspace row so readAutohostStringEnv can scope its
	// workspace_env lookup.
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "cond-test",
		Path:      t.TempDir(),
		Status:    "running",
		OwnerType: "user",
		OwnerID:   1,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return &WorkspaceSession{
		workspaceID: ws.ID,
		workDir:     ws.Path,
		q:           q,
		rand:        rand.New(rand.NewSource(1)), // deterministic for ratio tests
	}, q
}

func setWorkspaceEnv(t *testing.T, q *store.Queries, wsID int64, key, value string) {
	t.Helper()
	if err := q.SetWorkspaceEnv(context.Background(), store.SetWorkspaceEnvParams{
		WorkspaceID: wsID,
		Key:         key,
		Value:       value,
	}); err != nil {
		t.Fatalf("SetWorkspaceEnv %q: %v", key, err)
	}
}

func createIssueWithGoal(t *testing.T, q *store.Queries, goal string) int64 {
	t.Helper()
	col, err := q.CreateColumn(context.Background(), store.CreateColumnParams{
		ProjectID: 1,
		Name:      "todo",
		Position:  0,
	})
	if err != nil {
		// Need a project too — create one.
		proj, errP := q.CreateProject(context.Background(), store.CreateProjectParams{
			Name:      "cond-proj",
			OwnerType: "user",
			OwnerID:   1,
		})
		if errP != nil {
			t.Fatalf("CreateProject: %v", errP)
		}
		col, err = q.CreateColumn(context.Background(), store.CreateColumnParams{
			ProjectID: proj.ID,
			Name:      "todo",
			Position:  0,
		})
		if err != nil {
			t.Fatalf("CreateColumn: %v", err)
		}
	}
	iss, err := q.CreateIssue(context.Background(), store.CreateIssueParams{
		ColumnID: col.ID,
		Title:    "cond-issue",
		Position: 0,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if goal != "" {
		if err := q.UpdateIssueGoalCondition(context.Background(), store.UpdateIssueGoalConditionParams{
			GoalCondition: goal,
			ID:            iss.ID,
		}); err != nil {
			t.Fatalf("UpdateIssueGoalCondition: %v", err)
		}
	}
	return iss.ID
}

// --- readGoalCondition ---

func TestReadGoalCondition_WorkspaceFallback(t *testing.T) {
	s, q := newCondTestSession(t)
	s.issueID = 0
	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTOHOST_GOAL_CONDITION", "ws-cond")
	c, src := s.readGoalCondition(context.Background())
	if c != "ws-cond" || src != "workspace" {
		t.Fatalf("got cond=%q source=%q, want ws-cond/workspace", c, src)
	}
}

func TestReadGoalCondition_IssueOverrides(t *testing.T) {
	s, q := newCondTestSession(t)
	s.issueID = createIssueWithGoal(t, q, "issue-cond")
	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTOHOST_GOAL_CONDITION", "ws-cond")
	c, src := s.readGoalCondition(context.Background())
	if c != "issue-cond" || src != "issue" {
		t.Fatalf("got cond=%q source=%q, want issue-cond/issue", c, src)
	}
}

func TestReadGoalCondition_WhitespaceFallthrough(t *testing.T) {
	s, q := newCondTestSession(t)
	s.issueID = createIssueWithGoal(t, q, "   ")
	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTOHOST_GOAL_CONDITION", "ws-cond")
	c, src := s.readGoalCondition(context.Background())
	if c != "ws-cond" || src != "workspace" {
		t.Fatalf("got cond=%q source=%q, want ws-cond/workspace (whitespace issue should fall through)", c, src)
	}
}

func TestReadGoalCondition_BothEmpty(t *testing.T) {
	s, _ := newCondTestSession(t)
	s.issueID = 0
	c, src := s.readGoalCondition(context.Background())
	if c != "" || src != "" {
		t.Fatalf("got cond=%q source=%q, want empty/empty", c, src)
	}
}

// No initial-prompt fallback: when neither issue nor workspace configures a
// condition, readGoalCondition returns empty even if an initial prompt exists,
// so the LLM judge is skipped (explicit opt-in only).
func TestReadGoalCondition_NoInitialPromptFallback(t *testing.T) {
	s, _ := newCondTestSession(t)
	s.issueID = 0
	s.autohostGoalHint = "commit this and merge no-ff"
	c, src := s.readGoalCondition(context.Background())
	if c != "" || src != "" {
		t.Fatalf("got cond=%q source=%q, want empty/empty (no initial-prompt fallback)", c, src)
	}
}

func TestReadGoalCondition_WorkspaceConditionWins(t *testing.T) {
	s, q := newCondTestSession(t)
	s.issueID = 0
	s.autohostGoalHint = "prompt-cond"
	setWorkspaceEnv(t, q, s.workspaceID, "NIUNIU_AUTOHOST_GOAL_CONDITION", "ws-cond")
	c, src := s.readGoalCondition(context.Background())
	if c != "ws-cond" || src != "workspace" {
		t.Fatalf("got cond=%q source=%q, want workspace condition", c, src)
	}
}
