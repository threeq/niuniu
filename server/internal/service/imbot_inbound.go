package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// HandleInbound is the channel-agnostic inbound entry point (W2). Both the
// ConnectorManager's stream long connection and the optional public webhook feed
// the same normalized imbot.InboundEvent here. It implements the design §4.3
// pipeline: idempotent dedupe -> chat admission/pairing -> permission callback
// or slash command -> task resolution (thread > pinned > active pointer) ->
// RouteInProject (continue/new in the bound project) -> agentproxy.Deliver.
//
// It never returns an error (it is a fire-and-forget handler invoked from the
// connector goroutine); failures are logged and, where useful, surfaced back to
// the chat as a de-jargonized message.
func (s *IMBotService) HandleInbound(ctx context.Context, ev imbot.InboundEvent) {
	channel, err := s.q.GetIMBotChannel(ctx, ev.ChannelID)
	if err != nil {
		return // channel deleted mid-flight
	}
	if channel.Status != "active" {
		return
	}

	// Diagnostic: surface every inbound event so a platform that goes silent
	// (e.g. a WS connector not delivering, or a chat stuck in pairing/nudge) can
	// be told apart from a slash-command/outbound issue. The inbound success path
	// is otherwise log-free, which hides where a message is dropped.
	slog.Info("imbot: inbound event", "channel_type", channel.ChannelType, "chat", ev.ChatExtID, "kind", ev.Kind, "text", truncInbound(ev.Text))

	// Idempotency: platforms redeliver events; process each id once per channel.
	if ev.EventID != "" {
		if _, err := s.q.GetIMBotInboxEvent(ctx, store.GetIMBotInboxEventParams{ChannelID: ev.ChannelID, EventExtID: ev.EventID}); err == nil {
			return // already handled
		}
		if err := s.q.CreateIMBotInboxEvent(ctx, store.CreateIMBotInboxEventParams{ChannelID: ev.ChannelID, EventExtID: ev.EventID}); err != nil {
			slog.Warn("imbot: inbox insert failed", "channel", ev.ChannelID, "event", ev.EventID, "error", err)
		}
	}

	// Chat admission: unknown chat -> seed pending pairing; non-active -> nudge.
	chat, err := s.q.GetIMBotChatByExt(ctx, store.GetIMBotChatByExtParams{ChannelID: ev.ChannelID, ChatExtID: ev.ChatExtID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.startPairing(ctx, channel, ev)
		}
		return
	}
	// Routing target is the chat's assigned project (design §4): a shared bot
	// fans different chats to different projects. A chat that is not active, or
	// active but with no project assigned, is not yet routable — nudge and stop
	// (never deliver business messages to an unassigned chat).
	if chat.Status != "active" || !chat.ProjectID.Valid {
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
			"牛牛还未获准在此聊天中工作，请让管理员在设置里批准并选择目标项目后再试。")
		return
	}
	routeProjectID := chat.ProjectID.Int64

	// Interaction-button callback (permission approve/deny/always, ask-user option)
	// — dispatched by payload prefix, no text routing.
	if ev.Kind == "action_callback" {
		switch {
		case strings.HasPrefix(ev.CallbackData, "askuser:"):
			s.handleAskUserCallback(ctx, channel, chat, ev)
		default:
			s.handlePermissionCallback(ctx, channel, chat, ev)
		}
		return
	}

	text := strings.TrimSpace(ev.Text)
	if text == "" && len(ev.Attachments) == 0 {
		return
	}
	// An attachment-only message (image/file with no caption) still needs a text
	// hook so the router/classifier and the agent have something to act on; the
	// actual files are appended as `[附件: ...]` at delivery time.
	if text == "" {
		text = attachmentPlaceholder(ev.Attachments)
	}

	// Slash commands (optional; the channel already binds a single project).
	// For no-thread channels (Telegram DMs) /issues + /use are how a user keeps
	// several parallel tasks apart in one chat without crosstalk.
	cmd, rest := parseSlashCommand(text)
	slog.Info("imbot: inbound parsed", "channel_type", channel.ChannelType, "chat", ev.ChatExtID, "cmd", cmd, "text_len", utf8.RuneCountInString(text))
	switch cmd {
	case "status":
		s.replyStatus(ctx, channel, chat, ev)
		return
	case "issues", "list", "ls", "conversations":
		s.replyIssues(ctx, channel, chat, ev)
		return
	case "use":
		s.handleUse(ctx, channel, chat, ev, rest)
		return
	case "delete", "del", "rm", "删除":
		s.handleDelete(ctx, channel, chat, ev, rest)
		return
	case "stop", "停止", "暂停":
		s.handleStop(ctx, channel, chat, ev)
		return
	case "detail", "详情", "info":
		s.handleDetail(ctx, channel, chat, ev, rest)
		return
	case "new":
		text = strings.TrimSpace(rest) // fall through to routing with ForceNew
		if text == "" {
			s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, "请在 /new 后面描述要新建的任务。")
			return
		}
	}
	forceNew := cmd == "new"

	// `#<id>` conversation control: switch this chat to the conversation whose id
	// is the issue id shown in an outbound title (`#<id> 名字`). A valid id that
	// resolves to a workspace-backed task in this project switches to it (and
	// delivers any trailing text there); a missing/unknown id falls through to a
	// NEW conversation with the trailing text as its prompt.
	if idTok, body, isHash := parseHashSwitch(text); isHash {
		if idTok != "" {
			if id, perr := strconv.ParseInt(idTok, 10, 64); perr == nil {
				if t, ok := s.findChatTask(ctx, routeProjectID, id); ok {
					s.switchToConversation(ctx, channel, chat, ev, t, body)
					return
				}
				// The id names an existing project issue that has no workspace yet
				// (created on the kanban / by another chat) -> start one and begin
				// work on the issue's own content.
				if s.startIssueWorkspace(ctx, channel, chat, ev, routeProjectID, id, body) {
					return
				}
			}
		}
		// No id, or the id is not a known issue in this project -> start new.
		if body == "" {
			s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
				"没有找到该会话。请在 # 后面描述要新建的任务，或用 /issues 查看所有任务及编号。")
			return
		}
		text = body
		forceNew = true
	}

	// Resolve the target task: thread mapping > pinned > active pointer.
	issueID, wsID := s.resolveTask(ctx, chat, ev.ThreadExtID)

	if wsID == 0 || forceNew {
		if s.dispatch == nil {
			return
		}
		owner, ok := s.projectOwner(ctx, routeProjectID)
		if !ok {
			return
		}
		hint := RouteHint{ForceNew: forceNew}
		// A no-thread chat with an active pointer continues that task unless the
		// user forces new; resolveTask already applied that, so ActiveIssueID is
		// only a hint for the classifier tie-break.
		if ev.ThreadExtID == "" && chat.ActiveIssueID.Valid && !forceNew {
			hint.ActiveIssueID = chat.ActiveIssueID.Int64
		}
		target, rerr := s.dispatch.RouteInProject(ctx, owner, routeProjectID, text, hint)
		if rerr != nil {
			slog.Warn("imbot: route failed", "channel", channel.ID, "project", routeProjectID, "error", rerr)
			s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, "牛牛暂时无法开始这个任务，请稍后再试。")
			return
		}
		issueID, wsID = target.IssueID, target.WorkspaceID

		// Bind the new task so follow-ups land in the same place.
		if ev.ThreadExtID != "" {
			if _, cerr := s.q.CreateIMBotThread(ctx, store.CreateIMBotThreadParams{
				ChatID: chat.ID, ThreadExtID: ev.ThreadExtID, IssueID: issueID, WorkspaceID: wsID,
			}); cerr != nil {
				slog.Warn("imbot: bind thread failed", "chat", chat.ID, "thread", ev.ThreadExtID, "error", cerr)
			}
		} else {
			s.setActiveIssue(ctx, chat, issueID)
		}

		// A NEW conversation was just created — reply once with its `#<id> <标题>`
		// so the user knows the task id (and can `#<id>` back to it later). Only
		// here, never on follow-ups, to avoid flooding the chat with replies.
		s.replyTaskMarker(ctx, channel, ev, issueID)
	}

	// Deliver the message into the workspace's agent session (the step the WebUI
	// path leaves to the client; IM has no client so the server delivers).
	s.deliverToWorkspace(ctx, channel, ev, wsID, text)
}

