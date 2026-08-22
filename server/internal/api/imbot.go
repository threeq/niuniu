// IM Bot management REST endpoints (Epic #555). All routes live under
// /api/projects/:id/imbot/... and are project-scoped + authz-gated: List/read
// requires CanAccessProject; every mutation additionally requires
// EnsureOwnerWritable (the project is the natural ownership boundary, so no
// separate owner filter is needed). Credentials are write-only over the API —
// they go in on create/update and are never echoed back in a DTO.
package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// IMBotHandler binds the project-scoped IM channel + chat endpoints.
type IMBotHandler struct {
	svc       *service.IMBotService
	authz     *service.Authz
	db        *sql.DB                  // for ParseOwnerFilter (org-by-slug resolution) on owner-level routes
	dispatch  service.TaskRouter       // for StartOnboarding (Endpoint A)
	deliverer service.MessageDeliverer // for StartOnboarding (Endpoint A)
}

// NewIMBotHandler wires service + authz into the handler.
func NewIMBotHandler(svc *service.IMBotService, authz *service.Authz, db *sql.DB) *IMBotHandler {
	return &IMBotHandler{svc: svc, authz: authz, db: db}
}

// SetDispatch wires the task router for the AI-onboarding start endpoint.
// Called from server.go after the DispatchService is constructed.
func (h *IMBotHandler) SetDispatch(d service.TaskRouter) { h.dispatch = d }

// SetDeliverer wires the message deliverer for the AI-onboarding start endpoint.
// Called from server.go after the AgentProxy is constructed.
func (h *IMBotHandler) SetDeliverer(d service.MessageDeliverer) { h.deliverer = d }

func imbotIDParam(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad " + name})
		return 0, false
	}
	return id, true
}

func (h *IMBotHandler) callerUserID(c *gin.Context) (int64, bool) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return 0, false
	}
	return uid, true
}

// readAccess resolves project id + caller and enforces read access.
func (h *IMBotHandler) readAccess(c *gin.Context) (projectID, uid int64, ok bool) {
	projectID, ok = imbotIDParam(c, "id")
	if !ok {
		return
	}
	uid, ok = h.callerUserID(c)
	if !ok {
		return
	}
	if _, err := h.authz.CanAccessProject(c.Request.Context(), uid, projectID); err != nil {
		writeAuthzError(c, err)
		ok = false
	}
	return
}

// writeAccessWithOwner resolves project id + caller, enforces write access, and
// returns the OwnerRef so callers that need it (e.g. StartOnboarding) can use it.
func (h *IMBotHandler) writeAccessWithOwner(c *gin.Context) (projectID, uid int64, owner service.OwnerRef, ok bool) {
	projectID, ok = imbotIDParam(c, "id")
	if !ok {
		return
	}
	uid, ok = h.callerUserID(c)
	if !ok {
		return
	}
	owner, err := h.authz.CanAccessProject(c.Request.Context(), uid, projectID)
	if err != nil {
		writeAuthzError(c, err)
		ok = false
		return
	}
	if err := h.authz.EnsureOwnerWritable(c.Request.Context(), uid, owner); err != nil {
		writeAuthzError(c, err)
		ok = false
	}
	return
}

// writeAccess additionally enforces write access on the project owner.
func (h *IMBotHandler) writeAccess(c *gin.Context) (projectID, uid int64, ok bool) {
	projectID, uid, _, ok = h.writeAccessWithOwner(c)
	return
}

