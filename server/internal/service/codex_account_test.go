package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

func setupCodexAccountDB(t *testing.T) (*sql.DB, *store.Queries) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("failed to open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	store.Migrate(db)

	return db, store.New(db)
}

func newCodexAccountTestSvc(t *testing.T, dataDir string) *CodexAccountService {
	t.Helper()
	rawDB, q := setupCodexAccountDB(t)
	return NewCodexAccountService(store.Wrap(rawDB), q, dataDir)
}

func TestCodexVisibleTo_PublicAlwaysVisible(t *testing.T) {
	svc := &CodexAccountService{}
	acc := &ResolvedCodexAccount{Visibility: "public", CreatedBy: 99}
	if !svc.visibleTo(acc, callerInfo{ID: 1, Role: "member"}) {
		t.Error("public should be visible to all")
	}
}

func TestCodexVisibleTo_PrivateOnlyCreatorAndAdmin(t *testing.T) {
	svc := &CodexAccountService{}
	acc := &ResolvedCodexAccount{Visibility: "private", CreatedBy: 5}

	if !svc.visibleTo(acc, callerInfo{ID: 5, Role: "member"}) {
		t.Error("private visible to creator")
	}
	if !svc.visibleTo(acc, callerInfo{ID: 99, Role: "admin"}) {
		t.Error("private visible to admin")
	}
	if svc.visibleTo(acc, callerInfo{ID: 99, Role: "member"}) {
		t.Error("private NOT visible to other members")
	}
}

