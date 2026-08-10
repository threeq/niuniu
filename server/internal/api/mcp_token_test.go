package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// callMCPProbe wires MCPTokenAuth onto a /mcp/probe route, mints a token for
// the given workspace, and returns the HTTP status plus the auth_user_id the
// middleware resolved into the Gin context (0 if it left the context clean).
func callMCPProbe(t *testing.T, db *sql.DB, workspaceID int64) (int, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	mcpSess := service.NewMCPSessionService(store.New(db))

	r := gin.New()
	g := r.Group("/mcp")
	g.Use(api.LocalhostOnly())
	g.Use(api.MCPTokenAuth(mcpSess, store.New(db)))
	g.GET("/probe", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"uid": c.GetInt64("auth_user_id")})
	})

	token := mintMCPToken(t, db, workspaceID)
	req := httptest.NewRequest(http.MethodGet, "/mcp/probe", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "127.0.0.1:9999"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp struct {
		UID int64 `json:"uid"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp.UID
}

// TestMCPTokenAuth_PrefersActiveSessionUser confirms the existing behavior:
// when current_session_user_id is set, it wins over the owner fallback.
func TestMCPTokenAuth_PrefersActiveSessionUser(t *testing.T) {
	db := openMCPPermTestDB(t)
	q := store.New(db)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "ws", Path: "/tmp/ws", Status: "created", OwnerType: "user", OwnerID: 7,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	mustExec(t, db, `UPDATE workspaces SET current_session_user_id=3, created_by=9 WHERE id=?`, ws.ID)

	code, uid := callMCPProbe(t, db, ws.ID)
	if code != http.StatusOK || uid != 3 {
		t.Fatalf("want code=200 uid=3 (active session user wins), got code=%d uid=%d", code, uid)
	}
}

// TestMCPTokenAuth_FallsBackToOwnerForPersonalWorkspace is the regression test
// for the Tapd 401 bug: an autonomously-started agent (no current_session_user_id)
// in a personal (owner_type='user') workspace must resolve to the owner so that
// credential-scoped MCP tools (/mcp/external-*, /mcp/data-proxy, /mcp/dashboards)
// can find the owner's credentials instead of 401-ing on auth_user_id=0.
func TestMCPTokenAuth_FallsBackToOwnerForPersonalWorkspace(t *testing.T) {
	db := openMCPPermTestDB(t)
	q := store.New(db)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "ws", Path: "/tmp/ws", Status: "created", OwnerType: "user", OwnerID: 7,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	// current_session_user_id deliberately left NULL (autonomous start).

	code, uid := callMCPProbe(t, db, ws.ID)
	if code != http.StatusOK || uid != 7 {
		t.Fatalf("want code=200 uid=7 (owner fallback), got code=%d uid=%d", code, uid)
	}
}

// TestMCPTokenAuth_OrgFallsBackToCreator confirms that for an org-owned
// workspace (owner_id is an org id, not a user) the fallback uses created_by
// rather than mis-using owner_id as a user id.
func TestMCPTokenAuth_OrgFallsBackToCreator(t *testing.T) {
	db := openMCPPermTestDB(t)
	q := store.New(db)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "ws", Path: "/tmp/ws", Status: "created", OwnerType: "org", OwnerID: 42,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	mustExec(t, db, `UPDATE workspaces SET created_by=9 WHERE id=?`, ws.ID)

	code, uid := callMCPProbe(t, db, ws.ID)
	if code != http.StatusOK || uid != 9 {
		t.Fatalf("want code=200 uid=9 (creator fallback for org ws), got code=%d uid=%d", code, uid)
	}
}

// TestMCPTokenAuth_OrgFallsBackToOwnerMember covers the last-resort fallback:
// an org-owned workspace with no session user AND no created_by (legacy /
// no-issue workspace) resolves to the org's owner member, so credential-scoped
// MCP tools still get an identity instead of 401-ing on auth_user_id=0. The
// owner must win over an admin regardless of insert order.
func TestMCPTokenAuth_OrgFallsBackToOwnerMember(t *testing.T) {
	db := openMCPPermTestDB(t)
	q := store.New(db)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "ws", Path: "/tmp/ws", Status: "created", OwnerType: "org", OwnerID: 42,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	// created_by deliberately left NULL. Admin seeded first to prove role
	// priority beats insert order.
	mustExec(t, db, `INSERT INTO org_members (org_id, user_id, role) VALUES (42, 5, 'admin')`)
	mustExec(t, db, `INSERT INTO org_members (org_id, user_id, role) VALUES (42, 8, 'owner')`)

	code, uid := callMCPProbe(t, db, ws.ID)
	if code != http.StatusOK || uid != 8 {
		t.Fatalf("want code=200 uid=8 (org owner member fallback), got code=%d uid=%d", code, uid)
	}
}

// TestMCPTokenAuth_OrgCreatorBeatsMember locks the resolution precedence:
// when an org-owned workspace has BOTH created_by set AND org members, the
// workspace creator (step 3) wins over the org owner/admin fallback (step 4).
func TestMCPTokenAuth_OrgCreatorBeatsMember(t *testing.T) {
	db := openMCPPermTestDB(t)
	q := store.New(db)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "ws", Path: "/tmp/ws", Status: "created", OwnerType: "org", OwnerID: 42,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	mustExec(t, db, `UPDATE workspaces SET created_by=9 WHERE id=?`, ws.ID)
	mustExec(t, db, `INSERT INTO org_members (org_id, user_id, role) VALUES (42, 8, 'owner')`)

	code, uid := callMCPProbe(t, db, ws.ID)
	if code != http.StatusOK || uid != 9 {
		t.Fatalf("want code=200 uid=9 (creator beats org member), got code=%d uid=%d", code, uid)
	}
}

// TestMCPTokenAuth_OrgNoIdentityStaysZero confirms graceful degradation: an
// org-owned workspace with no session user, no created_by, and no members
// leaves auth_user_id=0 (there is genuinely no user to scope credentials to).
func TestMCPTokenAuth_OrgNoIdentityStaysZero(t *testing.T) {
	db := openMCPPermTestDB(t)
	q := store.New(db)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "ws", Path: "/tmp/ws", Status: "created", OwnerType: "org", OwnerID: 99,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	code, uid := callMCPProbe(t, db, ws.ID)
	if code != http.StatusOK || uid != 0 {
		t.Fatalf("want code=200 uid=0 (no resolvable identity), got code=%d uid=%d", code, uid)
	}
}