// deliverToWorkspace hands text (and any inbound attachments) to a workspace's
// agent session — the server-side equivalent of the WebUI client send. Inbound
// images/files are downloaded and written into the workspace's `.attachments/`
// dir, then referenced both in the content (`[附件: ...]` suffix, matching the
// WebUI send format) and in the attachments JSON the deliverer forwards. It is a
// no-op when no deliverer is wired (tests) or the workspace has vanished.
func (s *IMBotService) deliverToWorkspace(ctx context.Context, channel store.ImBotChannel, ev imbot.InboundEvent, wsID int64, text string) {
	if s.deliverer == nil {
		return
	}
	ws, err := s.q.GetWorkspace(ctx, wsID)
	if err != nil {
		return
	}
	// Mark the message with the 🐂 "正在执行中" reaction now that it is committed
	// to this workspace's agent; it is cleared when the agent finishes (agent_done).
	// (The `#<id> <标题>` text marker is posted only when a NEW conversation is
	// created — see HandleInbound — not on every message, to avoid reply spam.)
	s.reactProcessing(ctx, channel, ev, wsID)

	content, attachJSON := text, ""
	if len(ev.Attachments) > 0 {
		if saved := s.materializeAttachments(ctx, channel, ev, ws.Path); len(saved) > 0 {
			paths := make([]string, len(saved))
			for i, a := range saved {
				paths[i] = a.Path
			}
			content = strings.TrimSpace(text + "\n\n[附件: " + strings.Join(paths, ", ") + "]")
			if b, merr := json.Marshal(saved); merr == nil {
				attachJSON = string(b)
			}
		}
	}
	if _, _, derr := s.deliverer.Deliver(ctx, wsID, ws.Path, content, attachJSON); derr != nil {
		slog.Warn("imbot: deliver failed", "workspace", wsID, "error", derr)
	}
}

// savedAttachment is one materialized inbound file, in the exact JSON shape the
// agentproxy deliverer + WebUI expect (`{path,type,name,mimeType,size}` with a
// workspace-relative path).
type savedAttachment struct {
	Path     string `json:"path"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

// materializeAttachments downloads each inbound attachment via the adapter's
// resource fetcher and writes it under `<wsPath>/.attachments/`, returning the
// records to hand to the deliverer. Best-effort: an adapter with no fetcher, a
// download error, or a write error simply drops that file (logged) rather than
// failing the whole message.
func (s *IMBotService) materializeAttachments(ctx context.Context, channel store.ImBotChannel, ev imbot.InboundEvent, wsPath string) []savedAttachment {
	adapter, ok := s.adapters[imbot.ChannelType(channel.ChannelType)]
	if !ok {
		return nil
	}
	fetcher, ok := adapter.(imbot.MessageResourceFetcher)
	if !ok {
		return nil
	}
	cred, err := s.decryptCred(channel.ChannelType, channel.CredentialEnc)
	if err != nil {
		return nil
	}
	dir := filepath.Join(wsPath, ".attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("imbot: mkdir attachments failed", "workspace", wsPath, "error", err)
		return nil
	}
	var out []savedAttachment
	for i, att := range ev.Attachments {
		data, ferr := fetcher.FetchResource(ctx, cred, ev.MessageExtID, att)
		if ferr != nil {
			slog.Warn("imbot: fetch attachment failed", "channel", channel.ID, "kind", att.Kind, "error", ferr)
			continue
		}
		name, mimeType := attachmentFileName(att, data, ev.MessageExtID, i)
		dest := filepath.Join(dir, name)
		if werr := os.WriteFile(dest, data, 0o644); werr != nil {
			slog.Warn("imbot: write attachment failed", "path", dest, "error", werr)
			continue
		}
		out = append(out, savedAttachment{
			Path: ".attachments/" + name, Type: att.Kind, Name: name,
			MimeType: mimeType, Size: int64(len(data)),
		})
	}
	return out
}

// switchToConversation points the chat at an existing conversation (issue) and
// either delivers the trailing text there or confirms the switch when there is
// none. For a thread chat it also binds the thread to the issue so subsequent
// in-thread messages keep continuing it. This is the `#<id>` switch's success
// path (the id resolved to a live workspace-backed task in this project).
func (s *IMBotService) switchToConversation(ctx context.Context, channel store.ImBotChannel, chat store.ImBotChat, ev imbot.InboundEvent, t chatTask, body string) {
	s.setActiveIssue(ctx, chat, t.issueID)
	if ev.ThreadExtID != "" {
		if _, err := s.q.GetIMBotThreadByExt(ctx, store.GetIMBotThreadByExtParams{ChatID: chat.ID, ThreadExtID: ev.ThreadExtID}); errors.Is(err, sql.ErrNoRows) {
			if _, cerr := s.q.CreateIMBotThread(ctx, store.CreateIMBotThreadParams{
				ChatID: chat.ID, ThreadExtID: ev.ThreadExtID, IssueID: t.issueID, WorkspaceID: t.wsID,
			}); cerr != nil {
				slog.Warn("imbot: bind thread on switch failed", "chat", chat.ID, "thread", ev.ThreadExtID, "error", cerr)
			}
		}
	}
	if body == "" {
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
			"已切换到会话 #"+strconv.FormatInt(t.issueID, 10)+" - "+t.title+"。接下来的消息都会发给它。")
		return
	}
	s.deliverToWorkspace(ctx, channel, ev, t.wsID, body)
}

