package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/intake"
)

// intakePolicyFixture reuses the IM fixture's in-memory database so the rules
// CRUD, policy versions and the IM caller share one store.
func intakePolicyFixture(t *testing.T) (*IntakeService, *imbotFixture) {
	t.Helper()
	f := newIMBotFixture(t)
	return NewIntakeService(f.db, f.q), f
}

func TestIntakeRules_CRUDBumpsPolicyVersion(t *testing.T) {
	svc, f := intakePolicyFixture(t)
	ctx := context.Background()

	rule, err := svc.CreateRule(ctx, f.projectID, UpsertIntakeRuleInput{
		SourceKind:  intake.SourceKindIMBot,
		Name:        "发布类",
		Instruction: "只接发布相关请求",
		Conditions:  intake.RuleConditions{Keywords: []string{"发布"}},
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	if !rule.Enabled {
		t.Fatalf("new rule should be enabled: %+v", rule)
	}

	versions, err := svc.ListPolicyVersions(ctx, f.projectID, intake.SourceKindIMBot, 0)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("versions after create = %+v", versions)
	}
	if !strings.Contains(string(versions[0].Snapshot), "发布类") {
		t.Fatalf("version snapshot is missing the rule: %s", versions[0].Snapshot)
	}

	disabled := false
	updated, err := svc.UpdateRule(ctx, f.projectID, rule.ID, UpsertIntakeRuleInput{
		Name:        "发布类（停用）",
		Instruction: "暂停",
		Conditions:  intake.RuleConditions{Keywords: []string{"发布"}},
		Enabled:     &disabled,
	})
	if err != nil {
		t.Fatalf("update rule: %v", err)
	}
	if updated.Enabled {
		t.Fatalf("rule should be disabled: %+v", updated)
	}
	versions, _ = svc.ListPolicyVersions(ctx, f.projectID, intake.SourceKindIMBot, 0)
	if len(versions) != 2 || versions[0].Version != 2 {
		t.Fatalf("versions after update = %+v", versions)
	}
	if !strings.Contains(string(versions[0].Snapshot), "发布类（停用）") {
		t.Fatalf("updated snapshot is stale: %s", versions[0].Snapshot)
	}

	if err := svc.DeleteRule(ctx, f.projectID, rule.ID); err != nil {
		t.Fatalf("delete rule: %v", err)
	}
	versions, _ = svc.ListPolicyVersions(ctx, f.projectID, intake.SourceKindIMBot, 0)
	if len(versions) != 3 || versions[0].Version != 3 {
		t.Fatalf("versions after delete = %+v", versions)
	}
	var snapshot struct {
		Rules []any `json:"rules"`
	}
	if err := json.Unmarshal(versions[0].Snapshot, &snapshot); err != nil {
		t.Fatalf("snapshot json: %v", err)
	}
	if len(snapshot.Rules) != 0 {
		t.Fatalf("deleted rule still in snapshot: %s", versions[0].Snapshot)
	}
}

func TestIntakeRules_ValidationRejectsEmptyConditions(t *testing.T) {
	svc, f := intakePolicyFixture(t)
	if _, err := svc.CreateRule(context.Background(), f.projectID, UpsertIntakeRuleInput{
		SourceKind: intake.SourceKindIMBot,
		Name:       "空规则",
	}); err == nil {
		t.Fatalf("a rule with no conditions must be rejected")
	}
}

func TestIntakePolicy_SettingsAndRuleChangesApplyOnNextLoad(t *testing.T) {
	svc, f := intakePolicyFixture(t)
	ctx := context.Background()
	policyReader := imbotPolicy{q: f.q}

	_, err := svc.UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindIMBot,
			Mode:       string(intake.ModeAuto),
			AllowList:  []string{"ou_alice"},
		}},
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	rule, err := svc.CreateRule(ctx, f.projectID, UpsertIntakeRuleInput{
		SourceKind: intake.SourceKindIMBot,
		Name:       "发布类",
		Conditions: intake.RuleConditions{Keywords: []string{"deploy"}},
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	policy, err := policyReader.LoadPolicy(ctx, f.projectID, intake.SourceKindIMBot)
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	if policy.Mode != intake.ModeAuto || len(policy.AllowList) != 1 || len(policy.Rules) != 1 || policy.Version != 2 {
		t.Fatalf("policy after settings+rule = %+v", policy)
	}

	disabled := false
	if _, err := svc.UpdateRule(ctx, f.projectID, rule.ID, UpsertIntakeRuleInput{
		Name:       "发布类",
		Conditions: intake.RuleConditions{Keywords: []string{"deploy"}},
		Enabled:    &disabled,
	}); err != nil {
		t.Fatalf("disable rule: %v", err)
	}

	// The pipeline reloads the policy for every trigger, so the very next read
	// (the next event) sees the disabled rule and the new version.
	policy, err = policyReader.LoadPolicy(ctx, f.projectID, intake.SourceKindIMBot)
	if err != nil {
		t.Fatalf("reload policy: %v", err)
	}
	if len(policy.Rules) != 0 {
		t.Fatalf("disabled rule still loaded: %+v", policy.Rules)
	}
	if policy.Version != 3 {
		t.Fatalf("policy version = %d, want 3", policy.Version)
	}
}

func TestHandleInbound_BotCannotStopTask(t *testing.T) {
	svc, f := intakePolicyFixture(t)
	ctx := context.Background()
	chat := f.activeChat(t, "oc_a")
	f.svc.setActiveIssue(ctx, chat, f.issueID)

	if _, err := svc.UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindIMBot,
			Mode:       string(intake.ModeOff),
			BotList:    []string{"bot_release"},
		}},
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", ActorExtID: "bot_release",
		Text: "/stop", Kind: "message", EventID: "e1",
	})

	if len(f.deliverer.stopped) != 0 {
		t.Fatalf("bot stopped the task: %+v", f.deliverer.stopped)
	}
	if len(f.adapter.pushes) != 1 || !strings.Contains(f.adapter.pushes[0].Text, "权限") {
		t.Fatalf("expected a permission denial, got %+v", f.adapter.pushes)
	}
}

