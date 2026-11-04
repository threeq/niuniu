package service

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// These tests prove the HandleInbound extraction: the generic route path now
// runs through intake.Service and records intake_events / intake_decisions
// alongside the legacy im_bot_inbox, without changing routing or delivery.

func (f *imbotFixture) intakeEvent(t *testing.T, chatExtID, eventID string) store.IntakeEvent {
	t.Helper()
	row, err := f.q.GetIntakeEventByKey(context.Background(), store.GetIntakeEventByKeyParams{
		SourceKind: intake.SourceKindIMBot,
		SourceRef:  strconv.FormatInt(f.channelID, 10),
		EventExtID: eventID,
	})
	if err != nil {
		t.Fatalf("intake event %q: %v", eventID, err)
	}
	return row
}

func TestHandleInbound_RecordsIntakeEventAndDecision(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "帮我做张表", Kind: "message", EventID: "e1",
	})

	ev := f.intakeEvent(t, "oc_a", "e1")
	if ev.ProjectID != f.projectID || ev.EventKind != "message" || ev.ThreadExtID != "" {
		t.Fatalf("intake event = %+v", ev)
	}

	decisions, err := f.q.ListIntakeDecisionsByEvent(context.Background(), ev.ID)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("decisions = %v, err = %v", decisions, err)
	}
	d := decisions[0]
	if d.Decision != string(intake.DecisionAccept) || d.Strength != string(intake.StrengthExplicit) {
		t.Fatalf("decision = %+v", d)
	}
	if d.Executed != 1 || !d.IssueID.Valid || d.IssueID.Int64 != f.issueID {
		t.Fatalf("executed/issue = %+v", d)
	}
	if !d.WorkspaceID.Valid || d.WorkspaceID.Int64 != f.wsID {
		t.Fatalf("workspace = %+v", d.WorkspaceID)
	}

	// Phase 1 dual-write: the legacy inbox row is still there.
	if _, err := f.q.GetIMBotInboxEvent(context.Background(), store.GetIMBotInboxEventParams{
		ChannelID: f.channelID, EventExtID: "e1",
	}); err != nil {
		t.Fatalf("im_bot_inbox row missing: %v", err)
	}
}

func TestHandleInbound_ThreadFollowUpRecordsResume(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", ThreadExtID: "t1", Text: "开始", Kind: "message", EventID: "e1",
	})
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", ThreadExtID: "t1", Text: "继续", Kind: "message", EventID: "e2",
	})

	if f.router.calls != 1 {
		t.Fatalf("follow-up created a new task: router calls = %d", f.router.calls)
	}
	ev := f.intakeEvent(t, "oc_a", "e2")
	decisions, err := f.q.ListIntakeDecisionsByEvent(context.Background(), ev.ID)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("decisions = %v, err = %v", decisions, err)
	}
	if decisions[0].Decision != string(intake.DecisionResume) {
		t.Fatalf("follow-up decision = %q, want resume", decisions[0].Decision)
	}
	if len(f.deliverer.calls) != 2 || f.deliverer.calls[1].workspaceID != f.wsID {
		t.Fatalf("follow-up delivery = %+v", f.deliverer.calls)
	}
}

// A threadless chat whose active pointer already names a task resumes it (no
// route) and the recorded decision carries that task's ids.
func TestHandleInbound_ActivePointerResumeRecordsTask(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_a")
	f.svc.setActiveIssue(context.Background(), chat, f.issueID)

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "继续", Kind: "message", EventID: "e1",
	})

	if f.router.calls != 0 {
		t.Fatalf("active pointer did not resume: router calls = %d", f.router.calls)
	}
	if len(f.deliverer.calls) != 1 || f.deliverer.calls[0].workspaceID != f.wsID {
		t.Fatalf("delivery = %+v", f.deliverer.calls)
	}
	ev := f.intakeEvent(t, "oc_a", "e1")
	decisions, err := f.q.ListIntakeDecisionsByEvent(context.Background(), ev.ID)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("decisions = %v, err = %v", decisions, err)
	}
	d := decisions[0]
	if d.Decision != string(intake.DecisionResume) {
		t.Fatalf("decision = %q, want resume", d.Decision)
	}
	if !d.IssueID.Valid || d.IssueID.Int64 != f.issueID || !d.WorkspaceID.Valid || d.WorkspaceID.Int64 != f.wsID {
		t.Fatalf("resume decision lost the task ids: %+v", d)
	}
}

