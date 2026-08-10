package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/imageopt"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

// makeAttachmentHandlerForTest constructs a handler backed by in-memory sqlite
// with the full schema. The workspace row points at a per-test tmpdir.
// userID=0 in the gin context bypasses the Authz gate (userID > 0 check).
//
// Returns: handler, workspace path, workspace ID
func makeAttachmentHandlerForTest(t *testing.T) (*AttachmentHandler, string, int64) {
	t.Helper()

	wsDir := t.TempDir()

	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(store.Schema); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	store.Migrate(db)

	// Insert a minimal workspace row; all other columns have defaults.
	var wsID int64
	err = db.QueryRow(
		`INSERT INTO workspaces (path, name) VALUES (?, ?) RETURNING id`,
		wsDir, "test-workspace",
	).Scan(&wsID)
	if err != nil {
		// Fallback for SQLite builds without RETURNING support.
		res, err2 := db.Exec(
			`INSERT INTO workspaces (path, name) VALUES (?, ?)`,
			wsDir, "test-workspace",
		)
		if err2 != nil {
			t.Fatalf("insert workspace: %v", err2)
		}
		wsID, _ = res.LastInsertId()
	}

	queries := store.New(db)
	// nil cfg, notifyHub, authz are safe: CheckNotArchived only calls q.GetWorkspace.
	workspaceSvc := service.NewWorkspaceService(queries, db, nil, "", nil, nil)

	h := NewAttachmentHandler(queries, workspaceSvc)
	return h, wsDir, wsID
}

func makeAttachmentFixture(t *testing.T, name string, w, h int, transparent bool, noisy bool) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(42))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if transparent && x < w/4 && y < h/4 {
				a = 0
			}
			if noisy {
				img.SetNRGBA(x, y, color.NRGBA{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), a})
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, a})
		}
	}
	path := filepath.Join(t.TempDir(), name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return path
}

// makeMultipart wraps a disk file as multipart/form-data, returning
// the body buffer and the Content-Type header value.
func makeMultipart(t *testing.T, path string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	in, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer in.Close()
	if _, err := io.Copy(fw, in); err != nil {
		t.Fatalf("copy: %v", err)
	}
	w.Close()
	return &buf, w.FormDataContentType()
}

// doUpload posts fixturePath to handler.Upload for the given workspace ID and
// returns the recorder and the decoded JSON response body.
func doUpload(t *testing.T, handler *AttachmentHandler, wsID int64, fixturePath string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	body, contentType := makeMultipart(t, fixturePath)
	url := fmt.Sprintf("/workspaces/%d/attachments", wsID)
	req := httptest.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	router := gin.New()
	router.POST("/workspaces/:id/attachments", handler.Upload)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return rec, resp
}

func TestUpload_LargeImage_OptimizedFieldsTrue(t *testing.T) {
	// Large noisy PNG: above the 100 KB optimization trigger and below the
	// 10 MB upload cap. Generated in-test so fresh checkouts do not depend on
	// ignored imageopt/testdata fixtures.
	fixturePath := makeAttachmentFixture(t, "transparent-icon.png", 1024, 1024, true, true)

	handler, _, wsID := makeAttachmentHandlerForTest(t)
	handler.SetImageOpts(true, imageopt.DefaultOptions())

	_, resp := doUpload(t, handler, wsID, fixturePath)

	if resp["optimized"] != true {
		t.Errorf("optimized=%v, want true", resp["optimized"])
	}
	origSize, _ := resp["originalSize"].(float64)
	size, _ := resp["size"].(float64)
	if origSize <= size {
		t.Errorf("originalSize=%v should be > size=%v (original should be larger than compressed)", origSize, size)
	}
	if reason, ok := resp["optimizationSkipReason"].(string); !ok || reason != "" {
		t.Errorf("optimizationSkipReason=%q, want \"\"", reason)
	}
}

func TestUpload_OptimizationDisabled_FieldsPresentButFalse(t *testing.T) {
	// Same shape as the enabled-path test; file size is irrelevant when
	// optimization is disabled because the handler skips the optimization step.
	fixturePath := makeAttachmentFixture(t, "transparent-icon.png", 1024, 1024, true, true)

	handler, _, wsID := makeAttachmentHandlerForTest(t)
	handler.SetImageOpts(false, imageopt.DefaultOptions())

	_, resp := doUpload(t, handler, wsID, fixturePath)

	if resp["optimized"] != false {
		t.Errorf("optimized=%v, want false", resp["optimized"])
	}
	origSize, _ := resp["originalSize"].(float64)
	size, _ := resp["size"].(float64)
	if origSize != size {
		t.Errorf("originalSize (%v) should equal size (%v) when optimization is disabled", origSize, size)
	}
	if reason, ok := resp["optimizationSkipReason"].(string); !ok || reason != "" {
		t.Errorf("optimizationSkipReason=%q, want \"\"", reason)
	}
}

func TestUpload_SmallImage_SkipReasonAlreadySmall(t *testing.T) {
	fixturePath := makeAttachmentFixture(t, "small.png", 800, 600, false, false)

	handler, _, wsID := makeAttachmentHandlerForTest(t)
	handler.SetImageOpts(true, imageopt.DefaultOptions())

	_, resp := doUpload(t, handler, wsID, fixturePath)

	if resp["optimized"] != false {
		t.Errorf("optimized=%v, want false (small image should be skipped)", resp["optimized"])
	}
	if reason, _ := resp["optimizationSkipReason"].(string); reason != "already_small" {
		t.Errorf("optimizationSkipReason=%q, want already_small", reason)
	}
}
