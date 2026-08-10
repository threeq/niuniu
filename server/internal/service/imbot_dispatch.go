package service

import (
	"context"
	"database/sql"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// IMBotDispatcher subscribes to the in-process event bus and pushes outbound
// notifications to the IM chats of the affected project (Feishu/Lark WS long
// connection Push — LAN-friendly, no public URL). It is the outbound half of
// the W1 end-to-end path: an agent finishes / needs approval -> the bound
// project's active chats get a de-jargonized message.
type IMBotDispatcher struct {
	bus      *event.Bus
	q        *store.Queries
	svc      *IMBotService
	adapters map[imbot.ChannelType]imbot.ChannelAdapter

	mu   sync.Mutex
	ch   chan event.OutputEvent
	stop chan struct{}
	done chan struct{}
}

// NewIMBotDispatcher builds a dispatcher. svc supplies credential decryption
// (decrypt only happens in the service layer).
func NewIMBotDispatcher(bus *event.Bus, q *store.Queries, svc *IMBotService, adapters map[imbot.ChannelType]imbot.ChannelAdapter) *IMBotDispatcher {
	return &IMBotDispatcher{bus: bus, q: q, svc: svc, adapters: adapters}
}

// Start subscribes to the bus and runs the dispatch loop until Stop.
func (d *IMBotDispatcher) Start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ch != nil {
		return
	}
	d.ch = d.bus.Subscribe()
	d.stop = make(chan struct{})
	d.done = make(chan struct{})
	go d.loop(d.ch, d.stop, d.done)
	slog.Info("imbot: dispatcher started")
}

// Stop unsubscribes and waits for the loop to exit.
func (d *IMBotDispatcher) Stop() {
	d.mu.Lock()
	ch, stop, done := d.ch, d.stop, d.done
	d.ch, d.stop, d.done = nil, nil, nil
	d.mu.Unlock()
	if ch == nil {
		return
	}
	close(stop)
	d.bus.Unsubscribe(ch)
	<-done
}

func (d *IMBotDispatcher) loop(ch chan event.OutputEvent, stop, done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-stop:
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			d.handle(ev)
		}
	}
}

// maxOutboundLen caps a forwarded agent reply so a long answer never trips a
// platform message-size limit (Telegram ~4096; keep headroom for the title).
const maxOutboundLen = 3500

// interestedIn reports whether an event type should reach IM chats. Streaming
// text/tool noise is ignored; only meaningful lifecycle/interaction events.
// EventAgentDone now carries the agent's real final reply (proxy publishes the
// turn's last assistant text) — that is the primary per-turn chat message.
// EventWorkspaceCompleted is intentionally NOT forwarded: it would double up on
// the agent_done reply and re-create the old "『…』已完成" spam.
func interestedIn(t string) bool {
	switch t {
	case event.EventAgentDone,
		event.EventScheduleTrigger,
		event.EventGateDone,
		event.EventAskUserRequest,
		event.EventPermissionRequest:
		return true
	}
	return false
}

// outTarget is one concrete chat (optionally in-thread) that should receive an
// outbound message, resolved together with the channel credential needed to push.
type outTarget struct {
	channelID     int64
	chatID        int64
	channelType   string
	credentialEnc string
	chatExtID     string
	threadExtID   string
}