func TestHandleInbound_RedeliveryRecordsOneIntakeEvent(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}
	ev := imbot.InboundEvent{ChannelID: f.channelID, ChatExtID: "oc_a", Text: "hi", Kind: "message", EventID: "dup-1"}

	f.svc.HandleInbound(context.Background(), ev)
	f.svc.HandleInbound(context.Background(), ev)

	row := f.intakeEvent(t, "oc_a", "dup-1")
	decisions, err := f.q.ListIntakeDecisionsByEvent(context.Background(), row.ID)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("a duplicate delivery must record one decision, got %v (err %v)", decisions, err)
	}
	if f.router.calls != 1 || len(f.deliverer.calls) != 1 {
		t.Fatalf("duplicate produced side effects: router=%d deliver=%d", f.router.calls, len(f.deliverer.calls))
	}
}

func TestHandleInbound_ForceNewStartsSecondTaskOnBoundThread(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	// First message binds thread t1 to the fixture task.
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", ThreadExtID: "t1", Text: "开始", Kind: "message", EventID: "e1",
	})

	// /new on the same thread must route a NEW task instead of resuming.
	issB, wsB := f.newWorkspace(t, "任务B", "/tmp/ws-B")
	f.router.queue = []PlanTarget{{IssueID: issB, WorkspaceID: wsB}}
	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", ThreadExtID: "t1", Text: "/new 另起一个", Kind: "message", EventID: "e2",
	})

	if f.router.calls != 2 {
		t.Fatalf("force-new did not route a new task: router calls = %d", f.router.calls)
	}
	if got := f.router.lastHint.ForceNew; !got {
		t.Fatalf("route hint did not carry ForceNew: %+v", f.router.lastHint)
	}
	if len(f.deliverer.calls) != 2 || f.deliverer.calls[1].workspaceID != wsB {
		t.Fatalf("force-new delivery = %+v", f.deliverer.calls)
	}
}

func TestHandleInbound_PermanentRouteFailureLeavesDecisionPending(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_a")
	// Empty queue: RouteInProject returns a zero target without an error, which
	// the executor must treat as a failed accept (never bind issue 0).
	f.router.queue = nil

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_a", Text: "hi", Kind: "message", EventID: "e1",
	})

	if len(f.deliverer.calls) != 0 {
		t.Fatalf("delivered despite an incomplete route: %+v", f.deliverer.calls)
	}
	if len(f.adapter.pushes) == 0 {
		t.Fatalf("user was not told the task could not start")
	}
	ev := f.intakeEvent(t, "oc_a", "e1")
	decisions, err := f.q.ListIntakeDecisionsByEvent(context.Background(), ev.ID)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("decisions = %v, err = %v", decisions, err)
	}
	if decisions[0].Executed != 0 {
		t.Fatalf("failed accept marked executed: %+v", decisions[0])
	}
}

// NewIMBotService must always wire the pipeline: the IM handler has no legacy
// path to fall back to.
func TestIMBotService_HasIntakePipeline(t *testing.T) {
	f := newIMBotFixture(t)
	if f.svc.intake == nil {
		t.Fatal("IMBotService was built without its intake pipeline")
	}
}

// TestEffectiveChatIntakeMode covers the per-chat mode resolution and the
// rollback switch (design §7.1). The migrated column is authoritative by
// default; the rollback derives the mode from agent_mode and ignores it.
func TestEffectiveChatIntakeMode(t *testing.T) {
	chat := func(agentMode, intakeMode string) store.ImBotChat {
		return store.ImBotChat{AgentMode: agentMode, IntakeMode: intakeMode}
	}
	cases := []struct {
		name   string
		chat   store.ImBotChat
		legacy bool
		want   intake.Mode
	}{
		{"migrated observe chat is auto", chat("observe", "auto"), false, intake.ModeAuto},
		{"migrated command chat is off", chat("command", "off"), false, intake.ModeOff},
		{"new chat defaults to observe", chat("command", "observe"), false, intake.ModeObserve},
		{"inherit defers to project policy", chat("command", "inherit"), false, ""},
		{"unknown intake degrades to observe", chat("command", ""), false, intake.ModeObserve},
		{"rollback observe→auto", chat("observe", "off"), true, intake.ModeAuto},
		{"rollback command→off", chat("command", "auto"), true, intake.ModeOff},
		{"rollback empty agent degrades to command", chat("", "auto"), true, intake.ModeOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveChatIntakeMode(tc.chat, tc.legacy); got != tc.want {
				t.Fatalf("effectiveChatIntakeMode(%+v, legacy=%v) = %q, want %q", tc.chat, tc.legacy, got, tc.want)
			}
		})
	}
}

