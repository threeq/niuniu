package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// ShareChatSender 是公开对话对 agentproxy 的依赖切缝（*agentproxy.AgentProxy
// 天然满足）。接口化让 handler 测试可以注入假实现，不必起真 proxy。
type ShareChatSender interface {
	PrepareUserSend(ctx context.Context, workspaceID int64)
	Deliver(ctx context.Context, workspaceID int64, workDir, content, attachments string) (queued bool, queueID int64, err error)
}

// ShareEventHub 是公开观看者实时更新的依赖切缝（*agentproxy.SessionHub 天然
// 满足）。只订阅工作空间事件流，不暴露任何写能力。
type ShareEventHub interface {
	Subscribe(workspaceID int64, windowID string) chan event.OutputEvent
	Unsubscribe(workspaceID int64, windowID string)
}

// SharePublicHandler 是 /share 外链的公开访问面（未登录）。端点：
//
//	GET  /share/api/ws/:token/info     元信息（是否需要密码等，不含内容）
//	POST /share/api/ws/:token/unlock   密码解锁 → 发短时 HMAC cookie
//	GET  /share/api/ws/:token/messages 解锁后的聊天内容（只读文本行）
//	POST /share/api/ws/:token/chat     解锁+allowChat 时向工作空间 agent 发消息
//	GET  /share/api/ws/:token/events   解锁后的实时更新信号（SSE）
//
// token 不存在/已撤销/已过期统一 404 SHARE_NOT_FOUND（不区分原因，防枚举）。
// feature 门（团队版）在 router 层挂 LicenseFeatureGate。
type SharePublicHandler struct {
	svc    *service.WorkspaceShareService
	q      *store.Queries
	sender ShareChatSender // 对话通道；nil 时 chat 端点恒 503
	hub    ShareEventHub   // 事件流；nil 时 events 端点恒 503
}

func NewSharePublicHandler(svc *service.WorkspaceShareService, q *store.Queries, sender ShareChatSender, hub ShareEventHub) *SharePublicHandler {
	return &SharePublicHandler{svc: svc, q: q, sender: sender, hub: hub}
}

// shareCookieName 是解锁 cookie 名。Path=/ 使得 SPA 页面与 /share/api 同源
// 可读；HttpOnly+SameSite=Lax。
const shareCookieName = "niu_share_unlock"

func abortShareUnavailable(c *gin.Context) {
	RespondError(c, http.StatusNotFound, "SHARE_NOT_FOUND", "share not found or expired")
}

// resolveForPublic 解析并校验分享可用，失败时已写响应。
func (h *SharePublicHandler) resolveForPublic(c *gin.Context) (service.ResolvedShare, bool) {
	token := c.Param("token")
	resolved, err := h.svc.ResolveActive(c.Request.Context(), token)
	if err != nil {
		abortShareUnavailable(c)
		return service.ResolvedShare{}, false
	}
	return resolved, true
}

// shareMetaResponse 是 info/unlock 的响应形状。unlocked 告诉前端是否还需要
// 弹密码框；name/issueTitle 只在已解锁时下发，未解锁时保持空（不给匿名
// 探测工作空间名的口子）。allowChat 是分享的静态标记（与是否解锁无关——
// 前端只在解锁后用它渲染输入框）。
type shareMetaResponse struct {
	ExpiresAt    string `json:"expiresAt"`
	PasswordRequired bool  `json:"passwordRequired"`
	Unlocked     bool   `json:"unlocked"`
	AllowChat    bool   `json:"allowChat"`
	Name         string `json:"name,omitempty"`
	IssueTitle   string `json:"issueTitle,omitempty"`
}

func (h *SharePublicHandler) metaResponse(c *gin.Context, resolved service.ResolvedShare) shareMetaResponse {
	resp := shareMetaResponse{
		ExpiresAt:        resolved.Share.ExpiresAt.UTC().Format(timeRFC3339),
		PasswordRequired: true,
		Unlocked:         h.isUnlocked(c, resolved),
		AllowChat:        resolved.CanChat(),
	}
	if resp.Unlocked {
		resp.Name = resolved.WorkspaceName
		resp.IssueTitle = resolved.IssueTitle
	}
	return resp
}

func (h *SharePublicHandler) isUnlocked(c *gin.Context, resolved service.ResolvedShare) bool {
	cookie, err := c.Cookie(shareCookieName)
	if err != nil || cookie == "" {
		return false
	}
	return h.svc.VerifyUnlock(resolved.Share.ID, resolved.Share.TokenHash, cookie)
}

