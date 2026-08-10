// imbot_mcp_test.go — server-side handler tests for the five IM Bot MCP
// onboarding endpoints (Epic #555 T4). Tests mirror the pattern in
// mcp_issue_write_test.go: real MCPTokenAuth gate, in-memory SQLite, no mocks.
package api_test

import (
	"context"
	"encoding/json"
	"net/http"
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

// setupIMBotMCP wires the five imbot MCP routes with the real MCPTokenAuth gate.
//
// Seed state:
//   - user 1 owns project 1; workspace 1 (session user = 1) → token1 (authorized)
//   - user 2 owns project 2; workspace 2 (session user = 2) → token2 (cross-tenant)
//   - project 1 has one channel (id=1, type=lark, stream, status=active, with credential)
//   - project 1 has one pending chat (id=1, channel=1) and one approved chat (id=2, channel=1)
//
// Returns: engine, token for user-1 workspace (authorized), token for user-2 workspace (forbidden),
// project1ID, channel1ID, pendingChatID.
func setupIMBotMCP(t *testing.T) (r *gin.Engine, tok1, tok2 string, proj1, chan1, pendingChat int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openMCPPermTestDB(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)
	ctx := context.Background()

	// users
	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','admin')`)
	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (2,'u2','x','admin')`)

	// project 1 (owner user 1)
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1, 'p1', 'user', 1)`)
	proj1 = 1

	// project 2 (owner user 2)
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (2, 'p2', 'user', 2)`)

	// keyring
	kr, err := crypto.LoadOrCreate(t.TempDir() + "/kr")
	require.NoError(t, err)

	// minimal credential blob (encrypted)
	cred := map[string]any{"app_id": "testapp", "app_secret": "testsecret"}
	credJSON, _ := json.Marshal(cred)
	ct, err := kr.Encrypt(credJSON)
	require.NoError(t, err)

	// channel 1 owned by user 1 (lark stream, credential set). Owner-level bot; the
	// project's owner (user 1) drives approval/routing/status owner checks.
	mustExec(t, db,
		`INSERT INTO im_bot_channels (id, owner_type, owner_id, channel_type, name, connection_mode, status, credential_enc, webhook_secret, created_at, updated_at)
		 VALUES (1, 'user', 1, 'lark', 'test-bot', 'stream', 'active', ?, '', datetime('now'), datetime('now'))`,
		string(ct))
	chan1 = 1

	// pending chat under channel 1
	mustExec(t, db,
		`INSERT INTO im_bot_chats (id, channel_id, chat_ext_id, chat_name, bind_mode, status, created_at, updated_at)
		 VALUES (1, 1, 'ext-pending', 'PendingGroup', 'project', 'pending', datetime('now'), datetime('now'))`)
	pendingChat = 1

	// active (approved) chat under channel 1 — schema CHECK: status IN ('pending','active','disabled')
	mustExec(t, db,
		`INSERT INTO im_bot_chats (id, channel_id, chat_ext_id, chat_name, bind_mode, status, paired_by, created_at, updated_at)
		 VALUES (2, 1, 'ext-active', 'ActiveGroup', 'project', 'active', 1, datetime('now'), datetime('now'))`)

	// workspace 1 for user 1
	ws1, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		Name: "ws1", Path: "/tmp/ws1", Status: "created", OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	mustExec(t, db, `UPDATE workspaces SET current_session_user_id=1 WHERE id=?`, ws1.ID)

	// workspace 2 for user 2
	ws2, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		Name: "ws2", Path: "/tmp/ws2", Status: "created", OwnerType: "user", OwnerID: 2,
	})
	require.NoError(t, err)
	mustExec(t, db, `UPDATE workspaces SET current_session_user_id=2 WHERE id=?`, ws2.ID)

	// no-op lark adapter for tests (avoids real API calls)
	adapters := map[imbot.ChannelType]imbot.ChannelAdapter{
		imbot.ChannelLark: &imbotMCPStubAdapter{},
	}
	authz := service.NewAuthz(q, db)
	svc := service.NewIMBotService(q, db, kr, authz, adapters)
	handler := api.NewIMBotHandler(svc, authz, db)

	mcpSess := service.NewMCPSessionService(q)

	r = gin.New()
	g := r.Group("/mcp")
	g.Use(api.LocalhostOnly())
	g.Use(api.MCPTokenAuth(mcpSess, store.New(db)))

	// register the five MCP imbot routes (mirroring router.go)
	g.POST("/projects/:id/imbot/onboarding-token", handler.MCPRequestCredentialLink)
	g.POST("/projects/:id/imbot/channels/:cid/test", handler.MCPTestChannel)
	g.GET("/projects/:id/imbot/pending-chats", handler.MCPListPendingChats)
	g.POST("/projects/:id/imbot/chats/:chatid/approve", handler.MCPApproveChat)
	g.GET("/projects/:id/imbot/channels/:cid/status", handler.MCPChannelStatus)

	tok1 = mintMCPToken(t, db, ws1.ID)
	tok2 = mintMCPToken(t, db, ws2.ID)
	return
}

// imbotMCPStubAdapter is a no-op lark adapter for the MCP handler tests.
// TestChannel will fail because it uses CredentialVerifier, which this stub
// does not implement — that's expected for the connectivity test path.
type imbotMCPStubAdapter struct{}

func (a *imbotMCPStubAdapter) Type() imbot.ChannelType { return imbot.ChannelLark }
func (a *imbotMCPStubAdapter) Connect(_ context.Context, _ imbot.Credential, _ imbot.InboundHandler) error {
	return nil
}
func (a *imbotMCPStubAdapter) Push(_ context.Context, _ imbot.Credential, _ imbot.OutboundMessage) error {
	return nil
}
func (a *imbotMCPStubAdapter) VerifyWebhook(_ *http.Request, _ imbot.Credential) (imbot.InboundEvent, error) {
	return imbot.InboundEvent{}, nil
}
func (a *imbotMCPStubAdapter) Challenge(_ *http.Request) ([]byte, bool) { return nil, false }

// ─── authz isolation ──────────────────────────────────────────────────────────

// TestMCPIMBot_AuthzIsolation verifies that a session scoped to user 2 cannot
// access project 1 resources through any of the five MCP imbot endpoints.
func TestMCPIMBot_AuthzIsolation(t *testing.T) {
	r, _, tok2, proj1, chan1, chat1 := setupIMBotMCP(t)
	pid := itoa(proj1)
	cid := itoa(chan1)
	chatid := itoa(chat1)

	// credential link — cross-tenant must be forbidden/not-found
	w := mcpPOST(t, r, "/mcp/projects/"+pid+"/imbot/onboarding-token", tok2,
		`{"platform":"lark","name":"bot","connection_mode":"stream"}`)
	assert.True(t, w.Code == http.StatusForbidden || w.Code == http.StatusNotFound,
		"cross-tenant onboarding-token must be 403/404, got %d body=%s", w.Code, w.Body.String())

	// test channel
	w = mcpPOST(t, r, "/mcp/projects/"+pid+"/imbot/channels/"+cid+"/test", tok2, ``)
	assert.True(t, w.Code == http.StatusForbidden || w.Code == http.StatusNotFound,
		"cross-tenant test must be 403/404, got %d body=%s", w.Code, w.Body.String())

	// list pending chats
	w = mcpGET(t, r, "/mcp/projects/"+pid+"/imbot/pending-chats", tok2)
	assert.True(t, w.Code == http.StatusForbidden || w.Code == http.StatusNotFound,
		"cross-tenant pending-chats must be 403/404, got %d body=%s", w.Code, w.Body.String())

	// approve chat
	w = mcpPOST(t, r, "/mcp/projects/"+pid+"/imbot/chats/"+chatid+"/approve", tok2, ``)
	assert.True(t, w.Code == http.StatusForbidden || w.Code == http.StatusNotFound,
		"cross-tenant approve must be 403/404, got %d body=%s", w.Code, w.Body.String())

	// channel status
	w = mcpGET(t, r, "/mcp/projects/"+pid+"/imbot/channels/"+cid+"/status", tok2)
	assert.True(t, w.Code == http.StatusForbidden || w.Code == http.StatusNotFound,
		"cross-tenant status must be 403/404, got %d body=%s", w.Code, w.Body.String())
}

// ─── imbot_request_credential_link ────────────────────────────────────────────

// TestMCPIMBot_RequestCredentialLink_ReturnsURL verifies that the handler returns
// a URL containing the onboarding path and does NOT include the raw token as a
// separate field. The token is embedded only inside the URL.
func TestMCPIMBot_RequestCredentialLink_ReturnsURL(t *testing.T) {
	r, tok1, _, proj1, _, _ := setupIMBotMCP(t)
	pid := itoa(proj1)

	w := mcpPOST(t, r, "/mcp/projects/"+pid+"/imbot/onboarding-token", tok1,
		`{"platform":"lark","name":"飞书测试机器人","connection_mode":"stream"}`)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	// must contain "url" with the onboarding path
	urlVal, ok := resp["url"].(string)
	require.True(t, ok, "url field missing or not string: %s", w.Body.String())
	assert.Contains(t, urlVal, "/imbot/onboarding/", "url must contain onboarding path")

	// must contain expires_in_seconds
	expiresVal, ok := resp["expires_in_seconds"]
	require.True(t, ok, "expires_in_seconds missing")
	assert.EqualValues(t, 900, expiresVal)

	// the raw token must NOT appear as a separate top-level field named "token"
	_, hasToken := resp["token"]
	assert.False(t, hasToken, "raw token must not be a separate top-level field")
}

// TestMCPIMBot_RequestCredentialLink_TokenEmbeddedInURL verifies the raw token
// embedded in the URL is valid and can be used to submit a credential.
func TestMCPIMBot_RequestCredentialLink_TokenEmbeddedInURL(t *testing.T) {
	r, tok1, _, proj1, _, _ := setupIMBotMCP(t)
	pid := itoa(proj1)

	w := mcpPOST(t, r, "/mcp/projects/"+pid+"/imbot/onboarding-token", tok1,
		`{"platform":"lark","name":"bot2"}`)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	urlVal := resp["url"].(string)

	// The URL is a root-relative path (/imbot/onboarding/<token>); locate the path
	// prefix and take everything after it as the raw token.
	const prefix = "/imbot/onboarding/"
	idx := strings.Index(urlVal, prefix)
	require.GreaterOrEqual(t, idx, 0, "url must contain onboarding path: %s", urlVal)
	rawToken := urlVal[idx+len(prefix):]
	assert.NotEmpty(t, rawToken, "raw token embedded in URL must not be empty")

	// link_markdown must be a ready-to-paste clickable markdown link wrapping url.
	linkMD, ok := resp["link_markdown"].(string)
	require.True(t, ok, "link_markdown field missing: %s", w.Body.String())
	assert.Contains(t, linkMD, "("+urlVal+")", "link_markdown must wrap the absolute url")
}

// ─── imbot_list_pending_chats ─────────────────────────────────────────────────

// TestMCPIMBot_ListPendingChats_OnlyPending verifies that only pending chats
// are returned (the approved chat seeded in setup is excluded).
func TestMCPIMBot_ListPendingChats_OnlyPending(t *testing.T) {
	r, tok1, _, proj1, _, _ := setupIMBotMCP(t)
	pid := itoa(proj1)

	w := mcpGET(t, r, "/mcp/projects/"+pid+"/imbot/pending-chats", tok1)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	items, ok := resp["items"].([]any)
	require.True(t, ok, "items must be an array")
	assert.Len(t, items, 1, "only one pending chat expected")

	first := items[0].(map[string]any)
	assert.Equal(t, "pending", first["status"])
	assert.Equal(t, "ext-pending", first["chat_ext_id"])
}

// TestMCPIMBot_ListPendingChats_ChannelFilter verifies that the channel_id
// query param filters to the given channel (all pending chats are under chan1 here).
func TestMCPIMBot_ListPendingChats_ChannelFilter(t *testing.T) {
	r, tok1, _, proj1, chan1, _ := setupIMBotMCP(t)
	pid := itoa(proj1)
	cid := itoa(chan1)

	// Filter to the correct channel — should return the one pending chat.
	w := mcpGET(t, r, "/mcp/projects/"+pid+"/imbot/pending-chats?channel_id="+cid, tok1)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	items := resp["items"].([]any)
	assert.Len(t, items, 1)

	// Filter to a non-existent channel — should return empty.
	w = mcpGET(t, r, "/mcp/projects/"+pid+"/imbot/pending-chats?channel_id=9999", tok1)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	items = resp["items"].([]any)
	assert.Len(t, items, 0, "unknown channel should return empty pending list")
}

// ─── imbot_approve_chat ───────────────────────────────────────────────────────

// TestMCPIMBot_ApproveChat verifies that the handler approves the pending chat
// and returns the DTO with status=approved.
func TestMCPIMBot_ApproveChat(t *testing.T) {
	r, tok1, _, proj1, _, pendingChat := setupIMBotMCP(t)
	pid := itoa(proj1)
	chatid := itoa(pendingChat)

	// tok1 was minted for workspace 1, whose current_session_user_id=1.
	const callerUID = int64(1)

	w := mcpPOST(t, r, "/mcp/projects/"+pid+"/imbot/chats/"+chatid+"/approve", tok1, ``)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// ApproveIMBotChat sets status='active' (schema CHECK: pending|active|disabled)
	assert.Equal(t, "active", resp["status"])
	assert.EqualValues(t, pendingChat, resp["id"])

	// paired_by must be set to the MCP caller's uid (callerUID).
	pairedBy, ok := resp["paired_by"]
	require.True(t, ok, "paired_by must be present in approve response")
	assert.EqualValues(t, callerUID, pairedBy, "paired_by must equal the MCP caller uid")
}

// TestMCPIMBot_ApproveChat_NotFound verifies that approving a non-existent chat
// returns 404.
func TestMCPIMBot_ApproveChat_NotFound(t *testing.T) {
	r, tok1, _, proj1, _, _ := setupIMBotMCP(t)
	pid := itoa(proj1)

	w := mcpPOST(t, r, "/mcp/projects/"+pid+"/imbot/chats/9999/approve", tok1, ``)
	assert.Equal(t, http.StatusNotFound, w.Code, "body=%s", w.Body.String())
}

// ─── imbot_channel_status ─────────────────────────────────────────────────────

// TestMCPIMBot_ChannelStatus verifies that the handler returns the channel's
// status fields.
func TestMCPIMBot_ChannelStatus(t *testing.T) {
	r, tok1, _, proj1, chan1, _ := setupIMBotMCP(t)
	pid := itoa(proj1)
	cid := itoa(chan1)

	w := mcpGET(t, r, "/mcp/projects/"+pid+"/imbot/channels/"+cid+"/status", tok1)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "lark", resp["channel_type"])
	assert.Equal(t, "stream", resp["connection_mode"])
	assert.Equal(t, "active", resp["status"])
	assert.Equal(t, true, resp["has_credential"])
	assert.Equal(t, "test-bot", resp["name"])
}

// TestMCPIMBot_ChannelStatus_NotFound verifies that requesting a non-existent
// channel returns 404.
func TestMCPIMBot_ChannelStatus_NotFound(t *testing.T) {
	r, tok1, _, proj1, _, _ := setupIMBotMCP(t)
	pid := itoa(proj1)

	w := mcpGET(t, r, "/mcp/projects/"+pid+"/imbot/channels/9999/status", tok1)
	assert.Equal(t, http.StatusNotFound, w.Code, "body=%s", w.Body.String())
}

// ─── imbot_test_channel ───────────────────────────────────────────────────────

// TestMCPIMBot_TestChannel_NoVerifier verifies that TestChannel returns a bad-request
// error when the adapter does not implement CredentialVerifier (our stub adapter).
// This confirms the handler plumbs through properly even on error paths.
func TestMCPIMBot_TestChannel_NoVerifier(t *testing.T) {
	r, tok1, _, proj1, chan1, _ := setupIMBotMCP(t)
	pid := itoa(proj1)
	cid := itoa(chan1)

	// The stub adapter does not implement CredentialVerifier, so TestChannel
	// returns a "does not support connectivity test" error → 400.
	w := mcpPOST(t, r, "/mcp/projects/"+pid+"/imbot/channels/"+cid+"/test", tok1, ``)
	// Expect 400 (bad request) because the adapter reports "does not support test".
	assert.Equal(t, http.StatusBadRequest, w.Code,
		"stub adapter should cause 400 on test, body=%s", w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["ok"])
}

// TestMCPIMBot_TestChannel_NotFound verifies that testing a non-existent channel
// returns 404.
func TestMCPIMBot_TestChannel_NotFound(t *testing.T) {
	r, tok1, _, proj1, _, _ := setupIMBotMCP(t)
	pid := itoa(proj1)

	w := mcpPOST(t, r, "/mcp/projects/"+pid+"/imbot/channels/9999/test", tok1, ``)
	assert.Equal(t, http.StatusNotFound, w.Code,
		"non-existent channel must be 404, body=%s", w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["ok"])
}

