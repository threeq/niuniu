package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	_ "modernc.org/sqlite"
)

// setupClaudeAccountDB opens an in-memory SQLite DB suitable for ClaudeAccountService tests.
// Inlined here (rather than importing internal/testing) to avoid the import cycle:
// internal/testing → internal/service → (test) → internal/testing.
func setupClaudeAccountDB(t *testing.T) (*sql.DB, *store.Queries) {
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

func newClaudeAccountTestSvc(t *testing.T, dataDir string) *ClaudeAccountService {
	t.Helper()
	rawDB, q := setupClaudeAccountDB(t)
	return NewClaudeAccountService(store.Wrap(rawDB), q, dataDir)
}

func TestVisibleTo_PublicAlwaysVisible(t *testing.T) {
	svc := &ClaudeAccountService{}
	acc := &ResolvedAccount{Visibility: "public", CreatedBy: 99}
	if !svc.visibleTo(acc, callerInfo{ID: 1, Role: "member"}) {
		t.Error("public should be visible to all")
	}
}

func TestVisibleTo_PrivateOnlyCreatorAndAdmin(t *testing.T) {
	svc := &ClaudeAccountService{}
	acc := &ResolvedAccount{Visibility: "private", CreatedBy: 5}

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

func TestCreate_Succeeds(t *testing.T) {
	tmp := t.TempDir()
	svc := newClaudeAccountTestSvc(t, tmp)

	out, err := svc.Create(context.Background(), CreateAccountInput{
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
		t.Error("expected non-empty config_dir for managed account")
	}
	if out.Status != "pending" {
		t.Errorf("status=%s, want pending", out.Status)
	}
	if _, err := os.Stat(out.ConfigDir); err != nil {
		t.Errorf("config_dir not created on disk: %v", err)
	}
}

func TestCreate_RejectsEmptyName(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	_, err := svc.Create(context.Background(), CreateAccountInput{Name: "", CallerID: 1})
	if err == nil {
		t.Error("expected error on empty name")
	}
}

func TestCreate_DuplicateName(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()

	if _, err := svc.Create(ctx, CreateAccountInput{Name: "a", CallerID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, CreateAccountInput{Name: "a", CallerID: 1}); !IsNameTaken(err) {
		t.Errorf("expected NameTaken, got %v", err)
	}
}

func TestCreate_InvalidVisibility(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	_, err := svc.Create(context.Background(), CreateAccountInput{
		Name: "x", Visibility: "weird", CallerID: 1,
	})
	if !errors.Is(err, ErrInvalidVisibility) {
		t.Errorf("expected ErrInvalidVisibility, got %v", err)
	}
}

func TestList_FiltersPrivate(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()

	_, _ = svc.Create(ctx, CreateAccountInput{Name: "pub", Visibility: "public", CallerID: 1})
	_, _ = svc.Create(ctx, CreateAccountInput{Name: "mine", Visibility: "private", CallerID: 1})
	_, _ = svc.Create(ctx, CreateAccountInput{Name: "theirs", Visibility: "private", CallerID: 99})

	got, err := svc.List(ctx, callerInfo{ID: 1, Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, a := range got {
		names[a.Name] = true
	}
	if !names["pub"] || !names["mine"] || names["theirs"] {
		t.Errorf("filtering wrong: %v", names)
	}

	gotAdmin, _ := svc.List(ctx, callerInfo{ID: 7, Role: "admin"})
	if len(gotAdmin) < 3 {
		t.Errorf("admin should see all 3+ rows, got %d", len(gotAdmin))
	}
}

func TestUpdate_Rename(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()
	created, _ := svc.Create(ctx, CreateAccountInput{Name: "old", CallerID: 1})

	if err := svc.Update(ctx, UpdateAccountInput{
		ID: created.ID, Name: stringPtr("new"),
		Caller: callerInfo{ID: 1, Role: "member"},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetByID(ctx, created.ID)
	if got.Name != "new" {
		t.Errorf("name=%s, want new", got.Name)
	}
}

func TestUpdate_VisibilityChangeByCreator(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()
	created, _ := svc.Create(ctx, CreateAccountInput{Name: "x", CallerID: 1, Visibility: "public"})

	if err := svc.Update(ctx, UpdateAccountInput{
		ID: created.ID, Visibility: stringPtr("private"),
		Caller: callerInfo{ID: 1, Role: "member"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdate_RejectsByNonCreator(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()
	created, _ := svc.Create(ctx, CreateAccountInput{Name: "x", CallerID: 1})

	err := svc.Update(ctx, UpdateAccountInput{
		ID: created.ID, Name: stringPtr("y"),
		Caller: callerInfo{ID: 2, Role: "member"},
	})
	if !errors.Is(err, errForbidden) {
		t.Errorf("expected errForbidden, got %v", err)
	}
}

func TestDelete_RejectsDefaultRow(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()

	def, err := svc.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault: %v (default row should be seeded by migrate)", err)
	}
	err = svc.Delete(ctx, def.ID, callerInfo{ID: 1, Role: "admin"})
	if !errors.Is(err, ErrCannotDeleteDefault) {
		t.Errorf("expected ErrCannotDeleteDefault, got %v", err)
	}
}

func TestDelete_RemovesConfigDir(t *testing.T) {
	tmpDir := t.TempDir()
	svc := newClaudeAccountTestSvc(t, tmpDir)
	ctx := context.Background()

	created, _ := svc.Create(ctx, CreateAccountInput{Name: "tmp", CallerID: 1})
	if _, err := os.Stat(created.ConfigDir); err != nil {
		t.Fatalf("config_dir should exist after Create: %v", err)
	}

	if err := svc.Delete(ctx, created.ID, callerInfo{ID: 1, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(created.ConfigDir); !os.IsNotExist(err) {
		t.Errorf("config_dir should be removed; stat err=%v", err)
	}
}

func stringPtr(s string) *string { return &s }
func int64Ptr(i int64) *int64    { return &i }

func TestDelete_RefusesWhenInUse(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()
	created, _ := svc.Create(ctx, CreateAccountInput{Name: "in-use", CallerID: 1})

	// Register a tracker that pretends workspace 42 is using this account.
	svc.RegisterSpawnTracker(func(accountID int64) []int64 {
		if accountID == created.ID {
			return []int64{42}
		}
		return nil
	})

	err := svc.Delete(ctx, created.ID, callerInfo{ID: 1, Role: "admin"})
	if !errors.Is(err, ErrAccountInUse) {
		t.Fatalf("expected ErrAccountInUse, got %v", err)
	}
}

func TestDelete_AllowsWhenNoTrackerHits(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()
	created, _ := svc.Create(ctx, CreateAccountInput{Name: "free", CallerID: 1})

	// Tracker that returns nothing for any account.
	svc.RegisterSpawnTracker(func(accountID int64) []int64 { return nil })

	if err := svc.Delete(ctx, created.ID, callerInfo{ID: 1, Role: "admin"}); err != nil {
		t.Fatalf("Delete should succeed when no tracker reports usage: %v", err)
	}
}

func TestSetActive_PersonalScopeSelf(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()
	a1, _ := svc.Create(ctx, CreateAccountInput{Name: "a", CallerID: 1})

	if err := svc.SetActive(ctx, "user", 1, &a1.ID, callerInfo{ID: 1, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetActive(ctx, "user", 1)
	if got == nil || got.ID != a1.ID {
		t.Errorf("active=%v want %d", got, a1.ID)
	}
}

func TestSetActive_PersonalScopeOtherUserForbidden(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()
	a1, _ := svc.Create(ctx, CreateAccountInput{Name: "a", CallerID: 1})

	err := svc.SetActive(ctx, "user", 2, &a1.ID, callerInfo{ID: 1, Role: "member"})
	if !errors.Is(err, errForbidden) {
		t.Errorf("expected errForbidden, got %v", err)
	}
}

func TestSetActive_ClearsByNilAccountID(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()
	a1, _ := svc.Create(ctx, CreateAccountInput{Name: "a", CallerID: 1})
	_ = svc.SetActive(ctx, "user", 1, &a1.ID, callerInfo{ID: 1, Role: "member"})

	if err := svc.SetActive(ctx, "user", 1, nil, callerInfo{ID: 1, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetActive(ctx, "user", 1)
	if got != nil {
		t.Errorf("expected nil after clear, got %v", got)
	}
}

func TestSetActive_PrivateAccountNotVisibleRejected(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()
	priv, _ := svc.Create(ctx, CreateAccountInput{
		Name: "their-private", Visibility: "private", CallerID: 99,
	})
	err := svc.SetActive(ctx, "user", 1, &priv.ID, callerInfo{ID: 1, Role: "member"})
	if !errors.Is(err, ErrNotVisible) {
		t.Errorf("expected ErrNotVisible, got %v", err)
	}
}

// SetWorkspaceOverride end-to-end behavior (override → default-row auto-clears,
// visibility check, resolve chain) is covered in Task 8's ResolveForWorkspace
// tests where workspace fixtures are set up alongside.

// TestResolveConfigDirForUser_EmptyWhenNoActive covers the workspace-less
// resolver path used by SuggestGoalCondition: when the user has NOT picked
// an explicit default in Settings → Claude 账号 (dropdown shows
// "(无默认账号)"), the resolver returns "" so the caller skips
// CLAUDE_CONFIG_DIR injection and the CLI falls back to ~/.claude.
//
// This is the dropdown's literal semantic — "follow default" means the
// host's native home, not the migration's __default__ row.
func TestResolveConfigDirForUser_EmptyWhenNoActive(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()

	// userID == 0 (anonymous) → ""
	if got := svc.ResolveConfigDirForUser(ctx, 0); got != "" {
		t.Errorf("expected empty for userID=0, got %q", got)
	}
	// userID with no claude_active_account row → "". store.Migrate seeds
	// __default__ in the claude_accounts table but does NOT auto-bind it
	// via SetActive — explicit operator intent is required.
	if got := svc.ResolveConfigDirForUser(ctx, 1); got != "" {
		t.Errorf("expected empty when no claude_active_account row, got %q", got)
	}
}

// TestResolveConfigDirForUser_HonorsExplicitActive locks in the
// 2026-05-15 rule: the resolver returns whatever account the user
// explicitly picked in Settings → Claude 账号 → "新建工作空间的默认账号",
// nothing more, nothing less. No last_used_at heuristics, no implicit
// __default__ preference — the dropdown is the only source of truth.
func TestResolveConfigDirForUser_HonorsExplicitActive(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()

	a, err := svc.Create(ctx, CreateAccountInput{Name: "ops-pool", CallerID: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Mimic the "登录成功" path that activates the account, then bind it
	// via SetActive as the dropdown does on user select.
	if _, err := svc.db.ExecContext(ctx,
		`UPDATE claude_accounts SET status='active' WHERE id=?`, a.ID,
	); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	if err := svc.SetActive(ctx, "user", 1, &a.ID, NewCallerInfo(1, "member")); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	got := svc.ResolveConfigDirForUser(ctx, 1)
	if got != a.ConfigDir {
		t.Errorf("expected config_dir %q from explicitly-selected active account, got %q",
			a.ConfigDir, got)
	}
}

// TestResolveConfigDirForUser_LastUsedAtIsIgnored locks in the regression
// fix for the production drift: a side account with the most recent
// last_used_at (bumped by a parallel workspace agent's MarkUsed) must NOT
// be picked when the user has no explicit active set. Without an active
// row, the resolver returns "" → ~/.claude — no implicit promotion.
func TestResolveConfigDirForUser_LastUsedAtIsIgnored(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()

	// A side account with a very recent last_used_at, mimicking the prod
	// state where a workspace agent spawn bumped its timestamp seconds
	// after a __default__ spawn.
	side, err := svc.Create(ctx, CreateAccountInput{Name: "side-recent", CallerID: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx,
		`UPDATE claude_accounts SET status='active', last_used_at=9999 WHERE id=?`, side.ID,
	); err != nil {
		t.Fatalf("seed side: %v", err)
	}
	// NO SetActive call — user has not picked an explicit default.

	if got := svc.ResolveConfigDirForUser(ctx, 1); got != "" {
		t.Errorf("expected '' (no explicit active, ignore last_used_at), got %q", got)
	}
}

// TestResolveAccountForUser_ReturnsFullAccountForDiagnostics covers the
// richer sibling of ResolveConfigDirForUser used by handlers that need to
// name the chosen account in operator-facing diagnostics (e.g.
// SuggestGoalCondition's 502 body and slog). The resolver must expose
// id/name/config_dir together for the user's explicit active account,
// so the handler can compose "<name> (id=<id>)" labels.
func TestResolveAccountForUser_ReturnsFullAccountForDiagnostics(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()

	// userID == 0 (anonymous) → nil
	if got := svc.ResolveAccountForUser(ctx, 0); got != nil {
		t.Errorf("expected nil for userID=0, got %+v", got)
	}
	// No active set yet → nil, even with accounts present.
	if got := svc.ResolveAccountForUser(ctx, 1); got != nil {
		t.Errorf("expected nil before SetActive, got %+v", got)
	}

	// Bind an explicit active account; verify the returned struct carries
	// the diagnostic fields the handler renders.
	a, err := svc.Create(ctx, CreateAccountInput{Name: "ops-pool", CallerID: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx,
		`UPDATE claude_accounts SET status='active' WHERE id=?`, a.ID,
	); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	if err := svc.SetActive(ctx, "user", 1, &a.ID, NewCallerInfo(1, "member")); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	got := svc.ResolveAccountForUser(ctx, 1)
	if got == nil {
		t.Fatal("expected explicitly-selected active account, got nil")
	}
	if got.ID != a.ID {
		t.Errorf("expected ID=%d, got %d", a.ID, got.ID)
	}
	if got.Name != "ops-pool" {
		t.Errorf("expected Name=ops-pool, got %q", got.Name)
	}
	if got.ConfigDir == "" {
		t.Error("expected non-empty ConfigDir for a real account")
	}

	// Parity with ResolveConfigDirForUser: both methods MUST agree on
	// selection so callers can rely on the same account being used for
	// the subprocess and the diagnostic label.
	if cd := svc.ResolveConfigDirForUser(ctx, 1); cd != got.ConfigDir {
		t.Errorf("ResolveConfigDirForUser=%q diverged from ResolveAccountForUser.ConfigDir=%q",
			cd, got.ConfigDir)
	}
}

// TestResolveAccountForUser_HonorsExplicitFailedAccount locks in the
// "user choice is sacred" rule: even when the explicitly-selected active
// account is marked failed, the resolver returns it (not nil and not a
// silent fallback). The handler's diagnostic surfaces "[account X]
// ... failed" so the user knows to fix that specific account or flip the
// dropdown — silently swapping accounts would hide the misconfiguration.
func TestResolveAccountForUser_HonorsExplicitFailedAccount(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()

	a, err := svc.Create(ctx, CreateAccountInput{Name: "broken-but-chosen", CallerID: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx,
		`UPDATE claude_accounts SET status='active' WHERE id=?`, a.ID,
	); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	if err := svc.SetActive(ctx, "user", 1, &a.ID, NewCallerInfo(1, "member")); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	// Now mark it failed after the user picked it.
	if _, err := svc.db.ExecContext(ctx,
		`UPDATE claude_accounts SET status='failed' WHERE id=?`, a.ID,
	); err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	got := svc.ResolveAccountForUser(ctx, 1)
	if got == nil {
		t.Fatal("expected to return the explicitly-selected account even when failed, got nil")
	}
	if got.ID != a.ID {
		t.Errorf("expected the chosen account ID=%d, got %d", a.ID, got.ID)
	}
	if got.Status != "failed" {
		t.Errorf("expected status=failed to surface to caller, got %q", got.Status)
	}
}

// TestSetWorkspaceOverride_SessionClearerCalledOnFailedMigration guards the
// fix for "Agent process exited (code 1): No conversation found with session
// ID …" — when the projects-dir migration cannot run (no source dir, cross-
// device, etc.), the in-memory session must be invalidated so the next spawn
// does not pass --resume with an ID the new account doesn't know.
func TestSetWorkspaceOverride_SessionClearerCalledOnFailedMigration(t *testing.T) {
	svc, db := resolveTestSetup(t)
	ctx := context.Background()

	target, _ := svc.Create(ctx, CreateAccountInput{Name: "target", CallerID: 1})
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, path, owner_type, owner_id) VALUES (1, '/tmp/w-no-src', 'user', 1)`,
	); err != nil {
		t.Fatal(err)
	}

	var calledWith atomic.Int64
	svc.RegisterSessionClearer(func(_ context.Context, wsID int64) {
		calledWith.Store(wsID)
	})

	if err := svc.SetWorkspaceOverride(ctx, 1, &target.ID, callerInfo{ID: 1, Role: "member"}); err != nil {
		t.Fatalf("SetWorkspaceOverride: %v", err)
	}
	if got := calledWith.Load(); got != 1 {
		t.Errorf("expected session clearer called with workspace 1, got %d", got)
	}
}

// TestSetWorkspaceOverride_SessionClearerSkippedOnSuccessfulMigration asserts
// the clearer is NOT invoked on the happy path — when the projects dir was
// renamed atomically, the existing session_id remains valid under the new
// account's CLAUDE_CONFIG_DIR and must be preserved.
func TestSetWorkspaceOverride_SessionClearerSkippedOnSuccessfulMigration(t *testing.T) {
	svc, db := resolveTestSetup(t)
	ctx := context.Background()

	from, _ := svc.Create(ctx, CreateAccountInput{Name: "from", CallerID: 1})
	to, _ := svc.Create(ctx, CreateAccountInput{Name: "to", CallerID: 1})

	wsPath := filepath.Join(t.TempDir(), "ws-migrate")
	if err := os.MkdirAll(wsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, path, owner_type, owner_id, claude_account_id) VALUES (1, ?, 'user', 1, ?)`,
		wsPath, from.ID,
	); err != nil {
		t.Fatal(err)
	}

	// Create the source projects dir so migrateClaudeProjectsDir's os.Rename succeeds.
	hash := claudePathHash(wsPath)
	srcDir := filepath.Join(from.ConfigDir, "projects", hash)
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var called atomic.Bool
	svc.RegisterSessionClearer(func(_ context.Context, _ int64) { called.Store(true) })

	if err := svc.SetWorkspaceOverride(ctx, 1, &to.ID, callerInfo{ID: 1, Role: "member"}); err != nil {
		t.Fatalf("SetWorkspaceOverride: %v", err)
	}
	if called.Load() {
		t.Error("session clearer must not be invoked when projects-dir migration succeeds")
	}
	// And the dest dir should now exist (migration ran).
	dstDir := filepath.Join(to.ConfigDir, "projects", hash)
	if _, err := os.Stat(dstDir); err != nil {
		t.Errorf("expected dest projects dir to exist after migration: %v", err)
	}
}

// resolveTestSetup builds a service + raw DB so tests can directly INSERT
// workspace fixture rows.
func resolveTestSetup(t *testing.T) (*ClaudeAccountService, *sql.DB) {
	t.Helper()
	rawDB, q := setupTestDBRaw(t)
	svc := NewClaudeAccountService(store.Wrap(rawDB), q, t.TempDir())
	return svc, rawDB
}

func TestResolve_NoopWhenNoAccountBound(t *testing.T) {
	svc, db := resolveTestSetup(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, path, owner_type, owner_id) VALUES (1, '/tmp/w1', 'user', 99)`,
	); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ResolveForWorkspace(ctx, 1, 99)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigDir != "" {
		t.Errorf("expected noop (empty ConfigDir), got %q", got.ConfigDir)
	}
}

func TestResolve_WorkspaceOverrideHit(t *testing.T) {
	svc, db := resolveTestSetup(t)
	ctx := context.Background()

	override, _ := svc.Create(ctx, CreateAccountInput{Name: "override", CallerID: 1})
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, path, owner_type, owner_id, claude_account_id) VALUES (1, '/tmp/w1', 'user', 1, ?)`,
		override.ID,
	); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ResolveForWorkspace(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != override.ID {
		t.Errorf("expected override id %d, got %d", override.ID, got.ID)
	}
}

func TestResolve_NoopWhenBoundAccountInvisible(t *testing.T) {
	svc, db := resolveTestSetup(t)
	ctx := context.Background()

	priv, _ := svc.Create(ctx, CreateAccountInput{
		Name: "priv", Visibility: "private", CallerID: 99,
	})

	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, path, owner_type, owner_id, claude_account_id) VALUES (1, '/tmp/w1', 'user', 1, ?)`,
		priv.ID,
	); err != nil {
		t.Fatal(err)
	}

	// Caller 1 is not creator, not admin → invisible → noop.
	got, err := svc.ResolveForWorkspace(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigDir != "" {
		t.Errorf("expected noop (empty ConfigDir), got %q", got.ConfigDir)
	}
}

func TestResolve_AutonomousOrgNoopWhenPrivate(t *testing.T) {
	svc, db := resolveTestSetup(t)
	ctx := context.Background()

	priv, _ := svc.Create(ctx, CreateAccountInput{
		Name: "priv-org", Visibility: "private", CallerID: 5,
	})

	// Org-owned workspace with private override.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, path, owner_type, owner_id, claude_account_id) VALUES (1, '/tmp/w1', 'org', 7, ?)`,
		priv.ID,
	); err != nil {
		t.Fatal(err)
	}

	// userID=0 (autonomous) + org owner → caller="system" → can't see private → noop.
	got, err := svc.ResolveForWorkspace(ctx, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigDir != "" {
		t.Errorf("autonomous org should get noop (empty ConfigDir), got %q (private leak)", got.ConfigDir)
	}
}

func TestResolve_NoopWhenWorkspaceMissing(t *testing.T) {
	svc, _ := resolveTestSetup(t)

	got, err := svc.ResolveForWorkspace(context.Background(), 99999, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigDir != "" {
		t.Errorf("expected noop (empty ConfigDir) for missing workspace, got %q", got.ConfigDir)
	}
}

func TestMarkUsed_UpdatesLastUsedAt(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	ctx := context.Background()
	a, _ := svc.Create(ctx, CreateAccountInput{Name: "a", CallerID: 1})
	if a.LastUsedAt != 0 {
		t.Errorf("expected last_used_at=0 on new account, got %d", a.LastUsedAt)
	}

	svc.MarkUsed(ctx, a.ID)

	got, _ := svc.GetByID(ctx, a.ID)
	if got.LastUsedAt == 0 {
		t.Errorf("expected last_used_at > 0 after MarkUsed")
	}
}

// setupTestDBRaw is a private duplicate of internal/testing.SetupDBRaw — the
// internal/testing package transitively imports internal/service, so direct
// import would create a cycle. Maintain in lockstep with that helper.
func setupTestDBRaw(t *testing.T) (*sql.DB, *store.Queries) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	store.Migrate(db)
	return db, store.New(db)
}

func TestCanAccessClaudeAccount_DefaultPublicVisibleToAnyCaller(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	def, err := svc.GetDefault(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.CanAccessClaudeAccount(context.Background(), NewCallerInfo(999, "member"), def.ID)
	if err != nil {
		t.Fatalf("default must be visible: %v", err)
	}
	if got.ID != def.ID {
		t.Fatalf("got id=%d want %d", got.ID, def.ID)
	}
}

func TestCanAccessClaudeAccount_PrivateVisibleToCreator(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	created, err := svc.Create(context.Background(), CreateAccountInput{
		Name: "priv1", Visibility: "private", CallerID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CanAccessClaudeAccount(context.Background(), NewCallerInfo(1, "member"), created.ID); err != nil {
		t.Fatalf("creator must see own private: %v", err)
	}
	if _, err := svc.CanAccessClaudeAccount(context.Background(), NewCallerInfo(2, "member"), created.ID); !errors.Is(err, ErrNotVisible) {
		t.Fatalf("non-creator must get ErrNotVisible, got %v", err)
	}
}

func TestCanAccessClaudeAccount_PrivateVisibleToAdmin(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	created, err := svc.Create(context.Background(), CreateAccountInput{
		Name: "priv2", Visibility: "private", CallerID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CanAccessClaudeAccount(context.Background(), NewCallerInfo(99, "admin"), created.ID); err != nil {
		t.Fatalf("admin must see private: %v", err)
	}
}

func TestCanAccessClaudeAccount_MissingReturnsNotFound(t *testing.T) {
	svc := newClaudeAccountTestSvc(t, t.TempDir())
	_, err := svc.CanAccessClaudeAccount(context.Background(), NewCallerInfo(1, "admin"), 99999)
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("got %v, want ErrAccountNotFound", err)
	}
}

func TestDeleteAccount_EvictsUsageCache(t *testing.T) {
	accSvc := newClaudeAccountTestSvc(t, t.TempDir())
	usageSvc := NewClaudeUsageService(false, accSvc)
	accSvc.SetUsageEvictor(usageSvc)

	created, err := accSvc.Create(context.Background(), CreateAccountInput{
		Name: "to-delete", Visibility: "public", CallerID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Prime the cache so we can verify eviction.
	usageSvc.PrimeForTest(created.ID)
	if !usageSvc.HasCacheForTest(created.ID) {
		t.Fatal("PrimeForTest didn't seed cache")
	}

	if err := accSvc.Delete(context.Background(), created.ID, NewCallerInfo(1, "admin")); err != nil {
		t.Fatal(err)
	}
	if usageSvc.HasCacheForTest(created.ID) {
		t.Fatal("cache not evicted after delete")
	}
}