// startIssueWorkspace starts a backing workspace for an existing project issue
// that has none yet (the `#<id>` control aimed at a kanban issue that was created
// without a workspace), binds this chat to it, and begins work by delivering the
// issue's own content plus any trailing instruction the user appended after the
// id. It returns true when it HANDLED the id — the id named an issue of this
// project, whether the workspace start then succeeded or failed — so the caller
// does not fall through to creating a brand-new task. It returns false only when
// the id is not an issue of this project (caller treats `#<id>` as new-conversation).
func (s *IMBotService) startIssueWorkspace(ctx context.Context, channel store.ImBotChannel, chat store.ImBotChat, ev imbot.InboundEvent, projectID, issueID int64, body string) bool {
	issue, ok := s.issueInProject(ctx, projectID, issueID)
	if !ok {
		return false // not an issue of this project -> caller falls through to new
	}
	if s.starter == nil || s.deliverer == nil {
		return false
	}
	owner, ok := s.projectOwner(ctx, projectID)
	if !ok {
		return false
	}
	target, err := s.starter.StartWorkspaceForExistingIssue(ctx, owner, projectID, issueID)
	if err != nil {
		slog.Warn("imbot: start workspace for issue failed", "project", projectID, "issue", issueID, "error", err)
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, "牛牛暂时无法为该任务启动工作空间，请稍后再试。")
		return true
	}
	// Bind the chat to the started task so follow-ups continue it (active pointer
	// for a no-thread chat, thread mapping when in-thread).
	s.setActiveIssue(ctx, chat, target.IssueID)
	if ev.ThreadExtID != "" {
		if _, gerr := s.q.GetIMBotThreadByExt(ctx, store.GetIMBotThreadByExtParams{ChatID: chat.ID, ThreadExtID: ev.ThreadExtID}); errors.Is(gerr, sql.ErrNoRows) {
			if _, cerr := s.q.CreateIMBotThread(ctx, store.CreateIMBotThreadParams{
				ChatID: chat.ID, ThreadExtID: ev.ThreadExtID, IssueID: target.IssueID, WorkspaceID: target.WorkspaceID,
			}); cerr != nil {
				slog.Warn("imbot: bind thread on issue start failed", "chat", chat.ID, "thread", ev.ThreadExtID, "error", cerr)
			}
		}
	}
	// Announce the picked-up task, then deliver the issue's content so the agent
	// starts working from the kanban card.
	s.replyTaskMarker(ctx, channel, ev, target.IssueID)
	s.deliverToWorkspace(ctx, channel, ev, target.WorkspaceID, issueStartPrompt(issue, body))
	return true
}

// issueInProject loads an issue and confirms it belongs to projectID, so a shared
// bot never resolves another project's issue id.
func (s *IMBotService) issueInProject(ctx context.Context, projectID, issueID int64) (store.Issue, bool) {
	issue, err := s.q.GetIssue(ctx, issueID)
	if err != nil {
		return store.Issue{}, false
	}
	col, err := s.q.GetColumn(ctx, issue.ColumnID)
	if err != nil || col.ProjectID != projectID {
		return store.Issue{}, false
	}
	return issue, true
}

// issueStartPrompt builds the first message delivered to a freshly-started issue
// workspace: the issue's own content (description, else its autohost goal, else
// its title) so the agent works from the kanban card — plus any trailing
// instruction the user appended after `#<id>`.
func issueStartPrompt(issue store.Issue, body string) string {
	content := strings.TrimSpace(issue.Description.String)
	if content == "" {
		content = strings.TrimSpace(issue.GoalCondition)
	}
	if content == "" {
		content = strings.TrimSpace(issue.Title)
	}
	if content == "" {
		content = "请开始处理这个任务。"
	}
	if b := strings.TrimSpace(body); b != "" {
		content = strings.TrimSpace(content + "\n\n" + b)
	}
	return content
}

// attachmentPlaceholder synthesizes caption text for an attachment-only message
// so routing/classification and the agent have a hook. E.g. "[图片]" or
// "[文件: report.pdf]".
func attachmentPlaceholder(atts []imbot.InboundAttachment) string {
	parts := make([]string, 0, len(atts))
	for _, a := range atts {
		label := map[string]string{"image": "图片", "audio": "语音", "video": "视频"}[a.Kind]
		if label == "" {
			label = "文件"
		}
		if strings.TrimSpace(a.Name) != "" {
			parts = append(parts, "["+label+": "+a.Name+"]")
		} else {
			parts = append(parts, "["+label+"]")
		}
	}
	return strings.Join(parts, " ")
}

