package service

import (
	"context"
	"strconv"
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