func TestHandleInbound_AllowListGatesStopTask(t *testing.T) {
	svc, f := intakePolicyFixture(t)
	ctx := context.Background()
	chat := f.activeChat(t, "oc_a")
	f.svc.setActiveIssue(ctx, chat, f.issueID)

	if _, err := svc.UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindIMBot,
			Mode:       string(intake.ModeOff),
			AllowList:  []string{"ou_alice"},
		}},
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", ActorExtID: "ou_bob",
		Text: "/stop", Kind: "message", EventID: "e1",
	})
	if len(f.deliverer.stopped) != 0 {
		t.Fatalf("unlisted actor stopped the task")
	}

	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", ActorExtID: "ou_alice",
		Text: "/stop", Kind: "message", EventID: "e2",
	})
	if len(f.deliverer.stopped) != 1 || f.deliverer.stopped[0] != f.wsID {
		t.Fatalf("allow-listed actor was not permitted to stop: %+v", f.deliverer.stopped)
	}
}

func TestHandleInbound_BotCannotApprovePermission(t *testing.T) {
	svc, f := intakePolicyFixture(t)
	ctx := context.Background()
	f.activeChat(t, "oc_a")

	if _, err := svc.UpdateSettings(ctx, f.projectID, UpdateIntakeSettingsRequest{
		Settings: []IntakeSettingInput{{
			SourceKind: intake.SourceKindIMBot,
			Mode:       string(intake.ModeOff),
			BotList:    []string{"bot_release"},
		}},
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	reqID := f.seedPermissionRequest(t, f.wsID)
	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", ActorExtID: "bot_release", Kind: "action_callback",
		CallbackData: "permission:approve:" + itoa(reqID), EventID: "cb1",
	})

	if len(f.perm.calls) != 0 {
		t.Fatalf("bot approved a tool: %+v", f.perm.calls)
	}
	if len(f.adapter.pushes) != 1 || !strings.Contains(f.adapter.pushes[0].Text, "权限") {
		t.Fatalf("expected a permission denial, got %+v", f.adapter.pushes)
	}
}

func TestHandleInbound_AcceptWritesIssueProvenance(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	f.activeChat(t, "oc_a")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "帮我做张表", Kind: "message", EventID: "e1",
	})

	issue, err := f.q.GetIssue(ctx, f.issueID)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if !issue.ExternalSource.Valid || issue.ExternalSource.String != intake.SourceKindIMBot {
		t.Fatalf("external source = %+v, want im_bot", issue.ExternalSource)
	}
	if !issue.ExternalID.Valid || issue.ExternalID.String != "e1" {
		t.Fatalf("external id = %+v, want e1", issue.ExternalID)
	}
}

// Guard the fixture assumption that intake settings do not exist by default:
// an unconfigured project still lets an admitted human stop a task.
func TestHandleInbound_UnconfiguredChatStillAllowsStop(t *testing.T) {
	f := newIMBotFixture(t)
	ctx := context.Background()
	chat := f.activeChat(t, "oc_a")
	f.svc.setActiveIssue(ctx, chat, f.issueID)

	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", ActorExtID: "ou_human",
		Text: "/stop", Kind: "message", EventID: "e1",
	})
	if len(f.deliverer.stopped) != 1 {
		t.Fatalf("unconfigured chat denied a human: %+v", f.adapter.pushes)
	}
}
