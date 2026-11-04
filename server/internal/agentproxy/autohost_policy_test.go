package agentproxy

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// newPolicyTestSession builds a session bound to a fresh issue in its own
// project, with autohost enabled as the workspace permission mode. The project
// has no completion_settings row until the caller writes one, so the resolver's
// DefaultPolicy applies unless a test overrides it.
func newPolicyTestSession(t *testing.T) (s *WorkspaceSession, q *store.Queries, issueID, projectID int64) {
	t.Helper()
	s, q, _, issueID, projectID = newPolicyTestSessionWithDB(t)
	return s, q, issueID, projectID
}

func newPolicyTestSessionWithDB(t *testing.T) (s *WorkspaceSession, q *store.Queries, db *store.DB, issueID, projectID int64) {
	t.Helper()
	db, q = setupDispatchDBRaw(t)
	ctx := context.Background()

	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		Name:      "policy-test",
		Path:      t.TempDir(),
		Status:    "running",
		OwnerType: "user",
		OwnerID:   1,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := q.SetWorkspaceEnv(ctx, store.SetWorkspaceEnvParams{
		WorkspaceID: ws.ID,
		Key:         "NIUNIU_PERMISSION_MODE",
		Value:       AutohostMode,
	}); err != nil {
		t.Fatalf("SetWorkspaceEnv permission mode: %v", err)
	}
	proj, err := q.CreateProject(ctx, store.CreateProjectParams{
		Name:      "policy-proj",
		OwnerType: "user",
		OwnerID:   1,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{
		ProjectID: proj.ID,
		Name:      "todo",
		Position:  0,
	})
	if err != nil {
		t.Fatalf("CreateColumn: %v", err)
	}
	iss, err := q.CreateIssue(ctx, store.CreateIssueParams{
		ColumnID: col.ID,
		Title:    "policy-issue",
		Position: 0,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	return &WorkspaceSession{workspaceID: ws.ID, workDir: ws.Path, q: q, issueID: iss.ID}, q, db, iss.ID, proj.ID
}

func setCompletionSettings(t *testing.T, q *store.Queries, projectID int64, p completionSettingsRow) {
	t.Helper()
	if _, err := q.UpsertCompletionSettings(context.Background(), store.UpsertCompletionSettingsParams{
		ProjectID:       projectID,
		AutohostEnabled: p.autohostEnabled,
		MaxRounds:       p.maxRounds,
		TokenBudget:     p.tokenBudget,
		StopOnDecision:  p.stopOnDecision,
		RequireReview:   p.requireReview,
	}); err != nil {
		t.Fatalf("UpsertCompletionSettings: %v", err)
	}
}

type completionSettingsRow struct {
	autohostEnabled int64
	maxRounds       int64
	tokenBudget     int64
	stopOnDecision  int64
	requireReview   int64
}

func addAcceptanceItem(t *testing.T, q *store.Queries, issueID int64, title string, completed bool) {
	t.Helper()
	item, err := q.CreateChecklistWithKind(context.Background(), store.CreateChecklistWithKindParams{
		IssueID:   issueID,
		Title:     title,
		IssueID_2: issueID,
		Kind:      autohostAcceptanceKind,
		BriefID:   sql.NullInt64{},
	})
	if err != nil {
		t.Fatalf("CreateChecklistWithKind: %v", err)
	}
	if completed {
		if _, err := q.UpdateChecklist(context.Background(), store.UpdateChecklistParams{
			Title:       title,
			IsCompleted: 1,
			ID:          item.ID,
		}); err != nil {
			t.Fatalf("UpdateChecklist: %v", err)
		}
	}
}

func addManualTaskItem(t *testing.T, q *store.Queries, issueID int64, title string, completed bool) {
	t.Helper()
	item, err := q.CreateChecklist(context.Background(), store.CreateChecklistParams{
		IssueID:   issueID,
		Title:     title,
		IssueID_2: issueID,
	})
	if err != nil {
		t.Fatalf("CreateChecklist: %v", err)
	}
	if completed {
		if _, err := q.UpdateChecklist(context.Background(), store.UpdateChecklistParams{
			Title:       title,
			IsCompleted: 1,
			ID:          item.ID,
		}); err != nil {
			t.Fatalf("UpdateChecklist: %v", err)
		}
	}
}

func setLastText(s *WorkspaceSession, text string) {
	s.mu.Lock()
	s.assistantTextBlocks = []string{text}
	s.mu.Unlock()
}

func setConsec(s *WorkspaceSession, n int) {
	s.mu.Lock()
	s.autohostConsec = n
	s.mu.Unlock()
}

func setChainTokens(s *WorkspaceSession, n int64) {
	s.mu.Lock()
	s.autohostChainTokens = n
	s.mu.Unlock()
}

func TestAutohostPolicyDisabledStopsContinue(t *testing.T) {
	s, q, _, projectID := newPolicyTestSession(t)
	setCompletionSettings(t, q, projectID, completionSettingsRow{autohostEnabled: 0, maxRounds: 5, stopOnDecision: 1})
	setLastText(s, "still working")

	ok, prompt := s.autohostMaybeContinue(context.Background())
	if ok || prompt != "" {
		t.Fatalf("disabled policy must stop the watchdog, got ok=%v prompt=%q", ok, prompt)
	}
	if got := s.peekAutohostAttention(); got != "" {
		t.Fatalf("disabled policy is not an attention stop, got %q", got)
	}
}

func TestAutohostPolicyMaxRoundsStopsIntoAttention(t *testing.T) {
	s, q, _, projectID := newPolicyTestSession(t)
	setCompletionSettings(t, q, projectID, completionSettingsRow{autohostEnabled: 1, maxRounds: 2, stopOnDecision: 1})
	setLastText(s, "still working")
	setConsec(s, 2)

	ok, _ := s.autohostMaybeContinue(context.Background())
	if ok {
		t.Fatal("max rounds must not continue")
	}
	attention := s.peekAutohostAttention()
	if !strings.Contains(attention, "最大轮数") || !strings.Contains(attention, "请回答") {
		t.Fatalf("max rounds stop must carry one structured question, got %q", attention)
	}
}

func TestAutohostPolicyIssueProfileOverridesProjectDefault(t *testing.T) {
	s, q, issueID, projectID := newPolicyTestSession(t)
	// Project default allows many rounds; the issue's profile caps at one.
	setCompletionSettings(t, q, projectID, completionSettingsRow{autohostEnabled: 1, maxRounds: 20, stopOnDecision: 1})
	profile, err := q.CreateCompletionProfile(context.Background(), store.CreateCompletionProfileParams{
		ProjectID:       projectID,
		Name:            "one-round",
		AutohostEnabled: 1,
		MaxRounds:       1,
		StopOnDecision:  1,
	})
	if err != nil {
		t.Fatalf("CreateCompletionProfile: %v", err)
	}
	if _, err := q.SetIssueCompletionProfile(context.Background(), store.SetIssueCompletionProfileParams{
		CompletionProfileID: sql.NullInt64{Int64: profile.ID, Valid: true},
		ID:                  issueID,
	}); err != nil {
		t.Fatalf("SetIssueCompletionProfile: %v", err)
	}

	setLastText(s, "still working")
	setConsec(s, 1)
	ok, _ := s.autohostMaybeContinue(context.Background())
	if ok {
		t.Fatal("the issue profile (1 round) must override the project default (20)")
	}
	if got := s.peekAutohostAttention(); !strings.Contains(got, "最大轮数") {
		t.Fatalf("expected a rounds-cap question, got %q", got)
	}
}

func TestAutohostPolicyTokenBudgetStopsIntoAttention(t *testing.T) {
	s, q, _, projectID := newPolicyTestSession(t)
	setCompletionSettings(t, q, projectID, completionSettingsRow{autohostEnabled: 1, maxRounds: 20, tokenBudget: 1000, stopOnDecision: 1})
	setLastText(s, "still working")
	setChainTokens(s, 1000)

	ok, _ := s.autohostMaybeContinue(context.Background())
	if ok {
		t.Fatal("token budget must not continue")
	}
	attention := s.peekAutohostAttention()
	if !strings.Contains(attention, "token 预算") || !strings.Contains(attention, "请回答") {
		t.Fatalf("token budget stop must carry one structured question, got %q", attention)
	}
}

func TestAutohostAcceptanceGateKeepsWorking(t *testing.T) {
	s, q, issueID, projectID := newPolicyTestSession(t)
	setCompletionSettings(t, q, projectID, completionSettingsRow{autohostEnabled: 1, maxRounds: 5, stopOnDecision: 1})
	addAcceptanceItem(t, q, issueID, "单测通过", true)
	addAcceptanceItem(t, q, issueID, "文档已更新", false)
	// A manual progress item must never gate completion.
	addManualTaskItem(t, q, issueID, "查日志", false)
	setLastText(s, "全部做完\n"+autohostStopSentinel)

	ok, prompt := s.autohostMaybeContinue(context.Background())
	if !ok {
		t.Fatal("unmet acceptance must keep the watchdog working, not declare done")
	}
	if got := s.peekAutohostAttention(); got != "" {
		t.Fatalf("continuing must not park into attention, got %q", got)
	}
	if !strings.Contains(prompt, "文档已更新") {
		t.Fatalf("continue prompt must list the unmet acceptance item, got %q", prompt)
	}
	if strings.Contains(prompt, "查日志") {
		t.Fatalf("manual kind='task' items must not appear in the acceptance gate, got %q", prompt)
	}
}

func TestAutohostAcceptanceMetAllowsCompletion(t *testing.T) {
	s, q, issueID, projectID := newPolicyTestSession(t)
	setCompletionSettings(t, q, projectID, completionSettingsRow{autohostEnabled: 1, maxRounds: 5, stopOnDecision: 1})
	addAcceptanceItem(t, q, issueID, "单测通过", true)
	addAcceptanceItem(t, q, issueID, "文档已更新", true)
	setLastText(s, "全部做完\n"+autohostStopSentinel)

	ok, prompt := s.autohostMaybeContinue(context.Background())
	if ok || prompt != "" {
		t.Fatalf("all acceptance met must allow completion, got ok=%v prompt=%q", ok, prompt)
	}
	if got := s.peekAutohostAttention(); got != "" {
		t.Fatalf("a genuine completion must not park into attention, got %q", got)
	}
}

func TestAutohostAcceptanceUnmetAtCapAsksQuestion(t *testing.T) {
	s, q, issueID, projectID := newPolicyTestSession(t)
	setCompletionSettings(t, q, projectID, completionSettingsRow{autohostEnabled: 1, maxRounds: 1, stopOnDecision: 1})
	addAcceptanceItem(t, q, issueID, "文档已更新", false)
	setLastText(s, "全部做完\n"+autohostStopSentinel)
	setConsec(s, 1)

	ok, _ := s.autohostMaybeContinue(context.Background())
	if ok {
		t.Fatal("acceptance unmet at the cap must stop")
	}
	attention := s.peekAutohostAttention()
	if !strings.Contains(attention, "文档已更新") || !strings.Contains(attention, "请回答") {
		t.Fatalf("acceptance cap must ask one structured question naming the gap, got %q", attention)
	}
}

func TestAutohostHumanActionStopsWithQuestion(t *testing.T) {
	s, q, _, projectID := newPolicyTestSession(t)
	setCompletionSettings(t, q, projectID, completionSettingsRow{autohostEnabled: 1, maxRounds: 5, stopOnDecision: 1})
	setLastText(s, "分析完成。\n"+autohostHumanActionSentinel+" 用 SQLite 还是 PostgreSQL？")

	ok, _ := s.autohostMaybeContinue(context.Background())
	if ok {
		t.Fatal("a human-action marker must stop the loop")
	}
	attention := s.peekAutohostAttention()
	if !strings.Contains(attention, "用 SQLite 还是 PostgreSQL？") {
		t.Fatalf("attention must carry the worker's question, got %q", attention)
	}
}

// A policy edit must take effect on the very next turn against the SAME live
// session — no agent restart.
func TestAutohostPolicyChangeAppliesOnNextTurn(t *testing.T) {
	s, q, _, projectID := newPolicyTestSession(t)
	setCompletionSettings(t, q, projectID, completionSettingsRow{autohostEnabled: 1, maxRounds: 5, stopOnDecision: 1})
	setLastText(s, "still working")

	ok, _ := s.autohostMaybeContinue(context.Background())
	if !ok {
		t.Fatal("precondition: 5-round budget must allow a continue")
	}

	// Tighten the project default to one round; the same session is reused.
	setCompletionSettings(t, q, projectID, completionSettingsRow{autohostEnabled: 1, maxRounds: 1, stopOnDecision: 1})
	s.autohostReset()
	setLastText(s, "still working")
	setConsec(s, 1)

	ok, _ = s.autohostMaybeContinue(context.Background())
	if ok {
		t.Fatal("the tightened max_rounds must apply on the next turn without a restart")
	}
	if got := s.peekAutohostAttention(); !strings.Contains(got, "最大轮数") {
		t.Fatalf("expected the rounds-cap question after the policy change, got %q", got)
	}
}

// finalizeSendLoopTurn must treat a deliberate watchdog stop as attention
// without the error/rollback path.
func TestFinalizeAttentionStopSetsAttentionStatus(t *testing.T) {
	hook := &fakeStatusHook{}
	s := newDispatchTestSession(t)
	s.statusHook = hook
	s.mu.Lock()
	s.autohostAttention = "需要你决定"
	s.mu.Unlock()

	s.finalizeSendLoopTurn(context.Background(), false, "需要你决定", true)

	if got := hook.last(); got != "attention" {
		t.Fatalf("final status hook event = %q, want attention", got)
	}
	if got := s.peekAutohostAttention(); got != "" {
		t.Fatalf("attention must be consumed by finalize, got %q", got)
	}
}

// A policy read failure on an issue-bound session must fail closed (attention),
// never silently fall back to the permissive legacy env budgets.
func TestAutohostPolicyResolutionFailureFailsClosed(t *testing.T) {
	s, _, _, _ := newPolicyTestSession(t)
	s.issueID = 999999 // no such issue row -> resolver error
	setLastText(s, "still working")

	ok, _ := s.autohostMaybeContinue(context.Background())
	if ok {
		t.Fatal("a policy resolution failure must stop the watchdog")
	}
	attention := s.peekAutohostAttention()
	if !strings.Contains(attention, "无法解析本任务的自动完成策略") {
		t.Fatalf("policy failure must surface a structured question, got %q", attention)
	}
}

// A failed acceptance lookup must refuse the sentinel rather than read "unknown"
// as "all done".
func TestAutohostAcceptanceLookupFailureFailsClosed(t *testing.T) {
	s, q, db, _, projectID := newPolicyTestSessionWithDB(t)
	setCompletionSettings(t, q, projectID, completionSettingsRow{autohostEnabled: 1, maxRounds: 5, stopOnDecision: 1})
	if _, err := db.ExecContext(context.Background(), `DROP TABLE issue_checklists`); err != nil {
		t.Fatalf("drop issue_checklists: %v", err)
	}
	setLastText(s, "全部做完\n"+autohostStopSentinel)

	ok, _ := s.autohostMaybeContinue(context.Background())
	if ok {
		t.Fatal("an acceptance lookup failure must stop the watchdog")
	}
	attention := s.peekAutohostAttention()
	if !strings.Contains(attention, "无法读取 Task Brief 的验收项") {
		t.Fatalf("acceptance lookup failure must refuse completion, got attention %q", attention)
	}
}

func TestAutohostHumanActionQuestionParsing(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
		ok   bool
	}{
		{"inline", "x\n" + autohostHumanActionSentinel + " A 还是 B？", "A 还是 B？", true},
		{"only-marker", autohostHumanActionSentinel, "", true},
		{"none", "just working", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := autohostHumanActionQuestion(tc.text)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("autohostHumanActionQuestion(%q) = (%q, %v), want (%q, %v)", tc.text, got, ok, tc.want, tc.ok)
			}
		})
	}
}