func (d *IMBotDispatcher) handle(ev event.OutputEvent) {
	if !interestedIn(ev.Type) || ev.WorkspaceId == 0 {
		return
	}
	slog.Debug("imbot: dispatch event received", "type", ev.Type, "workspace", ev.WorkspaceId)
	ctx := context.Background()

	// The agent finished this workspace's turn — clear the 🐂 "正在执行中"
	// markers placed on the inbound messages that drove it (runs regardless of
	// whether the reply below has any IM target).
	if ev.Type == event.EventAgentDone {
		d.svc.clearProcessingReactions(ctx, ev.WorkspaceId)
	}
	pctx, err := d.q.GetProjectContextByWorkspace(ctx, ev.WorkspaceId)
	if err != nil {
		return // workspace not resolvable to a project (e.g. assistant scratch)
	}
	issueID := int64(0)
	if pctx.IssueID.Valid {
		issueID = pctx.IssueID.Int64
	}
	text, buttons := renderOutbound(ev, issueID, pctx.IssueTitle)
	if text == "" {
		return
	}

	// Scope the push to the chats that actually own this conversation — a thread
	// bound to the issue, or a chat whose active/pinned pointer is this issue.
	// A task with no IM binding (pure WebUI work) yields no targets, so IM stays
	// quiet instead of broadcasting every project workspace's reply to every chat.
	targets := d.resolveTargets(ctx, pctx.ProjectID, issueID)
	// Diagnostic: confirm the dispatcher reached the push stage and what it is
	// sending (runes) and to how many chats. Pairs with each adapter's send log
	// to trace an outbound message end-to-end when a reply goes missing in IM.
	slog.Info("imbot: dispatch outbound", "workspace", ev.WorkspaceId, "issue", issueID, "type", ev.Type, "runes", utf8.RuneCountInString(text), "targets", len(targets))
	for _, tg := range targets {
		adapter, ok := d.adapters[imbot.ChannelType(tg.channelType)]
		if !ok {
			continue
		}
		cred, err := d.svc.decryptCred(tg.channelType, tg.credentialEnc)
		if err != nil {
			slog.Warn("imbot: dispatch decrypt failed", "channel", tg.channelID, "error", err)
			continue
		}
		msg := imbot.OutboundMessage{
			ChatExtID:   tg.chatExtID,
			ThreadExtID: tg.threadExtID,
			Text:        text,
			Buttons:     buttons,
		}
		if err := adapter.Push(ctx, cred, msg); err != nil {
			slog.Warn("imbot: outbound push failed", "channel", tg.channelID, "chat", tg.chatID, "error", err)
		}
	}
}

// resolveTargets returns the active chats of a project that are bound to issueID,
// each paired with the thread (if any) that owns the conversation. A chat is a
// target when it has a thread bound to the issue (design §4.4 thread routing) OR
// its active/pinned pointer is the issue (the no-thread DM case). issueID==0 (an
// unlinked scratch workspace) has no owner and yields nothing.
func (d *IMBotDispatcher) resolveTargets(ctx context.Context, projectID, issueID int64) []outTarget {
	if issueID == 0 {
		return nil
	}
	threadByChat := map[int64]string{}
	if threads, err := d.q.ListIMBotThreadsByIssue(ctx, issueID); err == nil {
		for _, th := range threads {
			threadByChat[th.ChatID] = th.ThreadExtID
		}
	}
	// Shared-bot routing (design §5): the chats that own this conversation are
	// those ROUTED to this project (chat.project_id), not those under a
	// project-owned channel. Query them directly, then look up each chat's
	// channel for the credential to push with.
	chats, err := d.q.ListActiveIMBotChatsByProject(ctx, sql.NullInt64{Int64: projectID, Valid: true})
	if err != nil || len(chats) == 0 {
		return nil
	}
	channelByID := map[int64]store.ImBotChannel{}
	var out []outTarget
	for _, chat := range chats {
		thread, bound := threadByChat[chat.ID]
		isActive := chat.ActiveIssueID.Valid && chat.ActiveIssueID.Int64 == issueID
		isPinned := chat.PinnedIssueID.Valid && chat.PinnedIssueID.Int64 == issueID
		if !bound && !isActive && !isPinned {
			continue
		}
		ch, ok := channelByID[chat.ChannelID]
		if !ok {
			loaded, cerr := d.q.GetIMBotChannel(ctx, chat.ChannelID)
			if cerr != nil || loaded.Status != "active" {
				continue // channel gone or disabled — skip this chat
			}
			ch = loaded
			channelByID[chat.ChannelID] = ch
		}
		out = append(out, outTarget{
			channelID: ch.ID, chatID: chat.ID,
			channelType: ch.ChannelType, credentialEnc: ch.CredentialEnc,
			chatExtID: chat.ChatExtID, threadExtID: thread,
		})
	}
	return out
}

// outboundTitle is the `#<id> <名字>` line prefixed on every outbound message so
// the user can tell which conversation (workspace/task) it came from — and can
// reply `#<id>` to switch back to it (see parseHashSwitch). The full title is
// shown (no length cap); the `#<id>` prefix has no space so it matches the reply
// syntax verbatim.
func outboundTitle(issueID int64, issueTitle string) string {
	return formatTaskRef(issueID, issueTitle)
}

// formatTaskRef renders the `#<id> <标题>` reference shared by the outbound header
// and the new-conversation task marker. The full title is shown (no length cap);
// a blank title falls back to "任务". The `#<id>` prefix has no space so it
// matches the `#<id>` reply-to-switch syntax verbatim.
func formatTaskRef(issueID int64, issueTitle string) string {
	title := strings.TrimSpace(issueTitle)
	if title == "" {
		title = "任务"
	}
	return "#" + strconv.FormatInt(issueID, 10) + " " + title
}