// attachmentFileName picks a collision-resistant, path-safe filename for a
// downloaded attachment plus its MIME type. It prefers the platform-provided
// name (sanitized, prefixed with a per-message id so two messages' same-named
// files never clash), and otherwise synthesizes one from the sniffed content
// type / kind.
func attachmentFileName(att imbot.InboundAttachment, data []byte, msgID string, idx int) (name, mimeType string) {
	prefix := sanitizeIDPart(msgID) + "_" + strconv.Itoa(idx)
	base := filepath.Base(strings.TrimSpace(att.Name))
	if base != "" && base != "." && base != ".." && !strings.ContainsAny(att.Name, `/\`) {
		mt := mime.TypeByExtension(filepath.Ext(base))
		if mt == "" {
			mt = http.DetectContentType(data)
		}
		return prefix + "-" + base, mt
	}
	mt := http.DetectContentType(data)
	ext := extForContentType(mt)
	if ext == "" {
		ext = map[string]string{"image": ".png", "audio": ".ogg", "video": ".mp4"}[att.Kind]
	}
	kind := att.Kind
	if kind == "" {
		kind = "file"
	}
	return kind + "-" + prefix + ext, mt
}

// sanitizeIDPart reduces a platform message id to a short filesystem-safe token.
func sanitizeIDPart(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 12 {
		s = s[len(s)-12:]
	}
	if s == "" {
		s = "msg"
	}
	return s
}

// extForContentType maps a sniffed MIME type to a file extension for the common
// media kinds Feishu delivers; "" when unknown (caller falls back by kind).
func extForContentType(mt string) string {
	switch {
	case strings.HasPrefix(mt, "image/png"):
		return ".png"
	case strings.HasPrefix(mt, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(mt, "image/gif"):
		return ".gif"
	case strings.HasPrefix(mt, "image/webp"):
		return ".webp"
	case strings.HasPrefix(mt, "application/pdf"):
		return ".pdf"
	}
	return ""
}

// reactProcessing best-effort marks an inbound message with the 🐂 "正在执行中"
// reaction so the user sees 牛牛 has picked it up, and records the reaction id
// keyed by workspace so it can be cleared when the agent finishes. It is a no-op
// when the platform message id is unknown (e.g. callbacks), the adapter cannot
// react, or the credential cannot be decrypted; a live reaction failure is
// logged only — it must never block routing/delivery.
func (s *IMBotService) reactProcessing(ctx context.Context, channel store.ImBotChannel, ev imbot.InboundEvent, wsID int64) {
	if ev.MessageExtID == "" {
		return
	}
	adapter, ok := s.adapters[imbot.ChannelType(channel.ChannelType)]
	if !ok {
		return
	}
	reactor, ok := adapter.(imbot.MessageReactor)
	if !ok {
		return
	}
	cred, err := s.decryptCred(channel.ChannelType, channel.CredentialEnc)
	if err != nil {
		return
	}
	reactionID, err := reactor.React(ctx, cred, ev.MessageExtID, imbot.ReactionProcessing)
	if err != nil {
		slog.Warn("imbot: mark-processing reaction failed", "channel", channel.ID, "error", err)
		return
	}
	if reactionID != "" && wsID != 0 {
		s.recordProcessingReaction(wsID, pendingReaction{
			channelID: channel.ID, messageExtID: ev.MessageExtID, reactionID: reactionID,
		})
	}
}

// replyTaskMarker best-effort replies to the inbound message with a compact
// `#<id> <标题≤9字>` marker so the user learns which (newly-created) task now owns
// the conversation and can `#<id>` back to it. It is posted ONCE, only when a new
// conversation is created — never on follow-up messages — so it does not turn
// every message into a re-quoting reply (that flooded the chat). No-op when the
// message id is unknown or the adapter can't reply; a live failure is logged.
func (s *IMBotService) replyTaskMarker(ctx context.Context, channel store.ImBotChannel, ev imbot.InboundEvent, issueID int64) {
	if ev.MessageExtID == "" || issueID == 0 {
		return
	}
	adapter, ok := s.adapters[imbot.ChannelType(channel.ChannelType)]
	if !ok {
		return
	}
	replier, ok := adapter.(imbot.MessageReplier)
	if !ok {
		return
	}
	cred, err := s.decryptCred(channel.ChannelType, channel.CredentialEnc)
	if err != nil {
		return
	}
	title := ""
	if iss, ierr := s.q.GetIssue(ctx, issueID); ierr == nil {
		title = iss.Title
	}
	// Posted only once per new conversation, so show the full title (no length
	// cap) plus a friendly working line whose emoji is the platform's own (a
	// Feishu `[敲键盘]` shortcode renders verbatim on DingTalk and vice-versa).
	marker := formatTaskRef(issueID, title) + "\n" + workingPrefix(channel.ChannelType) + "牛牛正在为您工作。"
	if err := replier.Reply(ctx, cred, ev.MessageExtID, marker); err != nil {
		slog.Warn("imbot: task-marker reply failed", "channel", channel.ID, "error", err)
	}
}

// workingPrefix is the platform-native emoji prefixing the "牛牛正在为您工作" line
// on the new-conversation task marker: each IM renders its own shortcode, so we
// pick per channel (Feishu's [敲键盘] typing emoji, DingTalk's [忙疯了] frantic
// emoji) and fall back to the 🐂 Unicode emoji for platforms without a native
// shortcode. A cross-platform shortcode would otherwise render as literal text.
func workingPrefix(channelType string) string {
	switch imbot.ChannelType(channelType) {
	case imbot.ChannelLark:
		return "[敲键盘]"
	case imbot.ChannelDingTalk:
		return "[忙疯了]"
	default:
		return "🐂 "
	}
}

// pendingReaction is a "正在执行中" marker placed on an inbound message, held
// until the agent finishes so it can be removed.
type pendingReaction struct {
	channelID    int64
	messageExtID string
	reactionID   string
}

// recordProcessingReaction remembers a placed marker under its workspace so
// clearProcessingReactions can remove it on agent_done.
func (s *IMBotService) recordProcessingReaction(wsID int64, pr pendingReaction) {
	s.procMu.Lock()
	defer s.procMu.Unlock()
	if s.procReactions == nil {
		s.procReactions = map[int64][]pendingReaction{}
	}
	s.procReactions[wsID] = append(s.procReactions[wsID], pr)
}

// clearProcessingReactions removes every 🐂 "正在执行中" marker recorded for a
// workspace — called when the workspace's agent finishes a turn (agent_done), so
// the status marker disappears once the message is processed. Best-effort:
// removal failures are logged, and markers placed in a previous process run
// (not in memory) are simply left in place.
func (s *IMBotService) clearProcessingReactions(ctx context.Context, wsID int64) {
	s.procMu.Lock()
	prs := s.procReactions[wsID]
	delete(s.procReactions, wsID)
	s.procMu.Unlock()
	for _, pr := range prs {
		channel, err := s.q.GetIMBotChannel(ctx, pr.channelID)
		if err != nil {
			continue
		}
		adapter, ok := s.adapters[imbot.ChannelType(channel.ChannelType)]
		if !ok {
			continue
		}
		reactor, ok := adapter.(imbot.MessageReactor)
		if !ok {
			continue
		}
		cred, derr := s.decryptCred(channel.ChannelType, channel.CredentialEnc)
		if derr != nil {
			continue
		}
		if err := reactor.RemoveReaction(ctx, cred, pr.messageExtID, pr.reactionID); err != nil {
			slog.Warn("imbot: clear-processing reaction failed", "workspace", wsID, "error", err)
		}
	}
}

// findChatTask looks up a switchable conversation by its issue id within a
// project. It reuses chatTasks so only workspace-backed, deliverable tasks match.
func (s *IMBotService) findChatTask(ctx context.Context, projectID, issueID int64) (chatTask, bool) {
	for _, t := range s.chatTasks(ctx, projectID) {
		if t.issueID == issueID {
			return t, true
		}
	}
	return chatTask{}, false
}

// HandleWebhook is the optional public-webhook inbound entry (design §5.2): the
// equivalent of a stream frame, arriving over HTTP for publicly-deployed
// channels. It first answers the platform URL-verification challenge, then
// normalizes the body into an InboundEvent (via the adapter's VerifyWebhook) and
// runs the exact same HandleInbound pipeline. echo/isChallenge signal a
// challenge response the handler must echo back verbatim.
//
// Callers are authenticated by the adapter's platform-native signature check in
// VerifyWebhook (Lark Verification Token / Telegram secret_token / WeCom
// msg_signature), using the per-channel secret injected below; a failure maps to
// ErrWebhookUnauthorized (401). The pairing-approval gate (unknown chats only
// create a pending record, never execute) is a second, independent boundary —
// not a substitute for authentication, since it does not protect already-active
// chats or forged permission-approval callbacks.
func (s *IMBotService) HandleWebhook(ctx context.Context, channelID int64, r *http.Request) (echo []byte, isChallenge bool, err error) {
	channel, err := s.q.GetIMBotChannel(ctx, channelID)
	if err != nil {
		return nil, false, ErrIMBotNotFound
	}
	// The public webhook entry serves webhook-mode channels only: a stream
	// channel must never be drivable by an unauthenticated public POST.
	if channel.ConnectionMode != "webhook" {
		return nil, false, ErrIMBotNotFound
	}
	adapter, ok := s.adapters[imbot.ChannelType(channel.ChannelType)]
	if !ok {
		return nil, false, ErrIMBotNotFound
	}
	cred, derr := s.decryptCred(channel.ChannelType, channel.CredentialEnc)
	if derr != nil {
		return nil, false, derr
	}
	// Expose the per-channel webhook secret to the adapter so it can run the
	// platform-native signature check (design §8). It lives in its own column,
	// not the credential blob, so it is injected here at the boundary.
	if cred.Config == nil {
		cred.Config = map[string]any{}
	}
	cred.Config["webhook_secret"] = channel.WebhookSecret

	// URL-verification challenge: WeCom must decrypt echostr with the credential
	// (CredChallenger); Lark answers from the request body alone (Challenge).
	if cc, ok := adapter.(imbot.CredChallenger); ok {
		if body, isCh := cc.ChallengeWithCred(r, cred); isCh {
			return body, true, nil
		}
	} else if body, isCh := adapter.Challenge(r); isCh {
		return body, true, nil
	}

	ev, verr := adapter.VerifyWebhook(r, cred)
	if verr != nil {
		if errors.Is(verr, imbot.ErrWebhookUnauthorized) {
			return nil, false, ErrWebhookUnauthorized
		}
		return nil, false, verr
	}
	ev.ChannelID = channelID
	s.HandleInbound(ctx, ev)
	return nil, false, nil
}

// resolveTask picks the task an inbound message belongs to, in priority order:
//  1. workspace-pinned chat (third layer) -> the pinned issue.
//  2. thread mapping (second layer, preferred) -> the issue bound to the thread.
//     A brand-new thread returns (0,0) so a fresh task is created and bound to
//     it — never falling back to the active pointer (that would cross threads).
//  3. active-issue pointer (second-layer fallback for no-thread chats).
//
// wsID==0 signals "no existing task — route/create".
func (s *IMBotService) resolveTask(ctx context.Context, chat store.ImBotChat, threadExtID string) (issueID, wsID int64) {
	if chat.BindMode == "workspace" && chat.PinnedIssueID.Valid {
		issueID = chat.PinnedIssueID.Int64
		wsID = s.workspaceOfIssue(ctx, issueID)
		return
	}
	if threadExtID != "" {
		if th, err := s.q.GetIMBotThreadByExt(ctx, store.GetIMBotThreadByExtParams{ChatID: chat.ID, ThreadExtID: threadExtID}); err == nil {
			return th.IssueID, th.WorkspaceID
		}
		return 0, 0 // unmapped thread -> new task bound to this thread
	}
	if chat.ActiveIssueID.Valid {
		issueID = chat.ActiveIssueID.Int64
		wsID = s.workspaceOfIssue(ctx, issueID)
	}
	return
}

// workspaceOfIssue returns the (first) workspace id backing an issue, or 0.
func (s *IMBotService) workspaceOfIssue(ctx context.Context, issueID int64) int64 {
	rows, err := s.q.GetWorkspacesByIssue(ctx, sql.NullInt64{Int64: issueID, Valid: true})
	if err != nil || len(rows) == 0 {
		return 0
	}
	return rows[0].ID
}

// setActiveIssue points a no-thread chat at the task it just started so the next
// bare message continues it (the AionUi #2736 "can't switch in IM" fix uses the
// same pointer, flipped by /new or a fresh classifier verdict).
func (s *IMBotService) setActiveIssue(ctx context.Context, chat store.ImBotChat, issueID int64) {
	if _, err := s.q.UpdateIMBotChat(ctx, store.UpdateIMBotChatParams{
		BindMode:      chat.BindMode,
		PinnedIssueID: chat.PinnedIssueID,
		ActiveIssueID: sql.NullInt64{Int64: issueID, Valid: true},
		Status:        chat.Status,
		ID:            chat.ID,
	}); err != nil {
		slog.Warn("imbot: set active issue failed", "chat", chat.ID, "issue", issueID, "error", err)
	}
}

// handlePermissionCallback verifies an interaction-button callback and writes the
// decision back through the existing PermissionService (scope=once). CallbackData
// is "permission:approve:<reqID>" / "permission:deny:<reqID>". Authz: the request
// must resolve to this channel's bound project (so an actor in one project's chat
// can never decide another project's permission request).
func (s *IMBotService) handlePermissionCallback(ctx context.Context, channel store.ImBotChannel, chat store.ImBotChat, ev imbot.InboundEvent) {
	parts := strings.Split(ev.CallbackData, ":")
	if len(parts) != 3 || parts[0] != "permission" {
		return
	}
	reqID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return
	}
	// approve = allow once; always = allow + write a session allowlist entry; deny.
	always := parts[1] == "always"
	allow := parts[1] == "approve" || always
	if s.perm == nil {
		return
	}

	req, err := s.q.GetPermissionRequest(ctx, reqID)
	if err != nil {
		return
	}
	// Authz: the request must resolve to THIS chat's routed project (a shared bot
	// serves several projects, so scope to the chat's project — not the channel's
	// home project — so an actor in one chat can never decide another project's
	// permission request).
	if !chat.ProjectID.Valid {
		return
	}
	pctx, err := s.q.GetProjectContextByWorkspace(ctx, req.WorkspaceID)
	if err != nil || pctx.ProjectID != chat.ProjectID.Int64 {
		return // cross-project or unresolvable — ignore
	}

	d := Decision{Allow: allow, Always: always}
	if !allow {
		d.DenyMessage = "用户在 IM 中拒绝"
	}
	if always {
		// A high-risk tool refuses an 'any' matcher, so pin it to the exact
		// command/path/url; if that can't be derived, degrade to allow-once so the
		// tap still succeeds instead of erroring.
		if d.Matcher = imAlwaysMatcher(req); d.Matcher == nil {
			d.Always = false
		}
	}
	if derr := s.perm.Decide(ctx, reqID, d); derr != nil {
		if errors.Is(derr, ErrAlreadyDecided) {
			s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, "这个操作刚刚已经处理过了。")
			return
		}
		slog.Warn("imbot: permission decide failed", "request", reqID, "error", derr)
		return
	}
	msg := "✅ 已允许该操作。"
	switch {
	case !allow:
		msg = "已拒绝该操作。"
	case d.Always:
		msg = "✅ 已允许，本会话内后续同类操作将自动放行。"
	}
	s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, msg)
}

