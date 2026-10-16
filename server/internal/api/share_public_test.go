package api

import (
	"fmt"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// newSharePublicHarness 建一个 SharePublicHandler + 已插入一条 agent_messages
// 的 sqlite 环境，返回 (handler, share token, password)。
func newSharePublicHarness(t *testing.T) (*SharePublicHandler, string, string) {
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
		Path:     t.TempDir(),
		Name:     "share-ws",
		OwnerType: "user",
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	settings := service.NewServerSettingsService(store.Wrap(db))
	svc := service.NewWorkspaceShareService(q, settings, []byte("test-secret-0123456789abcdef"))
	password := "pass1234"
	info, err := svc.Create(context.Background(), wsID.ID, 0, password, time.Now().UTC().Add(time.Hour))
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

	return NewSharePublicHandler(svc, q), info.RawToken, password
}

func shareRouter(h *SharePublicHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/share/api/ws/:token/info", h.Info)
	r.POST("/share/api/ws/:token/unlock", h.Unlock)
	r.GET("/share/api/ws/:token/messages", h.Messages)
	return r
}

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