func (h *IMBotHandler) mapErr(c *gin.Context, err error) {
	if errors.Is(err, service.ErrIMBotNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if errors.Is(err, service.ErrInvalidChannelConfig) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if errors.Is(err, service.ErrDuplicateIMBotCredential) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if errors.Is(err, service.ErrForbidden) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

// ownerWriteAccess resolves the owner from the `?owner=` query (default: the
// caller's personal user owner) and enforces the caller may write to it. It is
// the gate for the owner-level bot CRUD + pending-chat routes (design §9).
func (h *IMBotHandler) ownerWriteAccess(c *gin.Context) (owner service.OwnerRef, uid int64, ok bool) {
	owner, uid, ok = h.ownerReadAccess(c)
	if !ok {
		return
	}
	if err := h.authz.EnsureOwnerWritable(c.Request.Context(), uid, owner); err != nil {
		writeAuthzError(c, err)
		return service.OwnerRef{}, 0, false
	}
	return owner, uid, true
}

// ownerReadAccess resolves the owner (default personal) and enforces read
// access. Owner comes from `?owner=user:<id>|org:<slug|id>`; absent = personal.
func (h *IMBotHandler) ownerReadAccess(c *gin.Context) (owner service.OwnerRef, uid int64, ok bool) {
	uid, ok = h.callerUserID(c)
	if !ok {
		return
	}
	f, err := ParseOwnerFilter(c, h.db)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return service.OwnerRef{}, 0, false
	}
	if f.Type == "" {
		owner = service.OwnerRef{Type: "user", ID: uid}
	} else {
		owner = service.OwnerRef{Type: f.Type, ID: f.ID}
	}
	if err := h.authz.CanAccessOwner(c.Request.Context(), uid, owner); err != nil {
		writeAuthzError(c, err)
		return service.OwnerRef{}, 0, false
	}
	return owner, uid, true
}

// --- channels ---

func (h *IMBotHandler) ListChannels(c *gin.Context) {
	pid, _, ok := h.readAccess(c)
	if !ok {
		return
	}
	items, err := h.svc.ListChannels(c.Request.Context(), pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type createChannelBody struct {
	ChannelType    string         `json:"channel_type" binding:"required"`
	Name           string         `json:"name" binding:"required"`
	ConnectionMode string         `json:"connection_mode"`
	WebhookSecret  string         `json:"webhook_secret"`
	Credential     map[string]any `json:"credential"`
}

func (h *IMBotHandler) CreateChannel(c *gin.Context) {
	_, _, owner, ok := h.writeAccessWithOwner(c)
	if !ok {
		return
	}
	var b createChannelBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	dto, err := h.svc.CreateChannel(c.Request.Context(), owner, service.CreateChannelInput{
		ChannelType:    b.ChannelType,
		Name:           b.Name,
		ConnectionMode: b.ConnectionMode,
		WebhookSecret:  b.WebhookSecret,
		Credential:     b.Credential,
	})
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, dto)
}

type updateChannelBody struct {
	Name           string         `json:"name"`
	ConnectionMode string         `json:"connection_mode"`
	WebhookSecret  string         `json:"webhook_secret"`
	Status         string         `json:"status"`
	Credential     map[string]any `json:"credential"`
}

func (h *IMBotHandler) UpdateChannel(c *gin.Context) {
	_, _, owner, ok := h.writeAccessWithOwner(c)
	if !ok {
		return
	}
	cid, ok := imbotIDParam(c, "cid")
	if !ok {
		return
	}
	var b updateChannelBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	dto, err := h.svc.UpdateChannel(c.Request.Context(), owner, cid, service.UpdateChannelInput{
		Name:           b.Name,
		ConnectionMode: b.ConnectionMode,
		WebhookSecret:  b.WebhookSecret,
		Status:         b.Status,
		Credential:     b.Credential,
	})
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, dto)
}

func (h *IMBotHandler) DeleteChannel(c *gin.Context) {
	_, _, owner, ok := h.writeAccessWithOwner(c)
	if !ok {
		return
	}
	cid, ok := imbotIDParam(c, "cid")
	if !ok {
		return
	}
	if err := h.svc.DeleteChannel(c.Request.Context(), owner, cid); err != nil {
		h.mapErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *IMBotHandler) TestChannel(c *gin.Context) {
	_, _, owner, ok := h.writeAccessWithOwner(c)
	if !ok {
		return
	}
	cid, ok := imbotIDParam(c, "cid")
	if !ok {
		return
	}
	if err := h.svc.TestChannel(c.Request.Context(), owner, cid); err != nil {
		if errors.Is(err, service.ErrIMBotNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type addChatBody struct {
	ChatExtID string `json:"chat_ext_id" binding:"required"`
	ChatName  string `json:"chat_name"`
}

// AddChat seeds a pending chat under a channel (W1 pairing entry; W2 replaces
// this with connector-driven auto-pending on first inbound message).
func (h *IMBotHandler) AddChat(c *gin.Context) {
	_, _, owner, ok := h.writeAccessWithOwner(c)
	if !ok {
		return
	}
	cid, ok := imbotIDParam(c, "cid")
	if !ok {
		return
	}
	var b addChatBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	dto, err := h.svc.AddChat(c.Request.Context(), owner, cid, b.ChatExtID, b.ChatName)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, dto)
}

// --- chats ---

func (h *IMBotHandler) ListChats(c *gin.Context) {
	pid, _, ok := h.readAccess(c)
	if !ok {
		return
	}
	items, err := h.svc.ListChats(c.Request.Context(), pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *IMBotHandler) ApproveChat(c *gin.Context) {
	pid, uid, ok := h.writeAccess(c)
	if !ok {
		return
	}
	chatID, ok := imbotIDParam(c, "chatid")
	if !ok {
		return
	}
	dto, err := h.svc.ApproveChat(c.Request.Context(), pid, chatID, uid)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, dto)
}

type patchChatBody struct {
	BindMode      string `json:"bind_mode"`
	PinnedIssueID *int64 `json:"pinned_issue_id"`
	ActiveIssueID *int64 `json:"active_issue_id"`
	Status        string `json:"status"`
}

func (h *IMBotHandler) PatchChat(c *gin.Context) {
	pid, _, ok := h.writeAccess(c)
	if !ok {
		return
	}
	chatID, ok := imbotIDParam(c, "chatid")
	if !ok {
		return
	}
	var b patchChatBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	dto, err := h.svc.PatchChat(c.Request.Context(), pid, chatID, service.PatchChatInput{
		BindMode:      b.BindMode,
		PinnedIssueID: b.PinnedIssueID,
		ActiveIssueID: b.ActiveIssueID,
		Status:        b.Status,
	})
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, dto)
}

func (h *IMBotHandler) DeleteChat(c *gin.Context) {
	pid, _, ok := h.writeAccess(c)
	if !ok {
		return
	}
	chatID, ok := imbotIDParam(c, "chatid")
	if !ok {
		return
	}
	if err := h.svc.DeleteChat(c.Request.Context(), pid, chatID); err != nil {
		h.mapErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ─── Owner-level bot + pending-chat endpoints (shared-bot / multi-project) ────
// These mount under /api/imbot/bots... and /api/imbot/pending-chats and are
// authorized by owner (?owner=, default personal). A bot is owner-level and
// belongs to no project; chats are routed to a project at approval time.

// ListBots handles GET /api/imbot/bots — the owner's bots.
func (h *IMBotHandler) ListBots(c *gin.Context) {
	owner, _, ok := h.ownerReadAccess(c)
	if !ok {
		return
	}
	items, err := h.svc.ListBotsByOwner(c.Request.Context(), owner)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type createBotBody struct {
	ProjectID      int64          `json:"project_id" binding:"required"`
	ChannelType    string         `json:"channel_type" binding:"required"`
	Name           string         `json:"name" binding:"required"`
	ConnectionMode string         `json:"connection_mode"`
	WebhookSecret  string         `json:"webhook_secret"`
	Credential     map[string]any `json:"credential"`
}

// CreateBot handles POST /api/imbot/bots. The bot is created owner-level under
// the resolved ?owner= (default personal); the caller must be able to write to
// that owner. project_id in the body is the project the caller ran the wizard in
// (used only to validate the caller/owner has that project); the bot itself is
// not bound to it.
func (h *IMBotHandler) CreateBot(c *gin.Context) {
	owner, uid, ok := h.ownerWriteAccess(c)
	if !ok {
		return
	}
	var b createBotBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// The referenced project must belong to the resolved owner (so the request is
	// owner-consistent). CanAccessProject returns the project's owner.
	projOwner, err := h.authz.CanAccessProject(c.Request.Context(), uid, b.ProjectID)
	if err != nil {
		writeAuthzError(c, err)
		return
	}
	if projOwner.Type != owner.Type || projOwner.ID != owner.ID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project owner mismatch"})
		return
	}
	dto, err := h.svc.CreateChannel(c.Request.Context(), owner, service.CreateChannelInput{
		ChannelType:    b.ChannelType,
		Name:           b.Name,
		ConnectionMode: b.ConnectionMode,
		WebhookSecret:  b.WebhookSecret,
		Credential:     b.Credential,
	})
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, dto)
}

// botWriteAccess resolves a bot id (:cid), enforces the caller may write to the
// bot's owner, and returns that owner for the owner-keyed service calls. Hides
// existence as 404 on cross-owner access.
func (h *IMBotHandler) botWriteAccess(c *gin.Context) (owner service.OwnerRef, uid int64, ok bool) {
	cid, ok := imbotIDParam(c, "cid")
	if !ok {
		return service.OwnerRef{}, 0, false
	}
	uid, ok = h.callerUserID(c)
	if !ok {
		return service.OwnerRef{}, 0, false
	}
	owner, err := h.svc.BotOwner(c.Request.Context(), cid)
	if err != nil {
		h.mapErr(c, err)
		return service.OwnerRef{}, 0, false
	}
	if err := h.authz.EnsureOwnerWritable(c.Request.Context(), uid, owner); err != nil {
		writeAuthzError(c, err)
		return service.OwnerRef{}, 0, false
	}
	return owner, uid, true
}

// UpdateBot handles PUT /api/imbot/bots/:cid.
func (h *IMBotHandler) UpdateBot(c *gin.Context) {
	owner, _, ok := h.botWriteAccess(c)
	if !ok {
		return
	}
	cid, _ := imbotIDParam(c, "cid")
	var b updateChannelBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	dto, err := h.svc.UpdateChannel(c.Request.Context(), owner, cid, service.UpdateChannelInput{
		Name:           b.Name,
		ConnectionMode: b.ConnectionMode,
		WebhookSecret:  b.WebhookSecret,
		Status:         b.Status,
		Credential:     b.Credential,
	})
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, dto)
}

// DeleteBot handles DELETE /api/imbot/bots/:cid.
func (h *IMBotHandler) DeleteBot(c *gin.Context) {
	owner, _, ok := h.botWriteAccess(c)
	if !ok {
		return
	}
	cid, _ := imbotIDParam(c, "cid")
	if err := h.svc.DeleteChannel(c.Request.Context(), owner, cid); err != nil {
		h.mapErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// TestBot handles POST /api/imbot/bots/:cid/test.
func (h *IMBotHandler) TestBot(c *gin.Context) {
	owner, _, ok := h.botWriteAccess(c)
	if !ok {
		return
	}
	cid, _ := imbotIDParam(c, "cid")
	if err := h.svc.TestChannel(c.Request.Context(), owner, cid); err != nil {
		if errors.Is(err, service.ErrIMBotNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListPendingChatsOwner handles GET /api/imbot/pending-chats.
func (h *IMBotHandler) ListPendingChatsOwner(c *gin.Context) {
	owner, _, ok := h.ownerReadAccess(c)
	if !ok {
		return
	}
	items, err := h.svc.ListPendingChatsByOwner(c.Request.Context(), owner)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// ListActiveChatsOwner handles GET /api/imbot/chats: the owner's active
// chat->project bindings (which group routes to which project).
func (h *IMBotHandler) ListActiveChatsOwner(c *gin.Context) {
	owner, _, ok := h.ownerReadAccess(c)
	if !ok {
		return
	}
	items, err := h.svc.ListActiveChatsByOwner(c.Request.Context(), owner)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// DeleteChatOwner handles DELETE /api/imbot/chats/:chatid: remove a chat->project
// binding (the group becomes unpaired). Authorized by owner (the chat's bot must
// belong to the resolved owner).
func (h *IMBotHandler) DeleteChatOwner(c *gin.Context) {
	owner, _, ok := h.ownerWriteAccess(c)
	if !ok {
		return
	}
	chatID, ok := imbotIDParam(c, "chatid")
	if !ok {
		return
	}
	if err := h.svc.DeleteChatByOwner(c.Request.Context(), chatID, owner); err != nil {
		h.mapErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type approveChatBody struct {
	ProjectID int64 `json:"project_id" binding:"required"`
}

// ApproveChatOwner handles POST /api/imbot/chats/:chatid/approve with a
// project_id body: approve the pending chat and route it to that project.
func (h *IMBotHandler) ApproveChatOwner(c *gin.Context) {
	uid, ok := h.callerUserID(c)
	if !ok {
		return
	}
	chatID, ok := imbotIDParam(c, "chatid")
	if !ok {
		return
	}
	var b approveChatBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	dto, err := h.svc.ApproveChatToProject(c.Request.Context(), chatID, b.ProjectID, uid)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, dto)
}

// ReassignChatOwner handles POST /api/imbot/chats/:chatid/reassign with a
// project_id body: move an already-paired chat to a new project.
func (h *IMBotHandler) ReassignChatOwner(c *gin.Context) {
	uid, ok := h.callerUserID(c)
	if !ok {
		return
	}
	chatID, ok := imbotIDParam(c, "chatid")
	if !ok {
		return
	}
	var b approveChatBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	dto, err := h.svc.ReassignChat(c.Request.Context(), chatID, b.ProjectID, uid)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, dto)
}

// ─── AI-onboarding endpoints (Epic #555 T3) ──────────────────────────────────

// onboardingKickoffPrompt is the seed text delivered to the agent that guides
// the user through IM-bot channel setup. It is intentionally self-contained:
// the onboarding workspace does not yet have the T6 skill materialized, so the
// prompt carries the essential platform steps + tool playbook inline so the
// agent is fully self-sufficient from this text alone.
const onboardingKickoffPrompt = `你是本项目的 IM 机器人接入向导。目标：把用户从零带到「渠道建好、连通、配对、双向闭环」。

**安全铁律：绝不在对话里索取或接收 App Secret / Bot Token 等明文密钥。** 凭据一律通过安全录入链接提交——链接在独立页面收取，不进入对话。

## 可用 MCP 工具（5 个）

| 工具 | 何时调用 |
|---|---|
| imbot_request_credential_link(platform, name, connection_mode) | 用户确认平台并准备好凭据后调用，返回安全录入链接 |
| imbot_test_channel(channel_id) | 渠道创建后立即调用验证连通性 |
| imbot_list_pending_chats(channel_id?) | 用户把机器人拉入群/私聊并发消息后查待配对列表 |
| imbot_approve_chat(chat_id) | 对待配对聊天调用，使其转为 active |
| imbot_channel_status(channel_id) | 查询渠道状态，用于收尾确认 |

## 飞书/Lark 人工步骤

1. 打开 open.feishu.cn，登录企业账号。
2. 开发者后台 → 创建企业自建应用，填写名称。
3. 凭证与基础信息 → 记录 App ID 和 App Secret。
4. 权限管理 → 勾选 im:message:send_as_bot、im:message（按需加 im:chat.member:read）。
5. 事件与回调 → 事件配置 → 选择「使用长连接接收事件（stream 模式，局域网无公网即可）」，订阅 im.message.receive_v1。
6. 版本管理与发布 → 创建版本并审批上线。

凭据 = App ID + App Secret；platform = "lark"；connection_mode = "stream"。

## Telegram 人工步骤

1. Telegram 搜索 @BotFather，发送 /newbot。
2. 依次填写显示名称和用户名（以 bot 结尾）。
3. BotFather 返回 HTTP API Token（格式：数字:字母串），即全部凭据。
4. 如需群内接收所有消息，向 BotFather 发 /setprivacy，设为 Disable。

凭据 = Token；platform = "telegram"；connection_mode = "stream"（长轮询，无需公网）。

## 钉钉(DingTalk) 人工步骤

1. 打开 open.dingtalk.com，登录企业账号。
2. 应用开发 → 企业内部应用 → 创建应用，填写名称。
3. 凭证与基础信息 → 记录 AppKey（即 client_id）和 AppSecret（即 client_secret）。
4. 机器人 → 创建机器人，记录 RobotCode（即 robot_code）。
5. 消息推送 → 开启 Stream 模式（局域网无公网即可）。

凭据 = AppKey(client_id) + AppSecret(client_secret) + RobotCode(robot_code)；platform = "dingtalk"；connection_mode = "stream"。

## 企业微信(WeCom) 人工步骤

**注意：企业微信仅支持 webhook 回调模式，需要公网可达的回调 URL。**

1. 打开 work.weixin.qq.com，登录企业账号。
2. 应用管理 → 创建自建应用，填写名称。
3. 记录 CorpID（即 corp_id，在"我的企业"页面）、AgentId（即 agent_id）和 Secret（即 secret）。
4. 接收消息 → 设置接收消息的 API → 记录 Token（即 token）和 EncodingAESKey（即 aes_key）。

凭据 = corp_id + agent_id + secret + token + aes_key；platform = "wework"；connection_mode = "webhook"。
渠道创建后，把 <站点>/api/imbot/webhook/<channel_id> 填入企业微信的回调 URL，引导用户触发 URL 验证。

## 微信ClawBot(WeChat) 人工步骤

**注意：微信个人号机器人（腾讯 openclaw-weixin / iLink 协议）不需要创建应用、不需要任何 App Secret / Token —— 凭据由「扫码登录」当场生成。**

1. 直接调用 imbot_request_credential_link(platform="wechat", name="<机器人名>", connection_mode="stream")，拿到扫码页链接。
2. 把 link_markdown 原样发给用户，让其在浏览器打开，页面会显示二维码。
3. 用户用**手机微信「扫一扫」**扫码并在手机上确认（如提示，输入手机显示的数字验证码）。
4. 确认成功后系统用微信返回的 bot_token 自动创建渠道并显示 channel_id（无需用户回填任何密钥）。

凭据 = 无（扫码即得 bot_token）；platform = "wechat"；connection_mode = "stream"（长轮询，无需公网）。

## 8 步流程

1. 确认平台（飞书(lark) / Telegram(telegram) / 钉钉(dingtalk) / 企业微信(wework) / 微信ClawBot(wechat)）。
2. 讲解对应平台人工步骤，等用户拿到凭据（微信为扫码，无需凭据）。
3. 调用 imbot_request_credential_link，参数：
   - 飞书/钉钉/Telegram: platform="lark"|"dingtalk"|"telegram", connection_mode="stream"
   - 企业微信: platform="wework", connection_mode="webhook"
   - 微信ClawBot: platform="wechat", connection_mode="stream"（链接打开即显示二维码，用户扫码登录，无需在对话里提交任何凭据）
   工具返回的 url 已是可直接点击的绝对地址；把返回的 link_markdown 原样发给用户（会渲染成可点击链接）。绝不把 App Secret / Token 等凭据留在对话里。
4. 等用户通过链接提交凭据（页面显示成功或工具返回 channel_id）。
   - 微信：用户扫码并在手机确认后，页面直接显示成功与 channel_id（无凭据回填步骤）。
   - 企业微信额外步骤：把 <站点>/api/imbot/webhook/<channel_id> 填入企业微信回调配置，引导用户触发 URL 验证。
5. 调用 imbot_test_channel(channel_id) 验证连通性。
6. 引导用户把机器人拉入目标群/私聊并发一条消息；调用 imbot_list_pending_chats 查待配对，再调用 imbot_approve_chat 审批。
7. 请用户在 IM 里再发一条消息，确认系统回复正常（双向闭环）。
8. 调用 imbot_channel_status 最终确认 active，告知接入完成。`

// StartOnboarding handles POST /api/projects/:id/imbot/onboarding.
// It creates a guidance issue in the project and starts a workspace/agent
// conversation seeded with the onboarding kickoff prompt. Returns 201
// {issue_id, workspace_id}.
func (h *IMBotHandler) StartOnboarding(c *gin.Context) {
	pid, _, ownerRef, ok := h.writeAccessWithOwner(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	if h.dispatch == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "dispatch not configured"})
		return
	}

	// Onboarding is an interactive wizard: it must WAIT for the user (choose
	// platform, paste credentials via the secure link, add the bot to a group).
	// So create the workspace in "bypassPermissions" — the agent skips permission
	// prompts but the autohost watchdog does NOT auto-continue turns. (Default
	// would be "autohost", which would barrel through without the user.)
	target, err := h.dispatch.RouteInProject(ctx, ownerRef, pid, onboardingKickoffPrompt, service.RouteHint{
		ForceNew:       true,
		TitleHint:      "接入 IM 机器人",
		PermissionMode: "bypassPermissions",
	})
	if err != nil {
		slog.ErrorContext(ctx, "imbot: RouteInProject failed", "project_id", pid, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	// Deliver the kickoff prompt to the new workspace's agent session.
	// workDir must be non-empty — AgentProxy.Deliver returns immediately when
	// workDir is empty, silently dropping the message.
	if h.deliverer != nil {
		workDir, pathErr := h.svc.WorkspacePath(ctx, target.WorkspaceID)
		if pathErr != nil {
			slog.WarnContext(ctx, "imbot: workspace path lookup failed", "workspace_id", target.WorkspaceID, "err", pathErr)
		}
		if workDir != "" {
			if _, _, derr := h.deliverer.Deliver(ctx, target.WorkspaceID, workDir, onboardingKickoffPrompt, ""); derr != nil {
				slog.WarnContext(ctx, "imbot: deliver kickoff prompt failed", "workspace_id", target.WorkspaceID, "err", derr)
			}
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"issue_id":     target.IssueID,
		"workspace_id": target.WorkspaceID,
	})
}

// GetOnboardingInfo handles GET /api/imbot/onboarding/:token/info.
// Public endpoint (token is the auth). Returns platform/channel_name/connection_mode
// so the credential form can render platform-correct fields. Read-only — does NOT
// consume the token. Returns 410 for invalid/expired/used tokens.
func (h *IMBotHandler) GetOnboardingInfo(c *gin.Context) {
	token := c.Param("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing token"})
		return
	}

	platform, channelName, connectionMode, err := h.svc.GetOnboardingTokenInfo(c.Request.Context(), token)
	if err != nil {
		if errors.Is(err, service.ErrOnboardingTokenInvalid) {
			c.JSON(http.StatusGone, gin.H{"error": "invalid or expired token"})
			return
		}
		slog.ErrorContext(c.Request.Context(), "imbot: GetOnboardingTokenInfo unexpected error", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"platform":        platform,
		"channel_name":    channelName,
		"connection_mode": connectionMode,
	})
}

// SubmitOnboardingCredential handles POST /api/imbot/onboarding/:token/credential.
// This endpoint is token-authenticated (the one-time token IS the authorization)
// and is mounted OUTSIDE the authenticated api group. The credential map is bound
// from the JSON body; no submitted value is ever echoed in the response.
func (h *IMBotHandler) SubmitOnboardingCredential(c *gin.Context) {
	token := c.Param("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing token"})
		return
	}

	var credential map[string]any
	if err := c.ShouldBindJSON(&credential); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	channelID, err := h.svc.SubmitOnboardingCredential(c.Request.Context(), token, credential)
	if err != nil {
		if errors.Is(err, service.ErrOnboardingTokenInvalid) {
			c.JSON(http.StatusGone, gin.H{"error": "invalid or expired token"})
			return
		}
		if errors.Is(err, service.ErrInvalidChannelConfig) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		slog.ErrorContext(c.Request.Context(), "imbot: SubmitOnboardingCredential unexpected error", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	// Never echo any submitted value — only return the new channel id.
	c.JSON(http.StatusOK, gin.H{"ok": true, "channel_id": channelID})
}

// IssueWechatLink mints a one-time WeChat QR-onboarding link for a project and
// returns its root-relative path. It lets the settings UI open the scan page
// directly — the same secure one-time-token flow the onboarding assistant uses,
// minus the chat step — so connecting a WeChat bot is discoverable without
// knowing to ask the assistant. WeChat needs no pasted secret: the bot_token is
// minted by the scan itself. Requires project write access (same gate as the
// AI-guided onboarding start).
func (h *IMBotHandler) IssueWechatLink(c *gin.Context) {
	pid, _, _, ok := h.writeAccessWithOwner(c)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	_ = c.ShouldBindJSON(&body)
	name := body.Name
	if name == "" {
		name = "微信ClawBot"
	}
	rawToken, err := h.svc.IssueOnboardingToken(c.Request.Context(), pid, "wechat", name, "stream")
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": "/imbot/onboarding/" + rawToken})
}

// WechatLoginStart handles POST /api/imbot/onboarding/:token/wechat/login/start.
// Token-authenticated (mounted outside the auth group). It kicks off the WeChat
// QR-scan handshake and returns the URL to render as a scannable QR image.
func (h *IMBotHandler) WechatLoginStart(c *gin.Context) {
	token := c.Param("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing token"})
		return
	}
	img, err := h.svc.StartWechatLogin(c.Request.Context(), token)
	if err != nil {
		h.writeWechatLoginError(c, err, "StartWechatLogin")
		return
	}
	c.JSON(http.StatusOK, gin.H{"qrcode_img_content": img})
}

// WechatLoginPoll handles POST /api/imbot/onboarding/:token/wechat/login/poll.
// Body is optional {verify_code}. Returns {status, channel_id?}; on
// status=="confirmed" the WeChat channel has been created.
func (h *IMBotHandler) WechatLoginPoll(c *gin.Context) {
	token := c.Param("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing token"})
		return
	}
	var body struct {
		VerifyCode string `json:"verify_code"`
	}
	_ = c.ShouldBindJSON(&body) // body is optional; an empty body is fine

	res, err := h.svc.PollWechatLogin(c.Request.Context(), token, body.VerifyCode)
	if err != nil {
		h.writeWechatLoginError(c, err, "PollWechatLogin")
		return
	}
	resp := gin.H{"status": res.Status}
	if res.ChannelID != 0 {
		resp["channel_id"] = res.ChannelID
	}
	c.JSON(http.StatusOK, resp)
}

// writeWechatLoginError maps login-flow errors: an invalid/expired onboarding
// token is 410, a non-wechat/invalid config is 400, and an upstream iLink
// failure is 502 (generic message; details are logged, never echoed).
func (h *IMBotHandler) writeWechatLoginError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, service.ErrOnboardingTokenInvalid):
		c.JSON(http.StatusGone, gin.H{"error": "invalid or expired token"})
	case errors.Is(err, service.ErrInvalidChannelConfig):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		slog.ErrorContext(c.Request.Context(), "imbot: "+op+" failed", "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "wechat login failed, please retry"})
	}
}

// ─── MCP handler methods (Epic #555 T4) ──────────────────────────────────────
// All five methods follow the same pattern: resolve uid from MCPTokenAuth context,
// resolve pid from the URL param, enforce CanAccessProject, then call the service.

// mcpReadAccess resolves the project ID from the URL :id param, validates the
// caller's identity from auth_user_id (set by MCPTokenAuth), and calls
// CanAccessProject. Used by the /mcp/projects/:id/imbot/* handlers. The :id
// param name is shared with other /mcp/projects/:id/* routes in the same gin group.
func (h *IMBotHandler) mcpReadAccess(c *gin.Context) (projectID, uid int64, ok bool) {
	projectID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || projectID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad project id"})
		return 0, 0, false
	}
	uid = c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return 0, 0, false
	}
	if _, err := h.authz.CanAccessProject(c.Request.Context(), uid, projectID); err != nil {
		writeAuthzError(c, err)
		return 0, 0, false
	}
	return projectID, uid, true
}