// imAlwaysMatcher derives the allowlist matcher for an IM "始终允许" tap. A
// non-high-risk tool gets an 'any' matcher (always-allow the whole tool); a
// high-risk tool (Bash/Edit/Write/WebFetch) refuses 'any', so it is pinned to the
// exact command/path/url the request carries. Returns nil when that field is
// absent, so the caller degrades the tap to allow-once instead of erroring.
func imAlwaysMatcher(req store.AgentPermissionRequest) *Matcher {
	if !IsHighRiskTool(req.ToolName) {
		return &Matcher{Kind: "any"}
	}
	input := map[string]any{}
	_ = json.Unmarshal([]byte(req.ToolInput), &input)
	field := strings.TrimSpace(extractMatcherField(req.ToolName, input))
	if field == "" {
		return nil
	}
	return &Matcher{Kind: "exact", Value: field}
}

// handleAskUserCallback answers an agent's ask_user question from an option-button
// tap. CallbackData is "askuser:<reqID>:<optIdx>". Authz mirrors the permission
// callback: the request's workspace must resolve to THIS chat's routed project, so
// a tap in one project's chat can never answer another project's question.
func (s *IMBotService) handleAskUserCallback(ctx context.Context, channel store.ImBotChannel, chat store.ImBotChat, ev imbot.InboundEvent) {
	parts := strings.Split(ev.CallbackData, ":")
	if len(parts) != 3 || parts[0] != "askuser" {
		return
	}
	reqID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return
	}
	optIdx, err := strconv.Atoi(parts[2])
	if err != nil || optIdx < 0 {
		return
	}
	if s.askUser == nil {
		return
	}
	req, err := s.q.GetAskUserRequest(ctx, reqID)
	if err != nil {
		return
	}
	// Authz: the request must resolve to THIS chat's routed project.
	if !chat.ProjectID.Valid {
		return
	}
	pctx, err := s.q.GetProjectContextByWorkspace(ctx, req.WorkspaceID)
	if err != nil || pctx.ProjectID != chat.ProjectID.Int64 {
		return
	}
	question, label, ok := askUserOption(req.QuestionsJson, optIdx)
	if !ok {
		return
	}
	d := AskUserDecision{Answers: []AskUserAnswer{{Question: question, Labels: []string{label}}}}
	if derr := s.askUser.Decide(ctx, reqID, d); derr != nil {
		if errors.Is(derr, ErrAskUserAlreadyDecided) {
			s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, "这个问题刚刚已经回答过了。")
			return
		}
		slog.Warn("imbot: ask-user decide failed", "request", reqID, "error", derr)
		return
	}
	s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, "✅ 已回复："+label)
}

