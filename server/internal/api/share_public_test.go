package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// newSharePublicHarness 建一个 SharePublicHandler + 已插入一条 agent_messages
// 的 sqlite 环境，返回 (handler, share token, password)。
// allowChat=true 时分享带 allow_chat=1；sender 传给 handler。
func newSharePublicHarnessOpts(t *testing.T, allowChat bool, sender ShareChatSender) (*SharePublicHandler, *service.WorkspaceShareService, string, string) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	store.Migrate(db)
	q := store.New(db)

	wsID, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Path:      t.TempDir(),
		Name:      "share-ws",
		OwnerType: "user",
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	settings := service.NewServerSettingsService(store.Wrap(db))
	svc := service.NewWorkspaceShareService(q, settings, []byte("test-secret-0123456789abcdef"))
	password := "pass1234"
	info, err := svc.Create(context.Background(), wsID.ID, 0, password, time.Now().UTC().Add(time.Hour), allowChat)
	if err != nil {
		t.Fatalf("create share: %v", err)
	}

	// 一条 user 文本 + 一条 tool_use（后者不应出现在公开输出里）。
	for i, m := range []store.CreateAgentMessageParams{
		{WorkspaceID: wsID.ID, Role: "user", Content: "hello", MessageID: "m1", EventType: "text"},
		{WorkspaceID: wsID.ID, Role: "assistant", Content: "world", MessageID: "m2", EventType: "text"},
		{WorkspaceID: wsID.ID, Role: "assistant", Content: "{}", MessageID: "m3", EventType: "tool_use"},
	} {
		m.ID = fmt.Sprintf("row-%d", i)
		if err := q.CreateAgentMessage(context.Background(), m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	return NewSharePublicHandler(svc, q, sender, nil), svc, info.RawToken, password
}

func newSharePublicHarness(t *testing.T) (*SharePublicHandler, string, string) {
	t.Helper()
	h, _, token, password := newSharePublicHarnessOpts(t, false, nil)
	return h, token, password
}

// fakeShareChatSender 记录 Deliver 调用，返回非 queued。
type fakeShareChatSender struct {
	prepared int
	wsIDs    []int64
	contents []string
}

func (f *fakeShareChatSender) PrepareUserSend(_ context.Context, workspaceID int64) {
	f.prepared++
	f.wsIDs = append(f.wsIDs, workspaceID)
}

func (f *fakeShareChatSender) Deliver(_ context.Context, workspaceID int64, _, content, _ string) (bool, int64, error) {
	f.contents = append(f.contents, content)
	_ = workspaceID
	return false, 0, nil
}

func shareRouter(h *SharePublicHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/share/api/ws/:token/info", h.Info)
	r.POST("/share/api/ws/:token/unlock", h.Unlock)
	r.GET("/share/api/ws/:token/messages", h.Messages)
	r.POST("/share/api/ws/:token/chat", h.Chat)
	r.GET("/share/api/ws/:token/events", h.Events)
	return r
}

// fakeShareEventHub 返回一条由测试预置的通道，便于在 ServeHTTP 之前塞事件。
type fakeShareEventHub struct {
	ch chan event.OutputEvent
}

func (f *fakeShareEventHub) Subscribe(int64, string) chan event.OutputEvent { return f.ch }
func (f *fakeShareEventHub) Unsubscribe(int64, string)                      {}

func doShareJSON(t *testing.T, r *gin.Engine, method, path string, body any, cookie *http.Cookie) (int, map[string]any, []*http.Cookie) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return w.Code, out, w.Result().Cookies()
}

func TestSharePublic_Flow(t *testing.T) {
	h, token, password := newSharePublicHarness(t)
	r := shareRouter(h)
	base := "/share/api/ws/" + token

	// info：未解锁 — 不泄露工作空间名
	code, body, _ := doShareJSON(t, r, http.MethodGet, base+"/info", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("info: status %d", code)
	}
	if body["unlocked"] != false || body["name"] != nil {
		t.Fatalf("locked info should hide name: %v", body)
	}

	// messages：未解锁 — 401
	code, _, _ = doShareJSON(t, r, http.MethodGet, base+"/messages", nil, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("locked messages: status %d, want 401", code)
	}

	// 错误密码 — 401
	code, _, _ = doShareJSON(t, r, http.MethodPost, base+"/unlock", map[string]string{"password": "wrong"}, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status %d, want 401", code)
	}

	// 正确密码 — 200 + cookie，信息带名字
	code, body, cookies := doShareJSON(t, r, http.MethodPost, base+"/unlock", map[string]string{"password": password}, nil)
	if code != http.StatusOK || body["unlocked"] != true || body["name"] != "share-ws" {
		t.Fatalf("unlock: status %d body %v", code, body)
	}
	var unlockCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == shareCookieName {
			unlockCookie = c
		}
	}
	if unlockCookie == nil {
		t.Fatal("unlock did not set cookie")
	}

	// messages：已解锁 — 只回 text 事件
	code, body, _ = doShareJSON(t, r, http.MethodGet, base+"/messages", nil, unlockCookie)
	if code != http.StatusOK {
		t.Fatalf("messages: status %d", code)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 text messages (tool_use filtered), got %d", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["content"] != "hello" || first["role"] != "user" {
		t.Fatalf("unexpected first message: %v", first)
	}
}