// truncateOutbound bounds a forwarded reply to maxOutboundLen runes, appending a
// marker so a clipped answer is obviously incomplete rather than silently cut.
func truncateOutbound(s string) string {
	r := []rune(s)
	if len(r) <= maxOutboundLen {
		return s
	}
	return string(r[:maxOutboundLen]) + "\n…（内容较长已截断，完整结果请到牛牛里查看）"
}

// renderOutbound turns a bus event into the message pushed to IM. Every message
// is titled with `#<id> <名字>` (outboundTitle). EventAgentDone forwards the
// agent's real reply verbatim (below the title); the rest are de-jargonized
// interaction/lifecycle lines. For a permission request it also returns the
// approve/deny buttons whose callback payloads the inbound handler decodes back
// into a PermissionService.Decide (the W2 permission闭环).
func renderOutbound(ev event.OutputEvent, issueID int64, issueTitle string) (string, []imbot.Button) {
	header := outboundTitle(issueID, issueTitle)
	switch ev.Type {
	case event.EventAgentDone:
		body := strings.TrimSpace(ev.Content)
		if body == "" || body == "completed" {
			body = "✅ 已处理完成。"
		}
		return header + "\n\n" + truncateOutbound(body), nil
	case event.EventScheduleTrigger:
		return header + "\n\n⏰ 定时任务已触发。", nil
	case event.EventGateDone:
		// Informative result instead of a bare "检查已完成": pass/fail + failure count
		// so the user knows whether to look, without opening 牛牛.
		if ev.GateDone != nil && !ev.GateDone.Passed {
			n := ev.GateDone.FailureCount
			if n <= 0 {
				n = 1
			}
			return header + "\n\n🔎 检查未通过，发现 " + strconv.Itoa(n) + " 处问题，请到牛牛里查看详情。", nil
		}
		return header + "\n\n✅ 检查通过。", nil
	case event.EventAskUserRequest:
		return renderAskUser(header, ev)
	case event.EventPermissionRequest:
		if ev.PermissionRequest == nil || ev.PermissionRequest.RequestID <= 0 {
			// No decidable request id — fall back to a "go check in 牛牛" nudge.
			return header + "\n\n🔐 请求执行一项操作，请到牛牛里批准或拒绝。", nil
		}
		id := strconv.FormatInt(ev.PermissionRequest.RequestID, 10)
		text := header + "\n\n🔐 想执行一项操作"
		if tool := strings.TrimSpace(ev.PermissionRequest.ToolName); tool != "" {
			text += "（" + tool + "）"
		}
		text += "，允许吗？"
		buttons := []imbot.Button{
			{Label: "允许", Value: "permission:approve:" + id},
			{Label: "始终允许", Value: "permission:always:" + id},
			{Label: "拒绝", Value: "permission:deny:" + id},
		}
		return text, buttons
	}
	return "", nil
}

// maxAskUserButtons caps how many option buttons a pushed ask-user card carries;
// beyond it (or for multi-question / multi-select requests) the card degrades to a
// "go answer in 牛牛" nudge rather than rendering an unwieldy button wall.
const maxAskUserButtons = 10

// renderAskUser turns an ask_user request into a question card with one tappable
// button per option — the in-IM answer path. It only handles the common shape
// (exactly one single-select question with a manageable option count); anything
// richer (several questions, multi-select) still routes the user to 牛牛, since a
// flat button row cannot express it faithfully.
func renderAskUser(header string, ev event.OutputEvent) (string, []imbot.Button) {
	nudge := header + "\n\n❓ 需要你的确认，请到牛牛里查看。"
	d := ev.AskUserRequest
	if d == nil || d.RequestID <= 0 || len(d.Questions) != 1 {
		return nudge, nil
	}
	q := d.Questions[0]
	if q.MultiSelect || len(q.Options) == 0 || len(q.Options) > maxAskUserButtons {
		return nudge, nil
	}
	id := strconv.FormatInt(d.RequestID, 10)
	text := header + "\n\n❓ "
	if h := strings.TrimSpace(q.Header); h != "" {
		text += "[" + h + "] "
	}
	text += strings.TrimSpace(q.Question)
	buttons := make([]imbot.Button, 0, len(q.Options))
	for i, opt := range q.Options {
		label := strings.TrimSpace(opt.Label)
		if label == "" {
			continue
		}
		buttons = append(buttons, imbot.Button{Label: label, Value: "askuser:" + id + ":" + strconv.Itoa(i)})
	}
	if len(buttons) == 0 {
		return nudge, nil
	}
	return text, buttons
}