func TestCodexCreate_Succeeds(t *testing.T) {
	tmp := t.TempDir()
	svc := newCodexAccountTestSvc(t, tmp)

	out, err := svc.Create(context.Background(), CreateCodexAccountInput{
		Name:       "工作号",
		Visibility: "public",
		CallerID:   42,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ID == 0 {
		t.Error("expected non-zero id")
	}
	if out.ConfigDir == "" {
		t.Error("expected non-empty config_dir")
	}
	st, err := os.Stat(out.ConfigDir)
	if err != nil || !st.IsDir() {
		t.Errorf("config_dir should exist as a directory: stat=%v err=%v", st, err)
	}
	if out.Visibility != "public" {
		t.Errorf("Visibility=%q want public", out.Visibility)
	}
	if out.Status != "pending" {
		t.Errorf("Status=%q want pending", out.Status)
	}
}

func TestCodexCreate_DuplicateName(t *testing.T) {
	tmp := t.TempDir()
	svc := newCodexAccountTestSvc(t, tmp)
	ctx := context.Background()

	if _, err := svc.Create(ctx, CreateCodexAccountInput{Name: "dup", CallerID: 1}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := svc.Create(ctx, CreateCodexAccountInput{Name: "dup", CallerID: 1})
	if !errors.Is(err, ErrCodexNameTaken) {
		t.Errorf("expected ErrCodexNameTaken, got %v", err)
	}
}

func TestCodexList_VisibilityFilter(t *testing.T) {
	tmp := t.TempDir()
	svc := newCodexAccountTestSvc(t, tmp)
	ctx := context.Background()

	// alice creates a private; bob creates a public.
	if _, err := svc.Create(ctx, CreateCodexAccountInput{Name: "alice-priv", Visibility: "private", CallerID: 1}); err != nil {
		t.Fatalf("alice Create: %v", err)
	}
	if _, err := svc.Create(ctx, CreateCodexAccountInput{Name: "bob-pub", Visibility: "public", CallerID: 2}); err != nil {
		t.Fatalf("bob Create: %v", err)
	}

	// bob (member) sees only public.
	bobList, err := svc.List(ctx, callerInfo{ID: 2, Role: "member"})
	if err != nil {
		t.Fatalf("bob List: %v", err)
	}
	if len(bobList) != 1 || bobList[0].Name != "bob-pub" {
		t.Errorf("bob should see only bob-pub; got %v", bobList)
	}

	// alice (member) sees own private + public.
	aliceList, err := svc.List(ctx, callerInfo{ID: 1, Role: "member"})
	if err != nil {
		t.Fatalf("alice List: %v", err)
	}
	if len(aliceList) != 2 {
		t.Errorf("alice should see both rows; got %d", len(aliceList))
	}

	// admin sees everything.
	adminList, err := svc.List(ctx, callerInfo{ID: 99, Role: "admin"})
	if err != nil {
		t.Fatalf("admin List: %v", err)
	}
	if len(adminList) != 2 {
		t.Errorf("admin should see both rows; got %d", len(adminList))
	}
}

func TestCodexDelete_RemovesRowAndDir(t *testing.T) {
	tmp := t.TempDir()
	svc := newCodexAccountTestSvc(t, tmp)
	ctx := context.Background()

	out, err := svc.Create(ctx, CreateCodexAccountInput{Name: "tmp", CallerID: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(ctx, out.ID, callerInfo{ID: 1, Role: "member"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.GetByID(ctx, out.ID); !errors.Is(err, ErrCodexAccountNotFound) {
		t.Errorf("expected ErrCodexAccountNotFound after delete, got %v", err)
	}
	if _, err := os.Stat(out.ConfigDir); !os.IsNotExist(err) {
		t.Errorf("expected config_dir removed, stat err=%v", err)
	}
}

func TestCodexDelete_RefusesWhenInUse(t *testing.T) {
	tmp := t.TempDir()
	svc := newCodexAccountTestSvc(t, tmp)
	ctx := context.Background()

	out, err := svc.Create(ctx, CreateCodexAccountInput{Name: "busy", CallerID: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Register a tracker returning a phantom workspace ID; Delete must refuse.
	called := atomic.Int32{}
	svc.RegisterSpawnTracker(func(id int64) []int64 {
		called.Add(1)
		if id == out.ID {
			return []int64{123}
		}
		return nil
	})

	err = svc.Delete(ctx, out.ID, callerInfo{ID: 1, Role: "member"})
	if !errors.Is(err, ErrCodexAccountInUse) {
		t.Errorf("expected ErrCodexAccountInUse, got %v", err)
	}
	if called.Load() == 0 {
		t.Error("expected tracker to be called")
	}
}

func TestCodexResolveForWorkspace_NoBindingReturnsNil(t *testing.T) {
	tmp := t.TempDir()
	rawDB, q := setupCodexAccountDB(t)
	svc := NewCodexAccountService(store.Wrap(rawDB), q, tmp)
	ctx := context.Background()

	// Branch 1: workspace doesn't exist at all → (nil, nil) not error.
	acc, err := svc.ResolveForWorkspace(ctx, 9999, 1)
	if err != nil {
		t.Fatalf("ResolveForWorkspace missing workspace: %v", err)
	}
	if acc != nil {
		t.Errorf("expected nil for missing workspace, got %+v", acc)
	}

	// Branch 2: workspace exists but codex_account_id IS NULL → (nil, nil).
	res, err := rawDB.ExecContext(ctx,
		"INSERT INTO workspaces (name, path, status, owner_type, owner_id, cli_type) VALUES ('w', '/tmp/w', 'idle', 'user', 1, 'codex')")
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	wsID, _ := res.LastInsertId()
	acc, err = svc.ResolveForWorkspace(ctx, wsID, 1)
	if err != nil {
		t.Fatalf("ResolveForWorkspace unbound workspace: %v", err)
	}
	if acc != nil {
		t.Errorf("expected nil for unbound workspace, got %+v", acc)
	}
}

func TestCodexRefreshFromDisk_MissingAuthLeavesPending(t *testing.T) {
	tmp := t.TempDir()
	svc := newCodexAccountTestSvc(t, tmp)
	ctx := context.Background()

	out, err := svc.Create(ctx, CreateCodexAccountInput{Name: "fresh", CallerID: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.RefreshFromDisk(ctx, out.ID); err != nil {
		t.Fatalf("RefreshFromDisk: %v", err)
	}
	after, err := svc.GetByID(ctx, out.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if after.Status != "pending" {
		t.Errorf("Status=%q want pending (no auth.json)", after.Status)
	}
}

func TestCodexRefreshFromDisk_ReadsAuthJSON(t *testing.T) {
	tmp := t.TempDir()
	svc := newCodexAccountTestSvc(t, tmp)
	ctx := context.Background()

	out, err := svc.Create(ctx, CreateCodexAccountInput{Name: "lived", CallerID: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Synthesize a JWT id_token with an "email" claim.
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]string{"email": "user@example.com"})
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	jwt := header + "." + payloadB64 + ".sig"

	auth := map[string]any{
		"tokens": map[string]string{"id_token": jwt},
	}
	authBytes, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(out.ConfigDir, "auth.json"), authBytes, 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	if err := svc.RefreshFromDisk(ctx, out.ID); err != nil {
		t.Fatalf("RefreshFromDisk: %v", err)
	}
	after, err := svc.GetByID(ctx, out.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if after.Status != "active" {
		t.Errorf("Status=%q want active", after.Status)
	}
	if after.Email != "user@example.com" {
		t.Errorf("Email=%q want user@example.com", after.Email)
	}
}

func TestCodexGetDefaultStatus_PendingWhenAuthMissing(t *testing.T) {
	tmp := t.TempDir()
	// Point CODEX_HOME at a fresh empty dir so we don't read whatever the
	// developer's actual ~/.codex/ contains.
	t.Setenv("CODEX_HOME", tmp)

	svc := newCodexAccountTestSvc(t, t.TempDir())
	got, err := svc.GetDefaultStatus(context.Background())
	if err != nil {
		t.Fatalf("GetDefaultStatus: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("Status=%q want pending", got.Status)
	}
	if got.Email != "" {
		t.Errorf("Email=%q want empty", got.Email)
	}
	if got.ConfigDir != tmp {
		t.Errorf("ConfigDir=%q want %q", got.ConfigDir, tmp)
	}
}

func TestCodexGetDefaultStatus_ActiveWhenAuthPresent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CODEX_HOME", tmp)

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]string{"email": "host@example.com"})
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	jwt := header + "." + payloadB64 + ".sig"
	auth := map[string]any{
		"tokens": map[string]string{"id_token": jwt},
	}
	authBytes, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(tmp, "auth.json"), authBytes, 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	svc := newCodexAccountTestSvc(t, t.TempDir())
	got, err := svc.GetDefaultStatus(context.Background())
	if err != nil {
		t.Fatalf("GetDefaultStatus: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("Status=%q want active", got.Status)
	}
	if got.Email != "host@example.com" {
		t.Errorf("Email=%q want host@example.com", got.Email)
	}
}

func TestCodexLoginToken_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	svc := newCodexAccountTestSvc(t, tmp)

	tok, err := svc.IssueLoginToken(42)
	if err != nil {
		t.Fatalf("IssueLoginToken: %v", err)
	}
	id, err := svc.VerifyLoginToken(tok)
	if err != nil {
		t.Fatalf("VerifyLoginToken: %v", err)
	}
	if id != 42 {
		t.Errorf("VerifyLoginToken=%d want 42", id)
	}
	// Single-use: second verify must fail.
	if _, err := svc.VerifyLoginToken(tok); err == nil {
		t.Error("expected single-use rejection on second Verify")
	}
}