// TestHandleInbound_RespondChatIgnoresProjectPolicy proves the chat-level mode
// wins: a project policy of auto must not touch ambient messages in a respond
// chat — they are dropped before the pipeline sees them.
func TestHandleInbound_RespondChatIgnoresProjectPolicy(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_chatmode")
	chat = f.setChatIntakeMode(t, chat, IntakeModeRespond)
	ctx := context.Background()
	if _, err := f.q.UpsertIntakeSetting(ctx, store.UpsertIntakeSettingParams{
		ProjectID: f.projectID, SourceKind: intake.SourceKindIMBot, Enabled: 1, Mode: "auto",
		DailyCap: 10, AllowListJson: `["ou_a"]`, BotListJson: `[]`,
	}); err != nil {
		t.Fatalf("seed intake setting: %v", err)
	}
	if _, err := f.q.CreateIntakeRule(ctx, store.CreateIntakeRuleParams{
		ProjectID: f.projectID, SourceKind: intake.SourceKindIMBot, Name: "r",
		Instruction: "", ConditionsJson: `{"keywords":["部署"]}`, Priority: 0, Enabled: 1,
	}); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID}}

	ev := f.groupMsg(chat.ChatExtID, "e-dry", "请部署到线上", false)
	ev.ActorExtID = "ou_b"
	f.svc.HandleInbound(ctx, ev)

	if f.router.calls != 0 {
		t.Fatalf("respond chat executed %d times, want 0", f.router.calls)
	}
	pending, err := f.q.ListPendingIntakeDecisions(ctx, store.ListPendingIntakeDecisionsParams{
		ProjectID: f.projectID, Decision: "accept",
	})
	if err != nil {
		t.Fatalf("list pending decisions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("respond chat recorded %d suggestions, want 0", len(pending))
	}
}

// TestHandleInbound_LegacyInheritFallsBackToRespond: "inherit project" no
// longer exists, so a legacy chat carrying it resolves to respond: inferred
// ambient work is not executed and produces no suggestion.
func TestHandleInbound_LegacyInheritFallsBackToRespond(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_inherit")
	chat = f.setChatAgentMode(t, chat, AgentModeObserve)
	chat = f.setChatIntakeMode(t, chat, IntakeModeInherit)
	ctx := context.Background()
	if _, err := f.q.UpsertIntakeSetting(ctx, store.UpsertIntakeSettingParams{
		ProjectID: f.projectID, SourceKind: intake.SourceKindIMBot, Enabled: 1, Mode: "auto",
		DailyCap: 10, AllowListJson: `["ou_a"]`, BotListJson: `[]`,
	}); err != nil {
		t.Fatalf("seed intake setting: %v", err)
	}
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID, ProjectID: f.projectID}}

	ev := f.groupMsg(chat.ChatExtID, "e-inherit", "请部署到线上", true)
	ev.ActorExtID = "ou_b"
	f.svc.HandleInbound(ctx, ev)

	if f.router.calls != 0 {
		t.Fatalf("legacy inherit executed %d times, want 0", f.router.calls)
	}
	pending, err := f.q.ListPendingIntakeDecisions(ctx, store.ListPendingIntakeDecisionsParams{
		ProjectID: f.projectID, Decision: "accept",
	})
	if err != nil {
		t.Fatalf("list pending decisions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("respond chat recorded %d suggestions, want 0", len(pending))
	}
}

