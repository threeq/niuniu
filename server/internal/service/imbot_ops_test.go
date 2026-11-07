package service

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/opsbot"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// --- Ops Bot routing / approval cards (Epic #788, design §3.4, §10, §13.4) --

func insertOpsBotRow(t *testing.T, db *sql.DB, ownerType string, ownerID int64, role string, controlWS int64) int64 {
	t.Helper()
	var existing int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ops_bots`).Scan(&existing); err != nil {
		t.Fatalf("count ops bots: %v", err)
	}
	accountRef := "acct-" + strconv.Itoa(existing+1)
	var ws sql.NullInt64
	if controlWS > 0 {
		ws = sql.NullInt64{Int64: controlWS, Valid: true}
	}
	res, err := db.Exec(`INSERT INTO ops_bots
		(owner_type, owner_id, role, pack_id, pack_version, account_ref, display_name, control_workspace_id)
		VALUES (?, ?, ?, 'test_pack', 1, ?, '测试 Bot', ?)`,
		ownerType, ownerID, role, accountRef, ws)
	if err != nil {
		t.Fatalf("insert ops bot: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("ops bot id: %v", err)
	}
	return id
}

func setOpsBotDirector(t *testing.T, db *sql.DB, botID, directorID int64) {
	t.Helper()
	if _, err := db.Exec(`UPDATE ops_bots SET director_bot_id = ? WHERE id = ?`, directorID, botID); err != nil {
		t.Fatalf("set director: %v", err)
	}
}

func bindChatToOpsBot(t *testing.T, db *sql.DB, chatID, botID int64) {
	t.Helper()
	if _, err := db.Exec(`UPDATE im_bot_chats SET ops_bot_id = ? WHERE id = ?`, botID, chatID); err != nil {
		t.Fatalf("bind chat to bot: %v", err)
	}
}

func newOpsApproval(t *testing.T, f *imbotFixture) *opsbot.Approval {
	t.Helper()
	return opsbot.NewApproval(store.Wrap(f.db), opsbot.ApprovalPolicy{})
}

func lastPushText(f *imbotFixture) string {
	if len(f.adapter.pushes) == 0 {
		return ""
	}
	return f.adapter.pushes[len(f.adapter.pushes)-1].Text
}

// TestOps_BoundChatRoutesToBotWorkspace: a chat bound to an Ops Bot delivers
// into that bot's control workspace, not the project's intake pipeline.
func TestOps_BoundChatRoutesToBotWorkspace(t *testing.T) {
	f := newIMBotFixture(t)
	_, botWS := f.newWorkspace(t, "bot-ctrl", "/tmp/ws-bot")
	botID := insertOpsBotRow(t, f.db, "user", 1, opsbot.RoleAccount, botWS)
	chat := f.activeChat(t, "oc_bot")
	bindChatToOpsBot(t, f.db, chat.ID, botID)
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_bot",
		Text: "发一篇秋季爆款", Kind: "message", EventID: "e1",
	})

	if f.router.calls != 0 {
		t.Fatalf("bound chat must not run project intake: router calls=%d", f.router.calls)
	}
	if len(f.deliverer.calls) != 1 {
		t.Fatalf("deliver calls = %d, want 1", len(f.deliverer.calls))
	}
	got := f.deliverer.calls[0]
	if got.workspaceID != botWS || got.workDir != "/tmp/ws-bot" {
		t.Fatalf("delivered to %+v, want bot control workspace %d", got, botWS)
	}
	if !strings.Contains(got.content, "秋季爆款") {
		t.Fatalf("delivered content %q lost the operator text", got.content)
	}
}

// TestOps_UnboundChatUsesIntake: a chat with no ops_bot_id keeps the original
// project-intake behaviour.
func TestOps_UnboundChatUsesIntake(t *testing.T) {
	f := newIMBotFixture(t)
	f.activeChat(t, "oc_plain")
	f.router.queue = []PlanTarget{{IssueID: f.issueID, WorkspaceID: f.wsID}}

	f.svc.HandleInbound(context.Background(), imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_plain",
		Text: "帮我做张表", Kind: "message", EventID: "e1",
	})

	if f.router.calls != 1 || f.router.lastPID != f.projectID {
		t.Fatalf("unbound chat must use intake: calls=%d pid=%d", f.router.calls, f.router.lastPID)
	}
	if len(f.deliverer.calls) != 1 || f.deliverer.calls[0].workspaceID != f.wsID {
		t.Fatalf("unbound delivery mismatch: %+v", f.deliverer.calls)
	}
}

// TestOps_ApprovalCallbackApproveAndDuplicate: a card tap drives Approval.Decide
// exactly once; a repeat tap reports "already handled" without re-deciding.
func TestOps_ApprovalCallbackApproveAndDuplicate(t *testing.T) {
	f := newIMBotFixture(t)
	botID := insertOpsBotRow(t, f.db, "user", 1, opsbot.RoleAccount, f.wsID)
	chat := f.activeChat(t, "oc_appr")
	bindChatToOpsBot(t, f.db, chat.ID, botID)

	approval := newOpsApproval(t, f)
	f.svc.SetOpsApproval(approval)
	ctx := context.Background()
	req, err := approval.Create(ctx, opsbot.ApprovalInput{
		BotID: botID, WorkspaceID: f.wsID, Type: opsbot.ApprovalPublish,
		PreviewJSON: `{"title":"秋季上新","summary":"九宫格"}`,
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}

	cb := opsbot.ApprovalCallbackValue(opsbot.ActionApproved, req.ID)
	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_appr", Kind: "action_callback", CallbackData: cb, EventID: "e1",
	})

	got, err := approval.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if got.Status != opsbot.ApprovalStatusAnswered {
		t.Fatalf("status = %q, want answered", got.Status)
	}
	if got.DecidedBy != 1 {
		t.Fatalf("decided_by = %d, want chat owner 1", got.DecidedBy)
	}

	// The same tap again must be idempotent and tell the operator.
	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_appr", Kind: "action_callback", CallbackData: cb, EventID: "e2",
	})
	if txt := lastPushText(f); !strings.Contains(txt, "已经处理过了") {
		t.Fatalf("duplicate tap reply = %q, want already-handled", txt)
	}
}

// TestOps_ApprovalCallbackDirectorProxy: a chat bound to a director bot may
// decide an account bot's card, recording the operator it stood in for.
func TestOps_ApprovalCallbackDirectorProxy(t *testing.T) {
	f := newIMBotFixture(t)
	directorID := insertOpsBotRow(t, f.db, "user", 1, opsbot.RoleDirector, f.wsID)
	accountID := insertOpsBotRow(t, f.db, "user", 1, opsbot.RoleAccount, f.wsID)
	setOpsBotDirector(t, f.db, accountID, directorID)

	chat := f.activeChat(t, "oc_director")
	bindChatToOpsBot(t, f.db, chat.ID, directorID)

	approval := newOpsApproval(t, f)
	f.svc.SetOpsApproval(approval)
	ctx := context.Background()
	req, err := approval.Create(ctx, opsbot.ApprovalInput{
		BotID: accountID, WorkspaceID: f.wsID, Type: opsbot.ApprovalVideo,
		PreviewJSON: `{"duration":"30s"}`,
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}

	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_director", Kind: "action_callback",
		CallbackData: opsbot.ApprovalCallbackValue(opsbot.ActionApproved, req.ID), EventID: "e1",
	})

	got, err := approval.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if got.Status != opsbot.ApprovalStatusAnswered {
		t.Fatalf("status = %q, want answered", got.Status)
	}
	if !strings.Contains(got.MetadataJSON, `"proxy_for"`) {
		t.Fatalf("metadata %s missing proxy_for audit field", got.MetadataJSON)
	}
	if got.DecisionSource != "proxy" {
		t.Fatalf("decision_source = %q, want proxy", got.DecisionSource)
	}
}

// TestOps_ApprovalCallback_ModifyPromptsAndCapturesRevision: "修改" parks the
// card and the operator's next message becomes the modified decision reason.
func TestOps_ApprovalCallback_ModifyPromptsAndCapturesRevision(t *testing.T) {
	f := newIMBotFixture(t)
	botID := insertOpsBotRow(t, f.db, "user", 1, opsbot.RoleAccount, f.wsID)
	chat := f.activeChat(t, "oc_mod")
	bindChatToOpsBot(t, f.db, chat.ID, botID)

	approval := newOpsApproval(t, f)
	f.svc.SetOpsApproval(approval)
	ctx := context.Background()
	req, err := approval.Create(ctx, opsbot.ApprovalInput{
		BotID: botID, WorkspaceID: f.wsID, Type: opsbot.ApprovalReply,
		PreviewJSON: `{"comment":"多少钱"}`,
	})
	if err != nil {
		t.Fatalf("create approval: %v", err)
	}

	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_mod", Kind: "action_callback",
		CallbackData: opsbot.ApprovalCallbackValue(opsbot.ActionModified, req.ID), EventID: "e1",
	})
	if txt := lastPushText(f); !strings.Contains(txt, "修改意见") {
		t.Fatalf("modify prompt = %q", txt)
	}

	f.svc.HandleInbound(ctx, imbot.InboundEvent{
		ChannelID: f.channelID, ChatExtID: "oc_mod", Text: "把价格写清楚一点", Kind: "message", EventID: "e2",
	})

	got, err := approval.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if got.Status != opsbot.ApprovalStatusAnswered {
		t.Fatalf("status = %q, want answered", got.Status)
	}
	if !strings.Contains(got.AnswersJSON, "把价格写清楚一点") {
		t.Fatalf("answers %s missing the revision reason", got.AnswersJSON)
	}
	// The revision message must not also be delivered to project intake.
	if f.router.calls != 0 {
		t.Fatalf("revision consumed by approval but intake ran: calls=%d", f.router.calls)
	}
}

// TestOps_NotificationFailureDoesNotBlockApproval: a failing push is logged and
// the approval row still commits.
func TestOps_NotificationFailureDoesNotBlockApproval(t *testing.T) {
	f := newIMBotFixture(t)
	botID := insertOpsBotRow(t, f.db, "user", 1, opsbot.RoleAccount, f.wsID)
	chat := f.activeChat(t, "oc_notify")
	bindChatToOpsBot(t, f.db, chat.ID, botID)

	f.svc.SetOpsNotifier(&opsbot.Notifier{Send: func(context.Context, opsbot.NotifyTarget, opsbot.NotifyPayload) error {
		return errors.New("network down")
	}})

	approval := newOpsApproval(t, f)
	approval.OnCreated = func(ctx context.Context, req opsbot.ApprovalRequest) error {
		f.svc.NotifyOpsApproval(ctx, req)
		return nil
	}
	ctx := context.Background()

	req, err := approval.Create(ctx, opsbot.ApprovalInput{
		BotID: botID, WorkspaceID: f.wsID, Type: opsbot.ApprovalPublish,
		PreviewJSON: `{"title":"通知失败"}`,
	})
	if err != nil {
		t.Fatalf("approval creation must survive a notification failure: %v", err)
	}
	got, err := approval.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if got.Status != opsbot.ApprovalStatusPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}
}