// askUserOption parses the stored questions JSON of an ask_user request and
// returns the (question text, chosen option label) for the single-question card
// the IM renders. ok is false when the JSON is unusable, carries more than one
// question, or the index is out of range.
func askUserOption(questionsJSON string, optIdx int) (question, label string, ok bool) {
	var qs []struct {
		Question string `json:"question"`
		Options  []struct {
			Label string `json:"label"`
		} `json:"options"`
	}
	if err := json.Unmarshal([]byte(questionsJSON), &qs); err != nil || len(qs) != 1 {
		return "", "", false
	}
	if optIdx >= len(qs[0].Options) {
		return "", "", false
	}
	return qs[0].Question, qs[0].Options[optIdx].Label, true
}

// handleStop stops the agent currently running the chat's active task (the /stop
// command) WITHOUT deleting the task or its workspace — the user can resume by
// sending another message. It resolves the same target as a normal message
// (thread > pinned > active pointer); a no-op nudge when nothing is running.
func (s *IMBotService) handleStop(ctx context.Context, channel store.ImBotChannel, chat store.ImBotChat, ev imbot.InboundEvent) {
	_, wsID := s.resolveTask(ctx, chat, ev.ThreadExtID)
	if wsID == 0 {
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
			"当前没有正在进行的任务。用 /issues 查看任务，或直接发一句话开始新任务。")
		return
	}
	if s.stopper == nil {
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, "牛牛暂时无法停止该任务，请稍后再试。")
		return
	}
	s.stopper.RemoveSession(ctx, wsID)
	s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, "⏹️ 已停止当前任务的执行。再发一句话即可继续。")
}

// handleDetail answers /detail [#id|序号] with a compact card for one issue:
// `#id 标题`, its column, workspace status, goal and description. With no argument
// it describes the chat's active task. Read-only.
func (s *IMBotService) handleDetail(ctx context.Context, channel store.ImBotChannel, chat store.ImBotChat, ev imbot.InboundEvent, rest string) {
	projectID := int64(0)
	if chat.ProjectID.Valid {
		projectID = chat.ProjectID.Int64
	}
	issueID := s.resolveDetailTarget(ctx, chat, projectID, strings.TrimSpace(rest))
	if issueID == 0 {
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
			"请提供要查看的任务编号，例如 /detail #123；或先用 #编号 选中一个任务。用 /issues 查看全部任务。")
		return
	}
	issue, ok := s.issueInProject(ctx, projectID, issueID)
	if !ok {
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, "没有找到该任务。用 /issues 查看全部任务及编号。")
		return
	}
	s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, s.formatIssueDetail(ctx, issue))
}

// resolveDetailTarget maps the /detail argument to an issue id: a `#id`, a bare
// index into the /issues workspace-backed listing, or (empty arg) the chat's
// active task. 0 = unresolved.
func (s *IMBotService) resolveDetailTarget(ctx context.Context, chat store.ImBotChat, projectID int64, arg string) int64 {
	if arg == "" {
		if chat.ActiveIssueID.Valid {
			return chat.ActiveIssueID.Int64
		}
		return 0
	}
	if strings.HasPrefix(arg, "#") {
		if id, err := strconv.ParseInt(strings.TrimSpace(arg[1:]), 10, 64); err == nil {
			return id
		}
		return 0
	}
	if n, err := strconv.Atoi(arg); err == nil && n >= 1 {
		tasks := s.chatTasks(ctx, projectID)
		if n <= len(tasks) {
			return tasks[n-1].issueID
		}
	}
	return 0
}

