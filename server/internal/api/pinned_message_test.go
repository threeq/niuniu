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
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

// pinRESTSetup wires a Gin engine with the pinned-message routes and seeds two
// workspaces: one owned by user 1 (the default test caller) and one owned by
// user 2 (for cross-owner authz checks). Returns the engine and both ws ids.
func pinRESTSetup(t *testing.T) (*gin.Engine, int64, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openPermREST(t) // reuses the in-memory schema helper from permission_test.go

	q := store.New(db)
	wsAlice, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "alice-ws", Path: "/tmp/ws-a", Status: "created",
		OwnerType: "user", OwnerID: 1,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace alice: %v", err)
	}
	wsBob, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "bob-ws", Path: "/tmp/ws-b", Status: "created",
		OwnerType: "user", OwnerID: 2,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace bob: %v", err)
	}

	authz := service.NewAuthz(q, db)
	h := &api.PinnedMessageHandler{DB: store.Wrap(db), Authz: authz}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		uid := int64(1)
		if v := c.GetHeader("X-Test-User-ID"); v != "" {
			if parsed, perr := strconv.ParseInt(v, 10, 64); perr == nil {
				uid = parsed
			}
		}
		c.Set("auth_user_id", uid)
		c.Next()
	})
	r.GET("/api/workspaces/:id/pinned-messages", h.List)
	r.POST("/api/workspaces/:id/pinned-messages", h.Create)
	r.DELETE("/api/workspaces/:id/pinned-messages/:pinId", h.Delete)
	return r, wsAlice.ID, wsBob.ID
}

func pinReq(t *testing.T, r *gin.Engine, method, path, body string, userID int64) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if userID > 0 {
		req.Header.Set("X-Test-User-ID", strconv.FormatInt(userID, 10))
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPinnedMessage_CreateListDelete(t *testing.T) {
	r, wsID, _ := pinRESTSetup(t)
	base := "/api/workspaces/" + strconv.FormatInt(wsID, 10) + "/pinned-messages"

	// Empty list initially.
	w := pinReq(t, r, http.MethodGet, base, "", 1)
	if w.Code != http.StatusOK {
		t.Fatalf("list: want 200 got %d: %s", w.Code, w.Body.String())
	}
	var listed struct {
		Pins []map[string]any `json:"pins"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(listed.Pins) != 0 {
		t.Fatalf("want empty list, got %d", len(listed.Pins))
	}

	// Pin a message.
	w = pinReq(t, r, http.MethodPost, base, `{"message_id":"m1","role":"assistant","preview":"hello"}`, 1)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: want 201 got %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		Pin map[string]any `json:"pin"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal create: %v", err)
	}
	pinID := int64(created.Pin["id"].(float64))
	if pinID <= 0 {
		t.Fatalf("want pin id > 0, got %v", created.Pin["id"])
	}

	// List now has one entry with the preview.
	w = pinReq(t, r, http.MethodGet, base, "", 1)
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	if len(listed.Pins) != 1 || listed.Pins[0]["preview"] != "hello" {
		t.Fatalf("want one pin with preview 'hello', got %+v", listed.Pins)
	}

	// Delete it.
	delPath := base + "/" + strconv.FormatInt(pinID, 10)
	w = pinReq(t, r, http.MethodDelete, delPath, "", 1)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: want 204 got %d: %s", w.Code, w.Body.String())
	}

	// List empty again.
	w = pinReq(t, r, http.MethodGet, base, "", 1)
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	if len(listed.Pins) != 0 {
		t.Fatalf("want empty after delete, got %d", len(listed.Pins))
	}
}

func TestPinnedMessage_UpsertIdempotent(t *testing.T) {
	r, wsID, _ := pinRESTSetup(t)
	base := "/api/workspaces/" + strconv.FormatInt(wsID, 10) + "/pinned-messages"

	pinReq(t, r, http.MethodPost, base, `{"message_id":"m1","role":"user","preview":"first"}`, 1)
	pinReq(t, r, http.MethodPost, base, `{"message_id":"m1","role":"user","preview":"second"}`, 1)

	w := pinReq(t, r, http.MethodGet, base, "", 1)
	var listed struct {
		Pins []map[string]any `json:"pins"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	if len(listed.Pins) != 1 {
		t.Fatalf("upsert should keep a single row, got %d", len(listed.Pins))
	}
	if listed.Pins[0]["preview"] != "second" {
		t.Fatalf("upsert should refresh preview, got %v", listed.Pins[0]["preview"])
	}
}

func TestPinnedMessage_CrossOwnerForbidden(t *testing.T) {
	r, _, wsBob := pinRESTSetup(t)
	base := "/api/workspaces/" + strconv.FormatInt(wsBob, 10) + "/pinned-messages"

	// User 1 cannot list or pin in user 2's workspace.
	w := pinReq(t, r, http.MethodGet, base, "", 1)
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("cross-owner list: want 403/404 got %d: %s", w.Code, w.Body.String())
	}
	w = pinReq(t, r, http.MethodPost, base, `{"message_id":"m1"}`, 1)
	if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Fatalf("cross-owner create: want 403/404 got %d: %s", w.Code, w.Body.String())
	}
}

