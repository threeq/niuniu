package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

// TestProjectHandler_Create_BuiltinBlueprint_AuthEnabled is a regression test
// for the "创建项目报了 403" bug: with auth enabled (auth_user_id > 0), creating
// a project from a builtin blueprint (owner sentinel user/0) returned 403
// because Create gated the builtin through CanAccessOwner, which has no
// sentinel exemption (0 != userID → ErrForbidden). Builtins are globally
// usable, so this must succeed with 201.
func TestProjectHandler_Create_BuiltinBlueprint_AuthEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	store.Migrate(db)

	ctx := context.Background()
	q := store.New(db)
	activitySvc := service.NewIssueActivityService(q)
	kanban := service.NewKanbanService(db, q, activitySvc, nil, nil)
	bpSvc := service.NewProjectBlueprintService(db, q)
	authz := service.NewAuthz(q, db)
	projSvc := service.NewProjectService(q, db, nil, authz)
	h := NewProjectHandler(projSvc, kanban, bpSvc, authz)

	// Builtins are seeded on boot in production; do it explicitly here.
	if err := bpSvc.SeedBuiltins(ctx); err != nil {
		t.Fatalf("seed builtins: %v", err)
	}

	// The builtin blueprint the new-project picker pre-selects (owner = user/0).
	var builtinID int64
	if err := db.QueryRow(
		`SELECT id FROM project_blueprints WHERE source = 'builtin' AND is_default = 1 ORDER BY id ASC LIMIT 1`,
	).Scan(&builtinID); err != nil {
		t.Fatalf("find builtin blueprint: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(
		http.MethodPost, "/api/projects",
		strings.NewReader(`{"name":"test","blueprint_id":`+strconv.FormatInt(builtinID, 10)+`}`),
	)
	c.Request.Header.Set("Content-Type", "application/json")
	// Simulate an authenticated caller (auth enabled → userID > 0).
	c.Set("auth_user_id", int64(1))
	c.Set("auth_role", "member")

	h.Create(c)

	if w.Code != http.StatusCreated {
		t.Fatalf("creating a project from a builtin blueprint must return 201, got %d: %s",
			w.Code, w.Body.String())
	}
}