// formatIssueDetail renders the /detail card: `#id 标题`, column, workspace status,
// and (clipped) goal + description.
func (s *IMBotService) formatIssueDetail(ctx context.Context, issue store.Issue) string {
	colName := ""
	if col, err := s.q.GetColumn(ctx, issue.ColumnID); err == nil {
		colName = col.Name
	}
	status := "⚪ 未启动工作空间"
	if s.workspaceOfIssue(ctx, issue.ID) != 0 {
		status = "🟢 已启动"
	}
	// CommonMark (see replyIssues): a bold title block, then one "- " list item per
	// field so they render one-per-line on every adapter.
	var b strings.Builder
	b.WriteString("**" + oneLine(formatTaskRef(issue.ID, issue.Title)) + "**\n\n")
	b.WriteString("- 所在列：" + colName + "\n")
	b.WriteString("- 工作空间：" + status + "\n")
	if goal := clipDetail(issue.GoalCondition, 200); goal != "" {
		b.WriteString("- 目标：" + goal + "\n")
	}
	if desc := clipDetail(issue.Description.String, 300); desc != "" {
		b.WriteString("- 描述：" + desc + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// oneLine collapses all runs of whitespace (including newlines) in s to single
// spaces, so a value stays on one markdown line — a stray newline would otherwise
// break the enclosing "- " list item / bold span.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clipDetail flattens s to one line (oneLine) and clips it to at most max runes,
// adding an ellipsis when clipped. Empty in, empty out.
func clipDetail(s string, max int) string {
	s = oneLine(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// startPairing seeds a pending chat on first contact from an unknown chat so a
// project admin can approve it in project settings (design §3.5 pairing).
func (s *IMBotService) startPairing(ctx context.Context, channel store.ImBotChannel, ev imbot.InboundEvent) {
	if _, err := s.q.CreateIMBotChat(ctx, store.CreateIMBotChatParams{
		ChannelID: channel.ID,
		ChatExtID: ev.ChatExtID,
		ChatName:  "",
		Status:    "pending",
	}); err != nil {
		// A concurrent inbound may have seeded it already (UNIQUE); ignore.
		slog.Debug("imbot: pairing seed", "channel", channel.ID, "chat", ev.ChatExtID, "error", err)
	}
	s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
		"👋 你好，我是牛牛。请让项目管理员在项目设置里批准此聊天，之后就能在这里发起任务了。")
}

// replyStatus answers /status with a de-jargonized count of in-progress tasks.
func (s *IMBotService) replyStatus(ctx context.Context, channel store.ImBotChannel, chat store.ImBotChat, ev imbot.InboundEvent) {
	n := 0
	if chat.ProjectID.Valid {
		if issues, err := s.q.ListIssuesByProject(ctx, chat.ProjectID.Int64); err == nil {
			n = len(issues)
		}
	}
	s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
		"当前有 "+strconv.Itoa(n)+" 个进行中的任务。直接发一句话即可开始新任务，或用 /new 明确新建。")
}

// chatTask is one switchable task in a chat: an issue that has a backing
// workspace, so /use can point at it and follow-ups can be delivered.
type chatTask struct {
	issueID int64
	wsID    int64
	title   string
}

// chatTasks lists the project's workspace-backed issues in a stable order (issue
// listing order), which is the numbering shared by /issues and /use <n>.
func (s *IMBotService) chatTasks(ctx context.Context, projectID int64) []chatTask {
	// Single JOIN (issue + its primary workspace) instead of an N+1 per-issue
	// workspace lookup; same order as ListIssuesByProject so /use index numbering
	// is unchanged.
	rows, err := s.q.ListProjectPlansWithWorkspace(ctx, projectID)
	if err != nil {
		return nil
	}
	tasks := make([]chatTask, 0, len(rows))
	for _, r := range rows {
		tasks = append(tasks, chatTask{issueID: r.ID, wsID: r.WorkspaceID, title: r.Title})
	}
	return tasks
}

// replyIssues answers /issues with the project's FULL issue list — every issue's
// `#<id>`, title, its kanban column (grouped) and whether it already has a
// workspace (公共空间) — so a pure-DM channel can see all work items, not only the
// workspace-backed ones. `#<id>` is the unified control: it switches to a started
// task, or starts a workspace and begins work for an issue that has none.
func (s *IMBotService) replyIssues(ctx context.Context, channel store.ImBotChannel, chat store.ImBotChat, ev imbot.InboundEvent) {
	projectID := int64(0)
	if chat.ProjectID.Valid {
		projectID = chat.ProjectID.Int64
	}
	issues, err := s.q.ListIssuesByProject(ctx, projectID)
	if err != nil || len(issues) == 0 {
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
			"当前项目还没有任务。直接发一句话即可开始一个新任务。")
		return
	}
	// Column names (id -> name) for the "所在列" grouping, and the set of issues that
	// already have a workspace (公共空间) for the status flag.
	colName := map[int64]string{}
	if cols, cerr := s.q.ListColumnsByProject(ctx, projectID); cerr == nil {
		for _, c := range cols {
			colName[c.ID] = c.Name
		}
	}
	hasWS := map[int64]bool{}
	for _, t := range s.chatTasks(ctx, projectID) {
		hasWS[t.issueID] = true
	}

	// Rendered as CommonMark: every adapter renders markdown (Lark card / DingTalk
	// sampleMarkdown / Telegram HTML), where a single "\n" is a soft break that
	// collapses lines together. So section titles are bold blocks separated by a
	// blank line, and each issue is a "- " list item — that is what makes the list
	// render one-per-line instead of as a run-on paragraph.
	var b strings.Builder
	b.WriteString("**当前项目的任务（共 " + strconv.Itoa(len(issues)) + " 个）**\n")
	// ListIssuesByProject is ordered by column position then issue position, so
	// issues of the same column are contiguous — print a header on each change.
	lastCol := int64(-1)
	for _, iss := range issues {
		if iss.ColumnID != lastCol {
			lastCol = iss.ColumnID
			name := colName[iss.ColumnID]
			if name == "" {
				name = "未分组"
			}
			b.WriteString("\n**" + name + "**\n\n")
		}
		status := "⚪ 未启动工作空间"
		if hasWS[iss.ID] {
			status = "🟢 已启动"
		}
		active := ""
		if chat.ActiveIssueID.Valid && chat.ActiveIssueID.Int64 == iss.ID {
			active = "（当前）"
		}
		b.WriteString("- #" + strconv.FormatInt(iss.ID, 10) + " " + oneLine(iss.Title) + " — " + status + active + "\n")
	}
	b.WriteString("\n发送 `#编号` 切换到已启动的任务，或为未启动的任务启动工作空间并开始处理（如 `#" +
		strconv.FormatInt(issues[0].ID, 10) + "`）；直接发一句话可新建任务，`/delete 编号` 可删除任务及其工作空间。")
	s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, b.String())
}

// handleUse switches the chat's active task to the /use <n> selection (n is the
// 1-based index from /issues). It sets the active pointer so the next bare
// message continues that task — the no-thread channel's way to keep parallel
// tasks apart without crosstalk.
func (s *IMBotService) handleUse(ctx context.Context, channel store.ImBotChannel, chat store.ImBotChat, ev imbot.InboundEvent, rest string) {
	arg := strings.TrimSpace(rest)
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 {
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
			"请提供要切换的任务编号，例如 /use 1。用 /issues 查看可选任务。")
		return
	}
	projectID := int64(0)
	if chat.ProjectID.Valid {
		projectID = chat.ProjectID.Int64
	}
	tasks := s.chatTasks(ctx, projectID)
	if n > len(tasks) {
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
			"没有编号 "+arg+" 对应的任务。用 /issues 查看可选任务。")
		return
	}
	t := tasks[n-1]
	s.setActiveIssue(ctx, chat, t.issueID)
	s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
		"已切换到任务「"+t.title+"」。接下来的消息都会发给它，用 /new 可另起新任务。")
}