// setUnlockCookie 在解锁成功后发 cookie（1 小时，滑动窗口由重新 unlock 续）。
func (h *SharePublicHandler) setUnlockCookie(c *gin.Context, resolved service.ResolvedShare) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     shareCookieName,
		Value:    h.svc.SignUnlock(resolved.Share.ID, resolved.Share.TokenHash),
		Path:     "/",
		MaxAge:   3600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// Info — GET /share/api/ws/:token/info
func (h *SharePublicHandler) Info(c *gin.Context) {
	resolved, ok := h.resolveForPublic(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, h.metaResponse(c, resolved))
}

type unlockReq struct {
	Password string `json:"password" binding:"required"`
}

// Unlock — POST /share/api/ws/:token/unlock
func (h *SharePublicHandler) Unlock(c *gin.Context) {
	resolved, ok := h.resolveForPublic(c)
	if !ok {
		return
	}
	var req unlockReq
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "password is required")
		return
	}
	if err := h.svc.VerifyPassword(c.Request.Context(), resolved.Share, req.Password); err != nil {
		if err == service.ErrSharePassword {
			RespondError(c, http.StatusUnauthorized, "SHARE_PASSWORD", "wrong password")
			return
		}
		InternalError(c, err)
		return
	}
	h.setUnlockCookie(c, resolved)
	// 解锁响应直接报 unlocked=true：cookie 刚写入响应头，c.Cookie 读不到它。
	meta := h.metaResponse(c, resolved)
	meta.Unlocked = true
	meta.Name = resolved.WorkspaceName
	meta.IssueTitle = resolved.IssueTitle
	c.JSON(http.StatusOK, meta)
}

// shareMessageResponse 是公开聊天消息行：只暴露 text 事件的最小字段集。
type shareMessageResponse struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
}

// Messages — GET /share/api/ws/:token/messages?limit=N[&before=<msgId>][&after=<msgId>]
//
// 只返回 role ∈ {user, assistant} 且 event_type='text' 的行——tool 调用/
// 结果、thinking、成本等内部细节不对匿名访问者暴露。需有效解锁 cookie。
//
// before 用于向上翻页（更老的一页），after 用于增量拉取；两者互斥，after 优先。
// 响应里的 cursor 是这一页最老的原始行 id（过滤前的行），观看者拿它继续 before
// 翻页——只按可见消息的 id 翻页会在「整页都是 tool/thinking」时卡住。
func (h *SharePublicHandler) Messages(c *gin.Context) {
	resolved, ok := h.resolveForPublic(c)
	if !ok {
		return
	}
	if !h.isUnlocked(c, resolved) {
		RespondError(c, http.StatusUnauthorized, "SHARE_LOCKED", "password required")
		return
	}

	limit := int64(300)
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.ParseInt(l, 10, 64); err == nil && parsed > 0 && parsed <= 2000 {
			limit = parsed
		}
	}

	ctx := c.Request.Context()
	after, before := c.Query("after"), c.Query("before")
	var rows []store.AgentMessage
	var err error
	switch {
	case after != "":
		rows, err = h.q.ListAgentMessagesAfter(ctx, store.ListAgentMessagesAfterParams{
			WorkspaceID: resolved.WorkspaceID,
			ID:          after,
			ID_2:        after,
			ID_3:        after,
			Limit:       limit + 1,
		})
	case before != "":
		rows, err = h.q.ListAgentMessagesBefore(ctx, store.ListAgentMessagesBeforeParams{
			WorkspaceID: resolved.WorkspaceID,
			ID:          before,
			ID_2:        before,
			ID_3:        before,
			Limit:       limit + 1,
		})
	default:
		rows, err = h.q.ListAgentMessagesLatest(ctx, store.ListAgentMessagesLatestParams{
			WorkspaceID: resolved.WorkspaceID,
			Limit:       limit + 1,
		})
	}
	if err != nil {
		InternalError(c, err)
		return
	}
	hasMore := int64(len(rows)) > limit
	if hasMore {
		if after != "" {
			rows = rows[:limit] // 增量：丢最新的一行，保持 ASC
		} else {
			rows = rows[1:] // 最新/更老一页：丢最老的一行，保持 ASC
		}
	}

	out := make([]shareMessageResponse, 0, len(rows))
	for _, m := range rows {
		if m.EventType != "text" {
			continue
		}
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		out = append(out, shareMessageResponse{
			ID:        m.ID,
			Role:      m.Role,
			Content:   m.Content,
			CreatedAt: m.CreatedAt.UTC().Format(timeRFC3339),
		})
	}
	// cursor = 本页最老的原始行 id（rows 已按 ASC 排序），供下一轮 before 翻页。
	cursor := ""
	if len(rows) > 0 {
		cursor = rows[0].ID
	}
	c.JSON(http.StatusOK, gin.H{"messages": out, "hasMore": hasMore, "cursor": cursor})
}

