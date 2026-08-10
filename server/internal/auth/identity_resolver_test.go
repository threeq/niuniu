package auth

import (
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

func openAuthTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatal(err)
	}
	store.Migrate(db)
	return db
}

func TestIdentityResolverSeedsLocalUserWhenAuthDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openAuthTestDB(t)
	defer db.Close()
	_, err := db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('local', 'x', 'admin')`)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Auth: config.AuthConfig{
		Enabled:    false,
		SingleUser: config.UserConfig{Username: "local"},
	}}

	r := gin.New()
	r.Use(IdentityResolver(cfg, db))
	r.GET("/ping", func(c *gin.Context) {
		uid, _ := c.Get("auth_user_id")
		c.JSON(200, gin.H{"uid": uid})
	})

	req := httptest.NewRequest("GET", "/ping", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"uid":1`) {
		t.Errorf("auth_user_id not 1 for seeded local user: %s", w.Body.String())
	}
}

func TestIdentityResolverSkipsMCPPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := openAuthTestDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO users (username, password_hash, role) VALUES ('local', 'x', 'admin')`)
	cfg := &config.Config{Auth: config.AuthConfig{Enabled: false, SingleUser: config.UserConfig{Username: "local"}}}

	r := gin.New()
	r.Use(IdentityResolver(cfg, db))
	r.GET("/mcp/test", func(c *gin.Context) {
		_, exists := c.Get("auth_user_id")
		c.JSON(200, gin.H{"has_identity": exists})
	})

	req := httptest.NewRequest("GET", "/mcp/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"has_identity":false`) {
		t.Errorf("mcp path should not get identity; body=%s", w.Body.String())
	}
}