// MCPRequestCredentialLink handles POST /mcp/projects/:pid/imbot/onboarding-token.
// Issues a one-time onboarding token and returns a root-relative credential
// submission URL. The raw token is embedded only in the URL — never in any other
// response field.
func (h *IMBotHandler) MCPRequestCredentialLink(c *gin.Context) {
	pid, _, ok := h.mcpReadAccess(c)
	if !ok {
		return
	}
	var body struct {
		Platform       string `json:"platform" binding:"required"`
		Name           string `json:"name" binding:"required"`
		ConnectionMode string `json:"connection_mode"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	mode := body.ConnectionMode
	if mode == "" {
		mode = "stream"
	}
	rawToken, err := h.svc.IssueOnboardingToken(c.Request.Context(), pid, body.Platform, body.Name, mode)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	// Return a ROOT-RELATIVE path — never an absolute URL. The link is shown in the
	// niuniu chat, where the user is already on the correct origin (personal:
	// http://127.0.0.1:<port>, team: https://<domain>); clicking a same-origin
	// markdown link lets the browser prepend the live scheme+host automatically, so
	// one path works for both editions. The server never has to know (or guess, from
	// spoofable request headers) its own public address.
	onboardPath := "/imbot/onboarding/" + rawToken
	c.JSON(http.StatusOK, gin.H{
		"url":                onboardPath,
		"link_markdown":      "[点击打开凭据录入页面（" + body.Platform + "）](" + onboardPath + ")",
		"expires_in_seconds": 900,
		"note":               "url 是站点内相对路径，浏览器会按当前页面自动补全协议与域名/端口（个人版与团队版通用）。请把 link_markdown 原样发给用户（渲染为可点击链接）；切勿把凭据留在对话里。",
	})
}

// MCPTestChannel handles POST /mcp/projects/:pid/imbot/channels/:cid/test.
func (h *IMBotHandler) MCPTestChannel(c *gin.Context) {
	pid, uid, ok := h.mcpReadAccess(c)
	if !ok {
		return
	}
	cid, err := strconv.ParseInt(c.Param("cid"), 10, 64)
	if err != nil || cid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad cid"})
		return
	}
	// Channels are owner-level; resolve the project's owner to authorize the test.
	owner, aerr := h.authz.CanAccessProject(c.Request.Context(), uid, pid)
	if aerr != nil {
		writeAuthzError(c, aerr)
		return
	}
	if err := h.svc.TestChannel(c.Request.Context(), owner, cid); err != nil {
		if errors.Is(err, service.ErrIMBotNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "not found"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// MCPListPendingChats handles GET /mcp/projects/:pid/imbot/pending-chats.
// Pending chats are owner-level: a bot has no home project, and a chat only gains
// a project_id once it is approved/routed. So the list is always resolved across
// the project owner's bots (the shared-bot 待配对列表); the wizard can approve chats
// that will be routed to this or a sibling project. channel_id filters by channel.
// The historical scope=owner query param is accepted but is now the only behavior.
func (h *IMBotHandler) MCPListPendingChats(c *gin.Context) {
	pid, uid, ok := h.mcpReadAccess(c)
	if !ok {
		return
	}
	type pendingChat struct {
		ID        int64  `json:"id"`
		ChannelID int64  `json:"channel_id"`
		ProjectID *int64 `json:"project_id"`
		ChatExtID string `json:"chat_ext_id"`
		ChatName  string `json:"chat_name"`
		Status    string `json:"status"`
	}

	// Resolve the project owner and list owner-level pending chats.
	owner, aerr := h.authz.CanAccessProject(c.Request.Context(), uid, pid)
	if aerr != nil {
		writeAuthzError(c, aerr)
		return
	}
	src, err := h.svc.ListPendingChatsByOwner(c.Request.Context(), owner)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var channelFilter int64
	if raw := c.Query("channel_id"); raw != "" {
		channelFilter, _ = strconv.ParseInt(raw, 10, 64)
	}
	pending := make([]pendingChat, 0, len(src))
	for _, ch := range src {
		if ch.Status != "pending" {
			continue
		}
		if channelFilter > 0 && ch.ChannelID != channelFilter {
			continue
		}
		pending = append(pending, pendingChat{
			ID:        ch.ID,
			ChannelID: ch.ChannelID,
			ProjectID: ch.ProjectID,
			ChatExtID: ch.ChatExtID,
			ChatName:  ch.ChatName,
			Status:    ch.Status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": pending})
}

// MCPApproveChat handles POST /mcp/projects/:pid/imbot/chats/:chatid/approve.
// An optional project_id in the body routes the chat to a different (same-owner)
// project; when absent it defaults to the current project (the wizard's project).
func (h *IMBotHandler) MCPApproveChat(c *gin.Context) {
	pid, uid, ok := h.mcpReadAccess(c)
	if !ok {
		return
	}
	chatID, err := strconv.ParseInt(c.Param("chatid"), 10, 64)
	if err != nil || chatID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad chatid"})
		return
	}
	targetProject := pid
	var body struct {
		ProjectID int64 `json:"project_id"`
	}
	// Body is optional; ignore a bind error (empty body) and default to pid.
	_ = c.ShouldBindJSON(&body)
	if body.ProjectID > 0 {
		targetProject = body.ProjectID
	}
	dto, err := h.svc.ApproveChatToProject(c.Request.Context(), chatID, targetProject, uid)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, dto)
}

// MCPChannelStatus handles GET /mcp/projects/:pid/imbot/channels/:cid/status.
// The bot is owner-level (no home project and it may not yet route a chat here),
// so status is resolved by the project's owner, not by a project reverse lookup.
func (h *IMBotHandler) MCPChannelStatus(c *gin.Context) {
	pid, uid, ok := h.mcpReadAccess(c)
	if !ok {
		return
	}
	cid, err := strconv.ParseInt(c.Param("cid"), 10, 64)
	if err != nil || cid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad cid"})
		return
	}
	owner, aerr := h.authz.CanAccessProject(c.Request.Context(), uid, pid)
	if aerr != nil {
		writeAuthzError(c, aerr)
		return
	}
	ch, err := h.svc.GetChannelByOwner(c.Request.Context(), owner, cid)
	if err != nil {
		if errors.Is(err, service.ErrIMBotNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":              ch.ID,
		"name":            ch.Name,
		"channel_type":    ch.ChannelType,
		"connection_mode": ch.ConnectionMode,
		"status":          ch.Status,
		"has_credential":  ch.HasCredential,
	})
}

// Webhook is the optional public inbound entry (design §5.2), mounted WITHOUT
// auth (external platforms call it). It answers the URL-verification challenge,
// then feeds the normalized event through the same HandleInbound pipeline as the
// stream long connection. The pairing-approval gate is the security boundary
// (an unknown chat only ever creates a pending record).
func (h *IMBotHandler) Webhook(c *gin.Context) {
	channelID, ok := imbotIDParam(c, "channelId")
	if !ok {
		return
	}
	echo, isChallenge, err := h.svc.HandleWebhook(c.Request.Context(), channelID, c.Request)
	if err != nil {
		if errors.Is(err, service.ErrIMBotNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}
		if errors.Is(err, service.ErrWebhookUnauthorized) {
			// Failed platform signature verification: reject a forged webhook.
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		// A malformed / non-actionable body is acknowledged (200) so the platform
		// stops retrying; details are logged in the service layer.
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	if isChallenge {
		c.Data(http.StatusOK, "application/json; charset=utf-8", echo)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
