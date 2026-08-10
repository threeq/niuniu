// imbot_owner_test.go — handler tests for the owner-level IM Bot routes
// (shared bot / multi-project routing). These mount under /api/imbot/... with a
// stubbed auth middleware that injects auth_user_id (the real IdentityResolver
// is exercised elsewhere); the focus here is owner resolution + authz + routing.
package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// setupIMBotOwner wires the owner-level imbot routes with a stub auth middleware
// that sets auth_user_id from the X-Test-Uid header.
//
// Seed: user 1 owns projects 1 & 2; user 2 owns project 3. A lark bot (channel 1)
// is created under project 1 (owner user 1) with a pending chat (chat 1).
func setupIMBotOwner(t *testing.T) (r *gin.Engine, chan1, pendingChat, proj1, proj2, proj3 int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openMCPPermTestDB(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)

	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','admin')`)
	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (2,'u2','x','admin')`)
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1,'p1','user',1)`)
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (2,'p2','user',1)`)
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (3,'p3','user',2)`)
	proj1, proj2, proj3 = 1, 2, 3

	kr, err := crypto.LoadOrCreate(t.TempDir() + "/kr")
	require.NoError(t, err)
	adapters := map[imbot.ChannelType]imbot.ChannelAdapter{imbot.ChannelLark: &imbotMCPStubAdapter{}}
	authz := service.NewAuthz(q, db)
	svc := service.NewIMBotService(q, db, kr, authz, adapters)

	// Owner-level bot for user 1 via the service (fingerprints the app identity).
	ownerU1 := service.OwnerRef{Type: "user", ID: 1}
	ch, err := svc.CreateChannel(context.Background(), ownerU1, service.CreateChannelInput{
		ChannelType: "lark", Name: "bot", Credential: map[string]any{"app_id": "a", "app_secret": "s"},
	})
	require.NoError(t, err)
	chan1 = ch.ID
	chat, err := svc.AddChat(context.Background(), ownerU1, chan1, "oc_p", "Pending")
	require.NoError(t, err)
	pendingChat = chat.ID

	handler := api.NewIMBotHandler(svc, authz, db)

	r = gin.New()
	g := r.Group("/api")
	g.Use(func(c *gin.Context) {
		if uid := c.GetHeader("X-Test-Uid"); uid != "" {
			if uid == "1" {
				c.Set("auth_user_id", int64(1))
			} else {
				c.Set("auth_user_id", int64(2))
			}
		}
		c.Next()
	})
	g.GET("/imbot/bots", handler.ListBots)
	g.POST("/imbot/bots", handler.CreateBot)
	g.POST("/imbot/bots/:cid/test", handler.TestBot)
	g.DELETE("/imbot/bots/:cid", handler.DeleteBot)
	g.GET("/imbot/pending-chats", handler.ListPendingChatsOwner)
	g.POST("/imbot/chats/:chatid/approve", handler.ApproveChatOwner)
	g.POST("/imbot/chats/:chatid/reassign", handler.ReassignChatOwner)
	return
}

func ownerReq(t *testing.T, r *gin.Engine, method, path, uid, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Uid", uid)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestIMBotOwner_ListBots_ScopedToOwner(t *testing.T) {
	r, _, _, _, _, _ := setupIMBotOwner(t)

	// user 1 sees their bot.
	w := ownerReq(t, r, http.MethodGet, "/api/imbot/bots", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	items := resp["items"].([]any)
	assert.Len(t, items, 1)

	// user 2 (different owner) sees none of user 1's bots.
	w = ownerReq(t, r, http.MethodGet, "/api/imbot/bots", "2", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Len(t, resp["items"].([]any), 0)
}

func TestIMBotOwner_ApproveChat_RoutesToProject(t *testing.T) {
	r, _, pendingChat, _, proj2, _ := setupIMBotOwner(t)

	w := ownerReq(t, r, http.MethodPost, "/api/imbot/chats/"+itoa(pendingChat)+"/approve", "1",
		`{"project_id":`+itoa(proj2)+`}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "active", resp["status"])
	assert.EqualValues(t, proj2, resp["project_id"])
}

func TestIMBotOwner_ApproveChat_CrossOwnerRejected(t *testing.T) {
	r, _, pendingChat, _, _, proj3 := setupIMBotOwner(t)

	// Routing user 1's chat to user 2's project 3 is hidden as not-found.
	w := ownerReq(t, r, http.MethodPost, "/api/imbot/chats/"+itoa(pendingChat)+"/approve", "1",
		`{"project_id":`+itoa(proj3)+`}`)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

func TestIMBotOwner_ApproveChat_CallerNotOwner(t *testing.T) {
	r, _, pendingChat, proj1, _, _ := setupIMBotOwner(t)

	// user 2 cannot approve user 1's chat (bot owner != caller) — cross-owner
	// target project (proj1 owned by user 1) is hidden as not-found for user 2.
	w := ownerReq(t, r, http.MethodPost, "/api/imbot/chats/"+itoa(pendingChat)+"/approve", "2",
		`{"project_id":`+itoa(proj1)+`}`)
	assert.True(t, w.Code == http.StatusForbidden || w.Code == http.StatusNotFound,
		"cross-owner approve must be 403/404, got %d body=%s", w.Code, w.Body.String())
}

func TestIMBotOwner_ReassignChat(t *testing.T) {
	r, _, pendingChat, proj1, proj2, _ := setupIMBotOwner(t)

	// Approve to project 1 first, then reassign to project 2.
	w := ownerReq(t, r, http.MethodPost, "/api/imbot/chats/"+itoa(pendingChat)+"/approve", "1",
		`{"project_id":`+itoa(proj1)+`}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	w = ownerReq(t, r, http.MethodPost, "/api/imbot/chats/"+itoa(pendingChat)+"/reassign", "1",
		`{"project_id":`+itoa(proj2)+`}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.EqualValues(t, proj2, resp["project_id"])
}

func TestIMBotOwner_CreateBot_DuplicateConflict(t *testing.T) {
	r, _, _, _, _, _ := setupIMBotOwner(t)

	// The seeded bot already uses app_id "a" under user 1. A second bot with the
	// same app identity must 409.
	w := ownerReq(t, r, http.MethodPost, "/api/imbot/bots", "1",
		`{"project_id":1,"channel_type":"lark","name":"dup","credential":{"app_id":"a","app_secret":"z"}}`)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
}
