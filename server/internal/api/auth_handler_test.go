package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/auth"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func setupAuthTestDB(t *testing.T) (*store.Queries, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return store.New(db), db
}

func setupAuthRouter(t *testing.T) (*gin.Engine, *service.AuthService) {
	gin.SetMode(gin.TestMode)
	q, db := setupAuthTestDB(t)
	authSvc := service.NewAuthService(q, db, "test-secret", "15m", "168h")
	authSvc.SetPolicy(auth.Policy{MinLength: 6}) // permissive policy for legacy fixtures
	handler := NewAuthHandler(authSvc)

	r := gin.New()
	auth := r.Group("/api/auth")
	{
		auth.POST("/login", handler.Login)
		auth.POST("/refresh", handler.Refresh)
		auth.POST("/logout", handler.Logout)
		auth.POST("/users", handler.CreateUser)
		auth.PATCH("/users/:id", handler.UpdateUser)
		auth.POST("/users/:id/password", handler.ResetPassword)
		auth.DELETE("/users/:id", handler.DeleteUserByID)
	}
	return r, authSvc
}

func TestAuthHandler_Login(t *testing.T) {
	r, authSvc := setupAuthRouter(t)
	_, err := authSvc.CreateUser(context.Background(), "admin", "pass123", "Admin", "admin")
	require.NoError(t, err)

	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "pass123"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/auth/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NotEmpty(t, resp["access_token"])
	assert.NotEmpty(t, resp["refresh_token"])
}

func TestAuthHandler_Login_InvalidCredentials(t *testing.T) {
	r, _ := setupAuthRouter(t)

	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "wrong"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/auth/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, 401, w.Code)
}

func TestAuthHandler_Refresh(t *testing.T) {
	r, authSvc := setupAuthRouter(t)
	authSvc.CreateUser(context.Background(), "admin", "pass123", "Admin", "admin")

	loginBody, _ := json.Marshal(map[string]string{"username": "admin", "password": "pass123"})
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest("POST", "/api/auth/login", bytes.NewBuffer(loginBody))
	req1.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w1, req1)

	var loginResp map[string]any
	json.Unmarshal(w1.Body.Bytes(), &loginResp)

	refreshBody, _ := json.Marshal(map[string]string{"refresh_token": loginResp["refresh_token"].(string)})
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/api/auth/refresh", bytes.NewBuffer(refreshBody))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)

	assert.Equal(t, 200, w2.Code)
	var refreshResp map[string]any
	json.Unmarshal(w2.Body.Bytes(), &refreshResp)
	assert.NotEmpty(t, refreshResp["access_token"])
}

func TestAuthHandler_CreateUser_HappyPath(t *testing.T) {
	r, _ := setupAuthRouter(t)

	body, _ := json.Marshal(map[string]string{
		"username": "alice", "password": "pass123",
		"display_name": "Alice", "role": "member",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/auth/users", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "alice", resp["username"])
	assert.Equal(t, "member", resp["role"])
	assert.Nil(t, resp["password_hash"]) // never leak
}

func TestAuthHandler_CreateUser_DuplicateUsername(t *testing.T) {
	r, svc := setupAuthRouter(t)
	_, _ = svc.CreateUser(context.Background(), "alice", "pass123", "Alice", "member")

	body, _ := json.Marshal(map[string]string{
		"username": "alice", "password": "pass456",
		"display_name": "Alice2", "role": "member",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/auth/users", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, 400, w.Code)
}

func TestAuthHandler_CreateUser_ShortPassword(t *testing.T) {
	r, _ := setupAuthRouter(t)
	body, _ := json.Marshal(map[string]string{
		"username": "x", "password": "abc",
		"display_name": "X", "role": "member",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/auth/users", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)
}

func TestAuthHandler_CreateUser_BadRole(t *testing.T) {
	r, _ := setupAuthRouter(t)
	body, _ := json.Marshal(map[string]string{
		"username": "x", "password": "pass123",
		"display_name": "X", "role": "superadmin",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/auth/users", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, 400, w.Code)
}

func TestAuthHandler_UpdateUser_HappyPath(t *testing.T) {
	r, svc := setupAuthRouter(t)
	a1, _ := svc.CreateUser(context.Background(), "a1", "pass123", "A1", "admin")
	u1, _ := svc.CreateUser(context.Background(), "u1", "pass123", "U1", "member")
	_ = a1

	body, _ := json.Marshal(map[string]any{"display_name": "U1 Renamed", "role": "admin"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH",
		"/api/auth/users/"+strconv.FormatInt(u1.ID, 10),
		bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Caller-ID", strconv.FormatInt(a1.ID, 10))
	r.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "U1 Renamed", resp["display_name"])
	assert.Equal(t, "admin", resp["role"])
}

func TestAuthHandler_UpdateUser_DemoteSelfRejected(t *testing.T) {
	r, svc := setupAuthRouter(t)
	a1, _ := svc.CreateUser(context.Background(), "a1", "pass123", "A1", "admin")
	_, _ = svc.CreateUser(context.Background(), "a2", "pass123", "A2", "admin")

	body, _ := json.Marshal(map[string]any{"role": "member"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH",
		"/api/auth/users/"+strconv.FormatInt(a1.ID, 10),
		bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Caller-ID", strconv.FormatInt(a1.ID, 10))
	r.ServeHTTP(w, req)

	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "demote")
}

func TestAuthHandler_UpdateUser_LastAdminRejected(t *testing.T) {
	r, svc := setupAuthRouter(t)
	a1, _ := svc.CreateUser(context.Background(), "a1", "pass123", "A1", "admin")
	other, _ := svc.CreateUser(context.Background(), "u1", "pass123", "U1", "member")

	body, _ := json.Marshal(map[string]any{"role": "member"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH",
		"/api/auth/users/"+strconv.FormatInt(a1.ID, 10),
		bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Caller-ID", strconv.FormatInt(other.ID, 10))
	r.ServeHTTP(w, req)

	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "last admin")
}

func TestAuthHandler_ResetPassword(t *testing.T) {
	r, svc := setupAuthRouter(t)
	u, _ := svc.CreateUser(context.Background(), "bob", "oldpass", "Bob", "member")

	body, _ := json.Marshal(map[string]string{"password": "newpass"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST",
		"/api/auth/users/"+strconv.FormatInt(u.ID, 10)+"/password",
		bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	_, err := svc.Login(context.Background(), "bob", "newpass")
	assert.NoError(t, err)
	_, err = svc.Login(context.Background(), "bob", "oldpass")
	assert.Error(t, err, "old password must no longer authenticate")
}

func TestAuthHandler_DeleteUser_LastAdminBlocked(t *testing.T) {
	r, svc := setupAuthRouter(t)
	a1, _ := svc.CreateUser(context.Background(), "a1", "pass123", "A1", "admin")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE",
		"/api/auth/users/"+strconv.FormatInt(a1.ID, 10), nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "last admin")
}

func TestAuthHandler_UpdateUser_NotFound(t *testing.T) {
	r, _ := setupAuthRouter(t)
	body, _ := json.Marshal(map[string]any{"display_name": "X"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/api/auth/users/9999", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, 404, w.Code)
}

func TestAuthHandler_CreateUser_DuplicateUsername_FriendlyMessage(t *testing.T) {
	r, svc := setupAuthRouter(t)
	_, _ = svc.CreateUser(context.Background(), "alice", "pass123", "Alice", "member")

	body, _ := json.Marshal(map[string]string{
		"username": "alice", "password": "pass456",
		"display_name": "Alice2", "role": "member",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/auth/users", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "username already exists")
}
