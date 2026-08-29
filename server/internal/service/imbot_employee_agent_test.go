package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// The Agent employee's analysis decides work that a project's OWN agent will then
// execute, so it must run on that project's configured agent backend and with its
// bound provider credentials — not a hardcoded `claude` against host credentials.
// These tests pin that contract at the resolution layer.

// A workspace's cli_type and its bound provider both reach the analysis call.
func TestEmployeeAnalyzer_UsesWorkspaceCliTypeAndProvider(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()

	// The project's workspace runs Codex against a self-hosted provider.
	if _, err := f.db.Exec(`UPDATE workspaces SET cli_type = 'codex' WHERE id = ?`, f.wsID); err != nil {
		t.Fatalf("set cli_type: %v", err)
	}
	prov, err := f.q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name: "自建网关", Platform: "anthropic",
		// base_urls is a per-protocol map; Codex speaks the OpenAI protocol
		// (sceneenv.ProtocolForCLI), so the gateway must expose that entry.
		BaseUrls: `{"openai":"https://gw.example.com/v1","anthropic":"https://gw.example.com"}`,
		ApiKey:   "sk-provider-key", Enabled: 1, OwnerType: "user", OwnerID: 1, Slug: "gw",
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if err := f.q.SetWorkspaceEnvProvider(ctx, store.SetWorkspaceEnvProviderParams{
		ID: f.wsID, EnvProviderID: sql.NullInt64{Int64: prov.ID, Valid: true},
	}); err != nil {
		t.Fatalf("bind provider: %v", err)
	}

	a := &employeeAnalyzer{q: f.q}
	cliType, env := a.resolveAgentContext(ctx, EmployeeScope{ProjectID: f.projectID, WorkspaceID: f.wsID})

	if cliType != "codex" {
		t.Errorf("cli_type = %q, want codex (the workspace's own backend)", cliType)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "sk-provider-key") {
		t.Errorf("bound provider credential missing from analysis env:\n%s", joined)
	}
	if !strings.Contains(joined, "gw.example.com") {
		t.Errorf("bound provider base URL missing from analysis env:\n%s", joined)
	}
	// niuniu's own control knobs are meaningless to a bare generation subprocess
	// and must not leak into it.
	for _, e := range env {
		if strings.HasPrefix(e, "NIUNIU_") {
			t.Errorf("NIUNIU_* control var leaked into analysis env: %q", e)
		}
	}
}

// With no workspace to speak for the project, the project's default agent applies.
func TestEmployeeAnalyzer_FallsBackToProjectDefaultCliType(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	if _, err := f.db.Exec(`UPDATE projects SET default_cli_type = 'qwen' WHERE id = ?`, f.projectID); err != nil {
		t.Fatalf("set project default: %v", err)
	}

	a := &employeeAnalyzer{q: f.q}
	cliType, env := a.resolveAgentContext(ctx, EmployeeScope{ProjectID: f.projectID})
	if cliType != "qwen" {
		t.Errorf("cli_type = %q, want qwen (the project default)", cliType)
	}
	if len(env) != 0 {
		t.Errorf("no workspace => no provider env, got %v", env)
	}
}

// An install that configured nothing must behave exactly as before this change:
// empty cli_type (Claude) and no extra env, so the global one-shot preset and host
// credentials remain the source of truth.
func TestEmployeeAnalyzer_UnconfiguredIsUnchanged(t *testing.T) {
	f := newIMBotFixture(t)
	a := &employeeAnalyzer{q: f.q}
	cliType, env := a.resolveAgentContext(context.Background(), EmployeeScope{})
	if cliType != "" || len(env) != 0 {
		t.Errorf("unconfigured scope should resolve to defaults, got cli_type=%q env=%v", cliType, env)
	}
}

// The scope handed to the analyzer must name the chat's routed project and a
// workspace that actually belongs to it — otherwise the analysis would run under
// another project's agent/credentials.
func TestEmployeeScope_PrefersTheChatsOwnConversation(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	chat := f.observeChat(t, "oc_scope")
	seedChatter(t, f, chat, employeeMinMessages)

	// Give the project a second workspace, then point the chat at the first one.
	otherIssue, _ := f.newWorkspace(t, "另一个任务", "/tmp/ws-other-scope")
	_ = otherIssue
	f.svc.setActiveIssue(ctx, chat, f.issueID)

	an := &fakeAnalyzer{verdict: EmployeeVerdict{Action: EmployeeActionNone}}
	NewIMBotEmployee(f.svc, f.q, an).sweep(ctx)

	if len(an.scopes) != 1 {
		t.Fatalf("want 1 analysis, got %d", len(an.scopes))
	}
	got := an.scopes[0]
	if got.ProjectID != f.projectID {
		t.Errorf("scope project = %d, want %d", got.ProjectID, f.projectID)
	}
	if got.WorkspaceID != f.wsID {
		t.Errorf("scope workspace = %d, want %d (the chat's active conversation)", got.WorkspaceID, f.wsID)
	}
}
