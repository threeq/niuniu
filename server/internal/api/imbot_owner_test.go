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
	"strconv"
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
//
// Both users are plain members on purpose: ListBots now widens its scope for a
// global admin (users.role='admin'), so seeding these two as admins would make
// every cross-owner isolation assertion below vacuously pass. Global-admin scope
// is covered separately by TestIMBotOwner_ListBots_GlobalAdminSeesAllOwners.
func setupIMBotOwner(t *testing.T) (r *gin.Engine, chan1, pendingChat, proj1, proj2, proj3 int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openMCPPermTestDB(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)

	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','member')`)
	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (2,'u2','x','member')`)
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
			if n, err := strconv.ParseInt(uid, 10, 64); err == nil {
				c.Set("auth_user_id", n)
			}
		}
		c.Next()
	})
	g.GET("/imbot/bots", handler.ListBots)
	g.POST("/imbot/bots", handler.CreateBot)
	g.POST("/imbot/bots/:cid/test", handler.TestBot)
	g.DELETE("/imbot/bots/:cid", handler.DeleteBot)
	g.GET("/imbot/pending-chats", handler.ListPendingChatsOwner)
	g.GET("/imbot/chats", handler.ListActiveChatsOwner)
	g.POST("/imbot/chats/:chatid/approve", handler.ApproveChatOwner)
	g.POST("/imbot/chats/:chatid/reassign", handler.ReassignChatOwner)
	g.PATCH("/imbot/chats/:chatid", handler.PatchChatOwner)
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

// setupIMBotOrgScope seeds the role matrix the settings page must respect:
//
//	user 10 — global admin (users.role='admin'), member of nothing
//	user 11 — org 50 'owner'   → administers org 50
//	user 12 — org 50 'member'  → plain member, administers nothing
//	user 13 — unrelated user with a personal bot, to prove a global admin sees it
//
// Bots: channel A under org 50, channel B under user 13's personal space.
func setupIMBotOrgScope(t *testing.T) (r *gin.Engine, orgChan, personalChan, orgProj int64) {
	t.Helper()
	r, _, orgChan, personalChan, orgProj = setupIMBotOrgScopeFull(t)
	return
}

// setupIMBotOrgScopeWithSvc is the same scaffold, additionally exposing the service
// so a test can seed chats directly instead of driving the approve endpoint.
func setupIMBotOrgScopeWithSvc(t *testing.T) (r *gin.Engine, svc *service.IMBotService, orgChan, orgProj int64) {
	t.Helper()
	r, svc, orgChan, _, orgProj = setupIMBotOrgScopeFull(t)
	return
}

func setupIMBotOrgScopeFull(t *testing.T) (r *gin.Engine, svc *service.IMBotService, orgChan, personalChan, orgProj int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openMCPPermTestDB(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)

	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (10,'gadmin','x','admin')`)
	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (11,'orgowner','x','member')`)
	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (12,'orgmember','x','member')`)
	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (13,'other','x','member')`)
	mustExec(t, db, `INSERT INTO organizations (id, slug, name, created_by) VALUES (50,'o50','Org 50',11)`)
	mustExec(t, db, `INSERT INTO org_members (org_id, user_id, role) VALUES (50,11,'owner')`)
	mustExec(t, db, `INSERT INTO org_members (org_id, user_id, role) VALUES (50,12,'member')`)
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (60,'orgproj','org',50)`)
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (61,'orgproj2','org',50)`)
	orgProj = 60

	kr, err := crypto.LoadOrCreate(t.TempDir() + "/kr")
	require.NoError(t, err)
	adapters := map[imbot.ChannelType]imbot.ChannelAdapter{imbot.ChannelLark: &imbotMCPStubAdapter{}}
	authz := service.NewAuthz(q, db)
	svc = service.NewIMBotService(q, db, kr, authz, adapters)

	chOrg, err := svc.CreateChannel(context.Background(), service.OwnerRef{Type: "org", ID: 50},
		service.CreateChannelInput{ChannelType: "lark", Name: "orgbot",
			Credential: map[string]any{"app_id": "org", "app_secret": "s"}})
	require.NoError(t, err)
	orgChan = chOrg.ID

	chPersonal, err := svc.CreateChannel(context.Background(), service.OwnerRef{Type: "user", ID: 13},
		service.CreateChannelInput{ChannelType: "lark", Name: "personalbot",
			Credential: map[string]any{"app_id": "personal", "app_secret": "s"}})
	require.NoError(t, err)
	personalChan = chPersonal.ID

	handler := api.NewIMBotHandler(svc, authz, db)
	r = gin.New()
	g := r.Group("/api")
	g.Use(func(c *gin.Context) {
		if uid := c.GetHeader("X-Test-Uid"); uid != "" {
			if n, err := strconv.ParseInt(uid, 10, 64); err == nil {
				c.Set("auth_user_id", n)
			}
		}
		c.Next()
	})
	g.GET("/imbot/bots", handler.ListBots)
	g.PATCH("/imbot/chats/:chatid", handler.PatchChatOwner)
	return
}

// botNames extracts the bot names from a ListBots response for order-insensitive
// set assertions.
func botNames(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var resp struct {
		Items []struct {
			Name  string `json:"name"`
			Owner struct {
				Type string `json:"type"`
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"owner"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
	out := make([]string, 0, len(resp.Items))
	for _, it := range resp.Items {
		out = append(out, it.Name)
	}
	return out
}

// TestIMBotOwner_ListBots_OrgAdminSeesAdministeredOrgs is the regression for the
// reported bug: a bot created through a wizard run in an org-owned project lives
// under owner org:<id>, so scoping the settings page to the caller's personal
// space alone rendered an empty list. An org owner/admin must see it.
func TestIMBotOwner_ListBots_OrgAdminSeesAdministeredOrgs(t *testing.T) {
	r, _, _, _ := setupIMBotOrgScope(t)

	w := ownerReq(t, r, http.MethodGet, "/api/imbot/bots", "11", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.ElementsMatch(t, []string{"orgbot"}, botNames(t, w),
		"org owner must see the org's bot, and not other users' personal bots")
}

// A plain org member administers nothing, so the org's bot — whose credential is
// an org-level secret — stays invisible to them.
func TestIMBotOwner_ListBots_PlainMemberSeesOnlyPersonal(t *testing.T) {
	r, _, _, _ := setupIMBotOrgScope(t)

	w := ownerReq(t, r, http.MethodGet, "/api/imbot/bots", "12", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, botNames(t, w), "plain member must not see the org's bot")
}

func TestIMBotOwner_ListBots_GlobalAdminSeesAllOwners(t *testing.T) {
	r, _, _, _ := setupIMBotOrgScope(t)

	w := ownerReq(t, r, http.MethodGet, "/api/imbot/bots", "10", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.ElementsMatch(t, []string{"orgbot", "personalbot"}, botNames(t, w),
		"global admin sees every owner's bots, including other users' personal ones")
}

// Each bot carries its owner so the UI can group by "who owns this".
func TestIMBotOwner_ListBots_StampsOwner(t *testing.T) {
	r, _, _, _ := setupIMBotOrgScope(t)

	w := ownerReq(t, r, http.MethodGet, "/api/imbot/bots", "11", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Items []struct {
			Owner struct {
				Type string `json:"type"`
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"owner"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Items, 1)
	assert.Equal(t, "org", resp.Items[0].Owner.Type)
	assert.EqualValues(t, 50, resp.Items[0].Owner.ID)
	assert.Equal(t, "Org 50", resp.Items[0].Owner.Name, "owner name is resolved for grouping labels")
}

// An explicit ?owner= keeps the historical single-owner behaviour, so an org
// detail view can still scope to exactly one owner instead of the role-based set.
func TestIMBotOwner_ListBots_ExplicitOwnerFilterStillScopes(t *testing.T) {
	r, _, _, _ := setupIMBotOrgScope(t)

	// Global admin narrowing to org 50 sees only that org's bot, not the personal one.
	w := ownerReq(t, r, http.MethodGet, "/api/imbot/bots?owner=org:50", "10", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.ElementsMatch(t, []string{"orgbot"}, botNames(t, w))
}

// PatchChatOwner derives authorization from the chat's own bot, so a plain member
// cannot retarget an org bot's routing even though the route carries no project.
func TestIMBotOwner_PatchChat_AuthorizedByBotOwner(t *testing.T) {
	r, svc, orgChan, orgProj := setupIMBotOrgScopeWithSvc(t)

	// Seed an approved chat on the org bot, routed to the org's project.
	chat, err := svc.AddChat(context.Background(), service.OwnerRef{Type: "org", ID: 50}, orgChan, "oc_org", "OrgChat")
	require.NoError(t, err)
	_, err = svc.ApproveChatToProject(context.Background(), chat.ID, orgProj, 11)
	require.NoError(t, err)

	body := `{"bind_mode":"workspace","pinned_issue_id":null}`

	// Plain org member (12) administers nothing → rejected.
	w := ownerReq(t, r, http.MethodPatch, "/api/imbot/chats/"+itoa(chat.ID), "12", body)
	assert.True(t, w.Code == http.StatusForbidden || w.Code == http.StatusNotFound,
		"plain member must not patch an org bot's chat, got %d body=%s", w.Code, w.Body.String())

	// Org owner (11) may.
	w = ownerReq(t, r, http.MethodPatch, "/api/imbot/chats/"+itoa(chat.ID), "11", body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "workspace", resp["bind_mode"])

	// Global admin (10) may too, without being an org member.
	w = ownerReq(t, r, http.MethodPatch, "/api/imbot/chats/"+itoa(chat.ID), "10",
		`{"bind_mode":"project","pinned_issue_id":null}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "project", resp["bind_mode"])
}