// TestAcceptDecision_ExecutesPendingDecision proves the calibration queue's
// accept path: a decision recorded by observe mode is replayed through the same
// executor, creating (or resuming) the task and applying the brief goal.
func TestAcceptDecision_ExecutesPendingDecision(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_accept")
	chat = f.setChatIntakeMode(t, chat, IntakeModeAuto)
	ctx := context.Background()
	if _, err := f.q.UpsertIntakeSetting(ctx, store.UpsertIntakeSettingParams{
		ProjectID: f.projectID, SourceKind: intake.SourceKindIMBot, Enabled: 1, Mode: "auto",
		DailyCap: 10, AllowListJson: `["ou_a"]`, BotListJson: `[]`,
	}); err != nil {
		t.Fatalf("seed intake setting: %v", err)
	}
	// No rule: the message does not execute, it becomes a suggestion.
	targetIssue, targetWS := f.newWorkspace(t, "目标任务", "/tmp/ws-accept")
	f.router.queue = []PlanTarget{{IssueID: targetIssue, WorkspaceID: targetWS, ProjectID: f.projectID}}

	ev := f.groupMsg(chat.ChatExtID, "e-acc", "请部署到线上", false)
	ev.ActorExtID = "ou_b"
	f.svc.HandleInbound(ctx, ev)

	recorded := f.intakeEvent(t, chat.ChatExtID, "e-acc")
	decisions, err := f.q.ListIntakeDecisionsByEvent(ctx, recorded.ID)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("decisions = %v, err = %v", decisions, err)
	}
	pending := decisions[0]
	if pending.Executed != 0 || pending.Decision != "accept" {
		t.Fatalf("precondition: decision = %+v, want a pending accept", pending)
	}

	issueID, wsID, err := f.svc.AcceptDecision(ctx, pending, 0)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if issueID != targetIssue || wsID != targetWS {
		t.Fatalf("accepted task = (%d,%d), want (%d,%d)", issueID, wsID, targetIssue, targetWS)
	}
	if f.router.calls != 1 {
		t.Fatalf("accept routed %d times, want 1", f.router.calls)
	}
	if f.router.lastHint.TitleHint != "请部署到线上" {
		t.Fatalf("title hint = %q, want the brief goal", f.router.lastHint.TitleHint)
	}
	if len(f.deliverer.calls) != 1 || f.deliverer.calls[0].workspaceID != targetWS {
		t.Fatalf("delivery = %+v", f.deliverer.calls)
	}

	issue, err := f.q.GetIssue(ctx, targetIssue)
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	if !strings.Contains(issue.GoalCondition, "请部署到线上") {
		t.Fatalf("goal_condition = %q, want the accepted brief goal", issue.GoalCondition)
	}
}

// Accepting one suggestion by hand acts on exactly that decision. Only an
// automatic execution folds the conversation's other pending suggestions into
// its work and marks them consumed; the manual path must leave them pending so
// the reviewer can still act on each one.
func TestAcceptDecision_DoesNotConsumeSiblingSuggestions(t *testing.T) {
	f := newIMBotFixture(t)
	chat := f.activeChat(t, "oc_manual")
	f.setChatIntakeMode(t, chat, IntakeModeAuto)
	ctx := context.Background()
	if _, err := f.q.UpsertIntakeSetting(ctx, store.UpsertIntakeSettingParams{
		ProjectID: f.projectID, SourceKind: intake.SourceKindIMBot, Enabled: 1, Mode: "auto",
		DailyCap: 10, AllowListJson: `["ou_listed"]`, BotListJson: `[]`,
	}); err != nil {
		t.Fatalf("seed intake setting: %v", err)
	}
	targetIssue, targetWS := f.newWorkspace(t, "目标任务", "/tmp/ws-manual")
	f.router.queue = []PlanTarget{{IssueID: targetIssue, WorkspaceID: targetWS, ProjectID: f.projectID}}

	// Two ambient messages with no matching rule → two pending suggestions.
	f.svc.HandleInbound(ctx, f.groupMsg(chat.ChatExtID, "e-m1", "整理周报", false))
	f.svc.HandleInbound(ctx, f.groupMsg(chat.ChatExtID, "e-m2", "清理旧分支", false))

	pending, err := f.q.ListPendingIntakeDecisions(ctx, store.ListPendingIntakeDecisionsParams{
		ProjectID: f.projectID, Decision: "accept",
	})
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending = %v (err %v), want 2 suggestions", pending, err)
	}

	accepted := pending[0]
	issueID, wsID, err := f.svc.AcceptDecision(ctx, accepted, 0)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	// Mirror IntakeService.AcceptDecision: the manual path marks only the chosen
	// decision executed.
	if _, err := f.q.SetIntakeDecisionExecuted(ctx, store.SetIntakeDecisionExecutedParams{
		IssueID:     sql.NullInt64{Int64: issueID, Valid: issueID > 0},
		WorkspaceID: sql.NullInt64{Int64: wsID, Valid: wsID > 0},
		ID:          accepted.ID,
	}); err != nil {
		t.Fatalf("mark executed: %v", err)
	}

	after, err := f.q.ListPendingIntakeDecisions(ctx, store.ListPendingIntakeDecisionsParams{
		ProjectID: f.projectID, Decision: "accept",
	})
	if err != nil {
		t.Fatalf("list pending after accept: %v", err)
	}
	if len(after) != 1 || after[0].ID != pending[1].ID {
		t.Fatalf("pending after accept = %v, want only suggestion %d untouched", after, pending[1].ID)
	}
	if after[0].ConsumedAt.Valid {
		t.Fatalf("suggestion %d was consumed by a manual accept", after[0].ID)
	}
}