// handleDelete deletes the task selected by /delete <ref> — its issue AND its
// backing workspace — then cleans up references so no later message resolves to
// the gone task. <ref> is either a 1-based index from the /issues listing (like
// /use) or a `#<id>` conversation id. Deletion is destructive and irreversible;
// the explicit command is its own confirmation.
func (s *IMBotService) handleDelete(ctx context.Context, channel store.ImBotChannel, chat store.ImBotChat, ev imbot.InboundEvent, rest string) {
	arg := strings.TrimSpace(rest)
	if arg == "" {
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
			"请提供要删除的任务编号，例如 /delete 1 或 /delete #123。用 /issues 查看可删除的任务。")
		return
	}
	projectID := int64(0)
	if chat.ProjectID.Valid {
		projectID = chat.ProjectID.Int64
	}
	t, ok := s.resolveDeleteTarget(ctx, projectID, arg)
	if !ok {
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
			"没有找到「"+arg+"」对应的任务。用 /issues 查看可删除的任务及编号。")
		return
	}
	if s.deleter == nil {
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, "牛牛暂时无法删除任务，请稍后再试。")
		return
	}

	// Stop the workspace's running agent (best-effort) before its directory is
	// removed, mirroring the WebUI delete's proxy.RemoveSession pre-step.
	var stop func(context.Context, int64)
	if s.stopper != nil {
		stop = func(c context.Context, wsID int64) { s.stopper.RemoveSession(c, wsID) }
	}
	if err := s.deleter.DeleteTask(ctx, projectID, t.issueID, stop); err != nil {
		slog.Warn("imbot: delete task failed", "project", projectID, "issue", t.issueID, "error", err)
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, "删除失败，请稍后再试。")
		return
	}

	s.clearDeletedTaskRefs(ctx, chat, t.issueID)
	s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID,
		"🗑️ 已删除会话 #"+strconv.FormatInt(t.issueID, 10)+" - "+t.title+" 及其工作空间。")
}

// resolveDeleteTarget maps a /delete argument to a switchable task. A leading `#`
// marks an explicit conversation id (`#123`); a bare number is a 1-based index
// into the /issues listing (same numbering as /use). Only workspace-backed tasks
// of the project resolve.
func (s *IMBotService) resolveDeleteTarget(ctx context.Context, projectID int64, arg string) (chatTask, bool) {
	if strings.HasPrefix(arg, "#") {
		id, err := strconv.ParseInt(strings.TrimSpace(arg[1:]), 10, 64)
		if err != nil {
			return chatTask{}, false
		}
		return s.findChatTask(ctx, projectID, id)
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 {
		return chatTask{}, false
	}
	tasks := s.chatTasks(ctx, projectID)
	if n > len(tasks) {
		return chatTask{}, false
	}
	return tasks[n-1], true
}

// clearDeletedTaskRefs removes lingering references to a just-deleted task: this
// chat's active/pinned pointer when it named the task, and every im_bot_thread
// bound to the issue (across chats) so a later in-thread message won't resolve to
// the gone workspace and silently drop.
func (s *IMBotService) clearDeletedTaskRefs(ctx context.Context, chat store.ImBotChat, issueID int64) {
	if err := s.q.DeleteIMBotThreadsByIssue(ctx, issueID); err != nil {
		slog.Warn("imbot: clear threads for deleted issue failed", "issue", issueID, "error", err)
	}
	pinned := chat.PinnedIssueID
	if pinned.Valid && pinned.Int64 == issueID {
		pinned = sql.NullInt64{}
	}
	active := chat.ActiveIssueID
	if active.Valid && active.Int64 == issueID {
		active = sql.NullInt64{}
	}
	if pinned == chat.PinnedIssueID && active == chat.ActiveIssueID {
		return // nothing pointed at the deleted task
	}
	if _, err := s.q.UpdateIMBotChat(ctx, store.UpdateIMBotChatParams{
		BindMode:      chat.BindMode,
		PinnedIssueID: pinned,
		ActiveIssueID: active,
		Status:        chat.Status,
		ID:            chat.ID,
	}); err != nil {
		slog.Warn("imbot: clear chat pointers for deleted issue failed", "chat", chat.ID, "issue", issueID, "error", err)
	}
}

// projectOwner resolves the (owner_type, owner_id) of the channel's bound project
// — the isolation boundary for created issues/workspaces.
func (s *IMBotService) projectOwner(ctx context.Context, projectID int64) (OwnerRef, bool) {
	p, err := s.q.GetProject(ctx, projectID)
	if err != nil {
		return OwnerRef{}, false
	}
	return OwnerRef{Type: p.OwnerType, ID: p.OwnerID}, true
}

// pushText is a best-effort outbound reply over the channel's adapter, honoring
// the originating thread so replies stay in-thread.
func (s *IMBotService) pushText(ctx context.Context, channel store.ImBotChannel, chatExtID, threadExtID, text string) {
	adapter, ok := s.adapters[imbot.ChannelType(channel.ChannelType)]
	if !ok {
		return
	}
	cred, err := s.decryptCred(channel.ChannelType, channel.CredentialEnc)
	if err != nil {
		return
	}
	if err := adapter.Push(ctx, cred, imbot.OutboundMessage{ChatExtID: chatExtID, ThreadExtID: threadExtID, Text: text}); err != nil {
		slog.Warn("imbot: reply push failed", "channel", channel.ID, "error", err)
	}
}

// truncInbound returns the first 60 runes of s (with an ellipsis if clipped) so
// an inbound message can be logged for diagnostics without dumping a huge reply.
func truncInbound(s string) string {
	r := []rune(s)
	if len(r) <= 60 {
		return s
	}
	return string(r[:60]) + "…"
}

// parseHashSwitch recognizes a leading `#<id>` conversation-switch token.
// isHash is true whenever the text starts with '#'. idTok is the leading run of
// digits when it is a well-formed id (terminated by whitespace or end of string);
// otherwise idTok is "" and the whole remainder is treated as new-conversation
// text. body is the message after the id token, trimmed.
//
//	"#42"            -> ("42", "",        true)
//	"#42 继续做"      -> ("42", "继续做",  true)
//	"#新任务"         -> ("",   "新任务",  true)   // no id -> new conversation
//	"#"              -> ("",   "",        true)
//	"hello"          -> ("",   "hello",   false)
func parseHashSwitch(text string) (idTok, body string, isHash bool) {
	if !strings.HasPrefix(text, "#") {
		return "", text, false
	}
	rest := text[1:]
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	// A digit run only counts as an id when it ends the token (whitespace or EOS);
	// "#12ab" is not an id — the user meant a new conversation titled "12ab".
	if i > 0 && (i == len(rest) || rest[i] == ' ' || rest[i] == '\t' || rest[i] == '\n' || rest[i] == '\r') {
		return rest[:i], strings.TrimSpace(rest[i:]), true
	}
	return "", strings.TrimSpace(rest), true
}

// parseSlashCommand splits a leading "/cmd rest" into ("cmd", "rest"). Non-slash
// input returns ("", full text). Recognized commands are matched by the caller.
// A Telegram-style "@botname" suffix on the command (e.g. "/new@MyBot" in a
// group) is stripped so the command still matches.
func parseSlashCommand(text string) (cmd, rest string) {
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	body := strings.TrimPrefix(text, "/")
	if i := strings.IndexAny(body, " \t\r\n"); i >= 0 {
		cmd, rest = body[:i], body[i+1:]
	} else {
		cmd = body
	}
	if at := strings.IndexByte(cmd, '@'); at >= 0 {
		cmd = cmd[:at]
	}
	return strings.ToLower(cmd), rest
}