type shareChatReq struct {
	Content string `json:"content" binding:"required"`
}

// Chat — POST /share/api/ws/:token/chat
//
// 允许对话（share.allow_chat）时，解锁的观看者可向工作空间 agent 发一条
// 消息。门依次是：分享可用 → 已解锁 → allowChat → 工作空间未归档 → 内容
// 非空且不超长。匿名发送刻意不调 SetSessionUser（不把 NULL 身份盖到团队
// 工作空间的 current_session_user_id 上）。
func (h *SharePublicHandler) Chat(c *gin.Context) {
	resolved, ok := h.resolveForPublic(c)
	if !ok {
		return
	}
	if !h.isUnlocked(c, resolved) {
		RespondError(c, http.StatusUnauthorized, "SHARE_LOCKED", "password required")
		return
	}
	if !resolved.CanChat() {
		RespondError(c, http.StatusForbidden, "SHARE_CHAT_DISABLED", "chat is not enabled for this share")
		return
	}
	if resolved.WorkspaceArchived {
		RespondError(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "archived workspaces do not accept messages")
		return
	}
	if h.sender == nil {
		RespondError(c, http.StatusServiceUnavailable, "SHARE_CHAT_UNAVAILABLE", "chat backend unavailable")
		return
	}

	var req shareChatReq
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "content is required")
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		BadRequest(c, "content is required")
		return
	}
	if len(content) > service.MaxSharedChatLen {
		BadRequest(c, "content too long")
		return
	}

	ctx := c.Request.Context()
	h.sender.PrepareUserSend(ctx, resolved.WorkspaceID)
	queued, queueID, err := h.sender.Deliver(ctx, resolved.WorkspaceID, resolved.WorkspacePath, content, "")
	if err != nil {
		InternalError(c, err)
		return
	}
	if queued {
		c.JSON(http.StatusAccepted, gin.H{"status": "queued", "queueId": queueID})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "accepted"})
}

// Events — GET /share/api/ws/:token/events
//
// 已解锁观看者的实时更新通道（SSE）。只下发「有新内容」的信号，不下发正文
// ——正文仍走 /messages（那里已经过滤掉 tool/thinking 等内部事件）。这样匿名
// 观看者既拿不到额外信息，也不会因为信号里缺少字段而看到半截内容。
// 服务端按 500ms 合并信号：流式输出时每 token 都会广播一条 text 事件，逐条
// 转发会让观看者每秒重拉十几次列表。
func (h *SharePublicHandler) Events(c *gin.Context) {
	resolved, ok := h.resolveForPublic(c)
	if !ok {
		return
	}
	if !h.isUnlocked(c, resolved) {
		RespondError(c, http.StatusUnauthorized, "SHARE_LOCKED", "password required")
		return
	}
	if h.hub == nil {
		RespondError(c, http.StatusServiceUnavailable, "SHARE_LIVE_UNAVAILABLE", "live updates unavailable")
		return
	}

	windowID := "share:" + uuid.NewString()
	ch := h.hub.Subscribe(resolved.WorkspaceID, windowID)
	defer h.hub.Unsubscribe(resolved.WorkspaceID, windowID)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	// 反向代理（nginx）默认会攒满缓冲才下发，SSE 会被卡住。
	c.Header("X-Accel-Buffering", "no")
	fmt.Fprint(c.Writer, ": connected\n\n")
	c.Writer.Flush()

	ctx := c.Request.Context()
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	const signalInterval = 500 * time.Millisecond
	var lastSignal time.Time

	c.Stream(func(w io.Writer) bool {
		select {
		case <-ctx.Done():
			return false
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			return true
		case ev, open := <-ch:
			if !open {
				return false
			}
			if ev.Type != event.EventText || (ev.Role != "user" && ev.Role != "assistant") {
				return true
			}
			if now := time.Now(); now.Sub(lastSignal) >= signalInterval {
				lastSignal = now
				fmt.Fprint(w, "data: {\"type\":\"message\"}\n\n")
			}
			return true
		}
	})
}

// timeRFC3339 统一公开端点的时间格式。
const timeRFC3339 = "2006-01-02T15:04:05Z07:00"