func TestSharePublic_UnknownToken(t *testing.T) {
	h, _, _ := newSharePublicHarness(t)
	r := shareRouter(h)
	for _, path := range []string{
		"/share/api/ws/nope/info",
		"/share/api/ws/nope/messages",
	} {
		code, _, _ := doShareJSON(t, r, http.MethodGet, path, nil, nil)
		if code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", path, code)
		}
	}
}

func unlockCookieFor(t *testing.T, r *gin.Engine, base, password string) *http.Cookie {
	t.Helper()
	_, _, cookies := doShareJSON(t, r, http.MethodPost, base+"/unlock", map[string]string{"password": password}, nil)
	for _, c := range cookies {
		if c.Name == shareCookieName {
			return c
		}
	}
	t.Fatal("unlock did not set cookie")
	return nil
}

func TestSharePublic_ChatGates(t *testing.T) {
	// allowChat=false（默认）：解锁后 chat 也 403，且 meta 报 allowChat=false。
	sender := &fakeShareChatSender{}
	h, _, token, password := newSharePublicHarnessOpts(t, false, sender)
	r := shareRouter(h)
	base := "/share/api/ws/" + token

	cookie := unlockCookieFor(t, r, base, password)
	code, _, _ := doShareJSON(t, r, http.MethodPost, base+"/chat", map[string]string{"content": "hi"}, cookie)
	if code != http.StatusForbidden {
		t.Fatalf("chat with allowChat=0: status %d, want 403", code)
	}
	_, body, _ := doShareJSON(t, r, http.MethodGet, base+"/info", nil, cookie)
	if body["allowChat"] != false {
		t.Fatalf("meta allowChat = %v, want false", body["allowChat"])
	}

	// allowChat=true：解锁后 chat 通到 Deliver；未解锁 401。
	sender2 := &fakeShareChatSender{}
	h2, svc2, token2, password2 := newSharePublicHarnessOpts(t, true, sender2)
	r2 := shareRouter(h2)
	base2 := "/share/api/ws/" + token2

	code, _, _ = doShareJSON(t, r2, http.MethodPost, base2+"/chat", map[string]string{"content": "hi"}, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("locked chat: status %d, want 401", code)
	}

	cookie2 := unlockCookieFor(t, r2, base2, password2)
	code, body, _ = doShareJSON(t, r2, http.MethodPost, base2+"/chat", map[string]string{"content": "hello from viewer"}, cookie2)
	if code != http.StatusAccepted || body["status"] != "accepted" {
		t.Fatalf("enabled chat: status %d body %v", code, body)
	}
	if sender2.prepared != 1 || len(sender2.contents) != 1 || sender2.contents[0] != "hello from viewer" {
		t.Fatalf("Deliver not invoked correctly: prepared=%d contents=%v", sender2.prepared, sender2.contents)
	}

	// meta 报 allowChat=true
	_, body, _ = doShareJSON(t, r2, http.MethodGet, base2+"/info", nil, cookie2)
	if body["allowChat"] != true {
		t.Fatalf("meta allowChat = %v, want true", body["allowChat"])
	}

	// 空白内容 400；超长 400
	code, _, _ = doShareJSON(t, r2, http.MethodPost, base2+"/chat", map[string]string{"content": "   "}, cookie2)
	if code != http.StatusBadRequest {
		t.Fatalf("blank chat: status %d, want 400", code)
	}
	long := strings.Repeat("a", service.MaxSharedChatLen+1)
	code, _, _ = doShareJSON(t, r2, http.MethodPost, base2+"/chat", map[string]string{"content": long}, cookie2)
	if code != http.StatusBadRequest {
		t.Fatalf("overlong chat: status %d, want 400", code)
	}

	// allowChat 运行时可关：PATCH 语义在 service 层，直接关掉后 chat 403
	if err := svc2.SetAllowChat(context.Background(), mustShareWorkspaceID(t, svc2, token2), mustShareID(t, svc2, token2), false); err != nil {
		t.Fatalf("SetAllowChat: %v", err)
	}
	code, _, _ = doShareJSON(t, r2, http.MethodPost, base2+"/chat", map[string]string{"content": "hi"}, cookie2)
	if code != http.StatusForbidden {
		t.Fatalf("chat after disable: status %d, want 403", code)
	}
}

