package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func setupOrgHandlerDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	store.Migrate(db)
	t.Cleanup(func() { db.Close() })
	return db
}

// insertOrgHandlerUser inserts a user and returns their ID.
func insertOrgHandlerUser(t *testing.T, db *sql.DB, username, role string) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(
		`INSERT INTO users (username, password_hash, display_name, role) VALUES (?, 'x', ?, ?) RETURNING id`,
		username, username, role,
	).Scan(&id)
	if err != nil {
		_, err2 := db.Exec(
			`INSERT INTO users (username, password_hash, display_name, role) VALUES (?, 'x', ?, ?)`,
			username, username, role,
		)
		require.NoError(t, err2)
		require.NoError(t, db.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id))
	}
	return id
}

// setupOrgRouter wires up a gin engine with all org-related routes and returns
// the engine together with the db and the constructed OrgService.
func setupOrgRouter(t *testing.T) (*gin.Engine, *sql.DB, *service.OrgService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := setupOrgHandlerDB(t)
	q := store.New(db)
	authz := service.NewAuthz(q, db)
	svc := service.NewOrgService(q, db, authz)
	h := NewOrgHandler(svc)

	r := gin.New()
	orgs := r.Group("/api/orgs")
	{
		orgs.POST("", h.CreateOrg)
		orgs.GET("", h.ListOrgsForUser)
		orgs.GET("/:id", h.GetOrg)
		orgs.PATCH("/:id", h.UpdateOrg)
		orgs.DELETE("/:id", h.DeleteOrg)
		orgs.GET("/:id/members", h.ListMembers)
		orgs.POST("/:id/members", h.AddMember)
		orgs.PATCH("/:id/members/:user_id", h.UpdateMemberRole)
		orgs.DELETE("/:id/members/:user_id", h.RemoveMember)
		orgs.POST("/:id/transfer-ownership", h.TransferOwnership)
		orgs.GET("/:id/audit-log", h.ListAuditLog)
	}
	return r, db, svc
}

// injectCaller is a gin middleware that injects auth_user_id, auth_username, and
// auth_role into the context — simulating what the JWT middleware does in production.
func injectCaller(userID int64, username, role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("auth_user_id", userID)
		c.Set("auth_username", username)
		c.Set("auth_role", role)
		c.Next()
	}
}

// TestOrgHandlerTransferOwnership verifies the handler-level happy path for
// POST /api/orgs/:id/transfer-ownership.
func TestOrgHandlerTransferOwnership(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupOrgHandlerDB(t)
	q := store.New(db)
	authz := service.NewAuthz(q, db)
	svc := service.NewOrgService(q, db, authz)
	h := NewOrgHandler(svc)

	// Create two users: original owner (admin) and target (member).
	ownerID := insertOrgHandlerUser(t, db, "owner-handler", "admin")
	memberID := insertOrgHandlerUser(t, db, "member-handler", "member")

	// Create the org via the service so audit + membership rows are seeded correctly.
	org, err := svc.Create(t.Context(), ownerID, "owner-handler", "Handler Org", "handler-org", "test")
	require.NoError(t, err)

	// Add member as a plain member of the org.
	err = svc.AddMember(t.Context(), ownerID, "owner-handler", org.ID, memberID, "member")
	require.NoError(t, err)

	// Build a gin engine with the caller injected as the owner.
	r := gin.New()
	r.Use(injectCaller(ownerID, "owner-handler", "admin"))
	r.POST("/api/orgs/:id/transfer-ownership", h.TransferOwnership)

	body, _ := json.Marshal(map[string]int64{"target_user_id": memberID})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", fmt.Sprintf("/api/orgs/%d/transfer-ownership", org.ID), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	// Verify DB state: original owner is now admin; target is owner.
	var ownerRole, memberRole string
	require.NoError(t, db.QueryRow(
		`SELECT role FROM org_members WHERE org_id = ? AND user_id = ?`, org.ID, ownerID,
	).Scan(&ownerRole))
	require.NoError(t, db.QueryRow(
		`SELECT role FROM org_members WHERE org_id = ? AND user_id = ?`, org.ID, memberID,
	).Scan(&memberRole))

	assert.Equal(t, "admin", ownerRole, "original owner should be demoted to admin")
	assert.Equal(t, "owner", memberRole, "target should be promoted to owner")
}

// TestOrgHandlerTransferOwnership_Unauthenticated verifies the handler returns
// 401 when no caller info is present in the context.
func TestOrgHandlerTransferOwnership_Unauthenticated(t *testing.T) {
	_, _, svc := setupOrgRouter(t)
	h := NewOrgHandler(svc)

	r := gin.New()
	// No injectCaller middleware — simulates missing JWT.
	r.POST("/api/orgs/:id/transfer-ownership", h.TransferOwnership)

	body, _ := json.Marshal(map[string]int64{"target_user_id": 99})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/orgs/1/transfer-ownership", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