func TestSharePublic_MessagesPaging(t *testing.T) {
	h, token, password := newSharePublicHarness(t)
	r := shareRouter(h)
	base := "/share/api/ws/" + token
	cookie := unlockCookieFor(t, r, base, password)

	// 首屏：3 行原始数据（含 1 行 tool_use）→ 2 条可见文本，cursor 指向最老原始行。
	code, body, _ := doShareJSON(t, r, http.MethodGet, base+"/messages?limit=3", nil, cookie)
	if code != http.StatusOK {
		t.Fatalf("latest: status %d", code)
	}
	if n := len(body["messages"].([]any)); n != 2 {
		t.Fatalf("latest messages = %d, want 2", n)
	}
	if body["cursor"] != "row-0" {
		t.Fatalf("latest cursor = %v, want row-0", body["cursor"])
	}
	if body["hasMore"] != false {
		t.Fatalf("latest hasMore = %v, want false", body["hasMore"])
	}

	// 向上翻页：before=row-2 limit=1 → 更老的 row-0/row-1 里取最新一条，
	// hasMore=true 表示还有更老的，cursor 推进到 row-1。
	code, body, _ = doShareJSON(t, r, http.MethodGet, base+"/messages?limit=1&before=row-2", nil, cookie)
	if code != http.StatusOK {
		t.Fatalf("before: status %d", code)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "world" {
		t.Fatalf("before messages = %v", msgs)
	}
	if body["hasMore"] != true || body["cursor"] != "row-1" {
		t.Fatalf("before hasMore=%v cursor=%v, want true/row-1", body["hasMore"], body["cursor"])
	}

	// 继续往前：只剩 row-0，之后没有更多。
	code, body, _ = doShareJSON(t, r, http.MethodGet, base+"/messages?limit=1&before=row-1", nil, cookie)
	if code != http.StatusOK {
		t.Fatalf("before2: status %d", code)
	}
	msgs = body["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "hello" {
		t.Fatalf("before2 messages = %v", msgs)
	}
	if body["hasMore"] != false {
		t.Fatalf("before2 hasMore = %v, want false", body["hasMore"])
	}

	// 翻到最早：空数组 + 空 cursor，前端据此停手。
	code, body, _ = doShareJSON(t, r, http.MethodGet, base+"/messages?limit=1&before=row-0", nil, cookie)
	if code != http.StatusOK {
		t.Fatalf("before3: status %d", code)
	}
	if n := len(body["messages"].([]any)); n != 0 {
		t.Fatalf("before3 messages = %d, want 0", n)
	}
	if body["hasMore"] != false || body["cursor"] != "" {
		t.Fatalf("before3 hasMore=%v cursor=%v", body["hasMore"], body["cursor"])
	}
}

func TestSharePublic_EventsRequiresUnlock(t *testing.T) {
	h, _, token, _ := newSharePublicHarnessOpts(t, false, nil)
	h.hub = &fakeShareEventHub{ch: make(chan event.OutputEvent, 4)}
	r := shareRouter(h)
	base := "/share/api/ws/" + token

	code, _, _ := doShareJSON(t, r, http.MethodGet, base+"/events", nil, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("locked events: status %d, want 401", code)
	}

	// hub 缺失时已解锁也拿不到实时通道（503），而不是挂起连接。
	h2, _, token2, password2 := newSharePublicHarnessOpts(t, false, nil)
	r2 := shareRouter(h2)
	base2 := "/share/api/ws/" + token2
	cookie2 := unlockCookieFor(t, r2, base2, password2)
	code, _, _ = doShareJSON(t, r2, http.MethodGet, base2+"/events", nil, cookie2)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("events without hub: status %d, want 503", code)
	}
}

func TestSharePublic_EventsEmitsMessageSignals(t *testing.T) {
	h, _, token, password := newSharePublicHarnessOpts(t, false, nil)
	hub := &fakeShareEventHub{ch: make(chan event.OutputEvent, 4)}
	h.hub = hub
	r := shareRouter(h)
	base := "/share/api/ws/" + token
	cookie := unlockCookieFor(t, r, base, password)

	// 一条 assistant 文本（应下发信号）+ 一条 tool_use（内部细节，不下发）。
	hub.ch <- event.OutputEvent{Type: event.EventText, Role: "assistant", Content: "hi"}
	hub.ch <- event.OutputEvent{Type: event.EventToolUse, Role: "assistant"}
	close(hub.ch)

	// c.Stream 需要 http.CloseNotifier，httptest.ResponseRecorder 不实现它，
	// 所以这里起一个真 server。
	srv := httptest.NewServer(r)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+base+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q", got)
	}
	if n := strings.Count(string(body), "data: {\"type\":\"message\"}"); n != 1 {
		t.Fatalf("expected exactly 1 message signal, got %d: %q", n, body)
	}
}

// mustShareWorkspaceID/mustShareID 从 token 反查分享行 id/工作空间 id（测试辅助）。
func mustShareWorkspaceID(t *testing.T, svc *service.WorkspaceShareService, token string) int64 {
	t.Helper()
	resolved, err := svc.ResolveActive(context.Background(), token)
	if err != nil {
		t.Fatalf("ResolveActive: %v", err)
	}
	return resolved.WorkspaceID
}

func mustShareID(t *testing.T, svc *service.WorkspaceShareService, token string) int64 {
	t.Helper()
	resolved, err := svc.ResolveActive(context.Background(), token)
	if err != nil {
		t.Fatalf("ResolveActive: %v", err)
	}
	return resolved.Share.ID
}
