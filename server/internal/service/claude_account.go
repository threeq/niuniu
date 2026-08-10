package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/niuniu-dev/niuniu/internal/claudehome"
	"github.com/niuniu-dev/niuniu/internal/paths"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// ResolvedAccount is the account info returned by Resolve* and read paths.
// Mirrors store.ClaudeAccount but with nullables flattened to Go primitives.
type ResolvedAccount struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	ConfigDir  string `json:"config_dir"`
	Visibility string `json:"visibility"`
	Status     string `json:"status"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
	CreatedBy  int64  `json:"created_by"`
}

// callerInfo carries the minimum identity bits needed for visibility +
// role authz inside the service. Handlers build it from the request context.
// Use NewCallerInfo() from outside the package.
type callerInfo struct {
	ID   int64
	Role string // "admin" | "member" | "system"
}

// NewCallerInfo is the public constructor for callerInfo (handlers use this).
func NewCallerInfo(id int64, role string) callerInfo {
	return callerInfo{ID: id, Role: role}
}

// CallerInfo is the exported alias for callerInfo so other packages
// (e.g. api) can name the type in interface signatures. Construct via
// NewCallerInfo.
type CallerInfo = callerInfo

// SpawnTrackerFn returns the workspace IDs currently running an agent that
// holds CLAUDE_CONFIG_DIR for the given accountID. Used by Delete to refuse
// removing an account whose config_dir is still in use (would cause the
// running claude CLI to lose its credentials mid-session).
type SpawnTrackerFn func(accountID int64) []int64

// SessionClearerFn kills any live agent process and zeroes the in-memory
// session ID for a workspace. Called when SetWorkspaceOverride falls back to
// clearing the session (directory migration failed) so the next spawn does
// not pass --resume with an ID that belongs to the old account.
type SessionClearerFn func(ctx context.Context, workspaceID int64)

// usageEvictor is the slice of *ClaudeUsageService that ClaudeAccountService
// uses for per-account cache cleanup — interface-shaped to avoid import cycle.
type usageEvictor interface {
	EvictAccount(accountID int64)
}

// ClaudeAccountService provides CRUD + visibility-filtered access to claude accounts.
type ClaudeAccountService struct {
	db              *store.DB
	q               *store.Queries
	dataDir         string
	tokens          *loginTokenStore
	trackers        []SpawnTrackerFn
	sessionClearers []SessionClearerFn
	usageEvictor    usageEvictor
}

// NewClaudeAccountService constructs the service. db must be a store.Wrap'd connection.
func NewClaudeAccountService(db *store.DB, q *store.Queries, dataDir string) *ClaudeAccountService {
	return &ClaudeAccountService{db: db, q: q, dataDir: dataDir, tokens: newLoginTokenStore()}
}

// RegisterSpawnTracker registers a spawn tracker (typically AgentManager and
// the agentproxy session map). Called from server.New after both are
// constructed. Multiple trackers OR'd together: if any returns non-empty,
// Delete refuses with ErrAccountInUse.
func (s *ClaudeAccountService) RegisterSpawnTracker(fn SpawnTrackerFn) {
	s.trackers = append(s.trackers, fn)
}

// RegisterSessionClearer registers a callback that kills any live agent
// process and clears the in-memory session ID when an account switch cannot
// migrate the projects directory. Called from server.New after AgentProxy is
// constructed.
func (s *ClaudeAccountService) RegisterSessionClearer(fn SessionClearerFn) {
	s.sessionClearers = append(s.sessionClearers, fn)
}

// SetUsageEvictor wires the usage cache so deletes can evict per-account state.
// nil-ok (no-op).
func (s *ClaudeAccountService) SetUsageEvictor(e usageEvictor) { s.usageEvictor = e }

// IssueLoginToken issues a short-lived HMAC token bound to the account.
func (s *ClaudeAccountService) IssueLoginToken(accountID int64) (string, error) {
	return s.tokens.Issue(accountID)
}

// VerifyLoginToken validates a token and returns the bound accountID.
// Single-use: subsequent calls with the same token error out.
func (s *ClaudeAccountService) VerifyLoginToken(tok string) (int64, error) {
	return s.tokens.Verify(tok)
}

// VisibleTo is the public form of visibleTo for handler-side gating.
func (s *ClaudeAccountService) VisibleTo(acc *ResolvedAccount, caller callerInfo) bool {
	return s.visibleTo(acc, caller)
}

// Sentinel errors.
var (
	ErrNameTaken           = errors.New("claude account: name taken")
	ErrAccountNotFound     = errors.New("claude account: not found")
	ErrCannotDeleteDefault = errors.New("claude account: cannot delete default")
	ErrNotVisible          = errors.New("claude account: not visible to caller")
	ErrInvalidVisibility   = errors.New("claude account: invalid visibility")
	ErrAccountInUse        = errors.New("claude account: in use by running agent(s)")
)

// IsNameTaken reports whether err is ErrNameTaken.
func IsNameTaken(err error) bool { return errors.Is(err, ErrNameTaken) }

// visibleTo: public visible to all; private only to creator + admins.
// Role "system" (autonomous run, org-scope) treated as least-privileged
// — only sees public.
func (s *ClaudeAccountService) visibleTo(acc *ResolvedAccount, caller callerInfo) bool {
	if acc.Visibility == "public" {
		return true
	}
	return caller.ID == acc.CreatedBy || caller.Role == "admin"
}

// CreateAccountInput is the input DTO for Create.
type CreateAccountInput struct {
	Name       string
	Visibility string // "public" or "private"; defaults to "public" if empty
	CallerID   int64
}

// Create inserts a new managed claude account, creates its config_dir on disk,
// and appends an audit entry (best-effort).
func (s *ClaudeAccountService) Create(ctx context.Context, in CreateAccountInput) (*ResolvedAccount, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	vis := in.Visibility
	if vis == "" {
		vis = "public"
	}
	if vis != "public" && vis != "private" {
		return nil, ErrInvalidVisibility
	}

	id := uuid.NewString()
	configDir := paths.ClaudeAccountDir(s.dataDir, id)

	// INSERT first: detect unique-name conflicts before touching the filesystem.
	now := time.Now().Unix()
	rowID, err := s.q.CreateClaudeAccount(ctx, store.CreateClaudeAccountParams{
		Name:       in.Name,
		Email:      sql.NullString{},
		ConfigDir:  configDir,
		Visibility: vis,
		Status:     "pending",
		CreatedAt:  now,
		CreatedBy:  sql.NullInt64{Int64: in.CallerID, Valid: in.CallerID > 0},
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrNameTaken
		}
		return nil, err
	}

	// Create the config directory; on failure roll back the DB row.
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		_ = s.q.DeleteClaudeAccount(ctx, rowID)
		return nil, fmt.Errorf("mkdir config_dir: %w", err)
	}

	s.appendAudit(ctx, rowID, in.CallerID, "account.create",
		fmt.Sprintf(`{"name":%q,"visibility":%q}`, in.Name, vis))

	return &ResolvedAccount{
		ID:         rowID,
		Name:       in.Name,
		ConfigDir:  configDir,
		Visibility: vis,
		Status:     "pending",
		CreatedAt:  now,
		CreatedBy:  in.CallerID,
	}, nil
}

// List returns all accounts visible to the caller.
// Admins see every account; members see public + their own private accounts.
func (s *ClaudeAccountService) List(ctx context.Context, caller callerInfo) ([]ResolvedAccount, error) {
	adminFlag := int64(0)
	if caller.Role == "admin" {
		adminFlag = 1
	}
	// Column1 is interface{} (bare ? placeholder); CreatedBy is sql.NullInt64.
	rows, err := s.q.ListClaudeAccountsForCaller(ctx, store.ListClaudeAccountsForCallerParams{
		Column1:   adminFlag,
		CreatedBy: sql.NullInt64{Int64: caller.ID, Valid: caller.ID > 0},
	})
	if err != nil {
		return nil, err
	}
	out := make([]ResolvedAccount, 0, len(rows))
	for _, r := range rows {
		out = append(out, sqlcClaudeAccountToResolved(r))
	}
	return out, nil
}

func sqlcClaudeAccountToResolved(r store.ClaudeAccount) ResolvedAccount {
	return ResolvedAccount{
		ID:         r.ID,
		Name:       r.Name,
		Email:      nullStringToStr(r.Email),
		ConfigDir:  r.ConfigDir,
		Visibility: r.Visibility,
		Status:     r.Status,
		CreatedAt:  r.CreatedAt,
		LastUsedAt: nullInt64ToInt(r.LastUsedAt),
		CreatedBy:  nullInt64ToInt(r.CreatedBy),
	}
}

func nullStringToStr(n sql.NullString) string {
	if n.Valid {
		return n.String
	}
	return ""
}

func nullInt64ToInt(n sql.NullInt64) int64 {
	if n.Valid {
		return n.Int64
	}
	return 0
}

// errForbidden is an alias to the package-level ErrForbidden (declared in authz.go),
// kept under the existing unexported name for legacy internal callers.
var errForbidden = ErrForbidden

type UpdateAccountInput struct {
	ID         int64
	Name       *string
	Visibility *string
	Caller     callerInfo
}

// GetByID fetches a single account row.
func (s *ClaudeAccountService) GetByID(ctx context.Context, id int64) (*ResolvedAccount, error) {
	r, err := s.q.GetClaudeAccount(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAccountNotFound
		}
		return nil, err
	}
	out := ResolvedAccount{
		ID:         r.ID,
		Name:       r.Name,
		Email:      nullStringToStr(r.Email),
		ConfigDir:  r.ConfigDir,
		Visibility: r.Visibility,
		Status:     r.Status,
		CreatedAt:  r.CreatedAt,
		LastUsedAt: nullInt64ToInt(r.LastUsedAt),
		CreatedBy:  nullInt64ToInt(r.CreatedBy),
	}
	return &out, nil
}

// CanAccessClaudeAccount resolves an account by ID and verifies the caller
// is allowed to see it via the existing visibleTo rule (public OR creator OR
// admin; "system" role only sees public). This is the single source of truth
// for per-account read authz on the /api/claude-accounts/:id/* namespace.
//
// Returns:
//   - (*ResolvedAccount, nil) on success
//   - (nil, ErrAccountNotFound) when the row doesn't exist
//   - (nil, ErrNotVisible) when caller cannot see it (handler maps to 404 like
//     mapClaudeAccountServiceError already does, to avoid leaking existence)
func (s *ClaudeAccountService) CanAccessClaudeAccount(ctx context.Context, caller callerInfo, accountID int64) (*ResolvedAccount, error) {
	acc, err := s.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !s.visibleTo(acc, caller) {
		return nil, ErrNotVisible
	}
	return acc, nil
}

// GetDefault returns the default row (config_dir == ''). Errors if missing.
func (s *ClaudeAccountService) GetDefault(ctx context.Context) (*ResolvedAccount, error) {
	r, err := s.q.GetDefaultClaudeAccount(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAccountNotFound
		}
		return nil, err
	}
	return &ResolvedAccount{
		ID:         r.ID,
		Name:       r.Name,
		Email:      nullStringToStr(r.Email),
		ConfigDir:  r.ConfigDir,
		Visibility: r.Visibility,
		Status:     r.Status,
		CreatedAt:  r.CreatedAt,
		LastUsedAt: nullInt64ToInt(r.LastUsedAt),
		CreatedBy:  nullInt64ToInt(r.CreatedBy),
	}, nil
}

// Update changes name and/or visibility. Authz: admin OR creator.
// Default row's visibility cannot be changed away from public.
func (s *ClaudeAccountService) Update(ctx context.Context, in UpdateAccountInput) error {
	row, err := s.GetByID(ctx, in.ID)
	if err != nil {
		return err
	}
	if in.Caller.Role != "admin" && in.Caller.ID != row.CreatedBy {
		return errForbidden
	}
	// Default row: visibility stays public (otherwise resolution chain breaks for non-creators).
	if row.ConfigDir == "" && in.Visibility != nil && *in.Visibility != "public" {
		return ErrInvalidVisibility
	}
	if in.Visibility != nil && *in.Visibility != "public" && *in.Visibility != "private" {
		return ErrInvalidVisibility
	}

	// Two independent UPDATEs (split per IM/I14 — no COALESCE+sqlc.narg).
	if in.Name != nil {
		if err := s.q.UpdateClaudeAccountName(ctx, store.UpdateClaudeAccountNameParams{
			Name: *in.Name, ID: in.ID,
		}); err != nil {
			if isUniqueViolation(err) {
				return ErrNameTaken
			}
			return err
		}
		s.appendAudit(ctx, in.ID, in.Caller.ID, "account.update_name",
			fmt.Sprintf(`{"old":%q,"new":%q}`, row.Name, *in.Name))
	}
	if in.Visibility != nil {
		if err := s.q.UpdateClaudeAccountVisibility(ctx, store.UpdateClaudeAccountVisibilityParams{
			Visibility: *in.Visibility, ID: in.ID,
		}); err != nil {
			return err
		}
		s.appendAudit(ctx, in.ID, in.Caller.ID, "account.update_visibility",
			fmt.Sprintf(`{"old":%q,"new":%q}`, row.Visibility, *in.Visibility))
	}
	return nil
}

// Delete removes an account row + its config_dir. Default row is rejected.
// Authz: admin OR creator. Refuses if any registered SpawnTracker reports
// running agents holding the account's CLAUDE_CONFIG_DIR (C4 / spec IM-7).
func (s *ClaudeAccountService) Delete(ctx context.Context, id int64, caller callerInfo) error {
	row, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if row.ConfigDir == "" {
		return ErrCannotDeleteDefault
	}
	if caller.Role != "admin" && caller.ID != row.CreatedBy {
		return errForbidden
	}

	// In-flight check: refuse delete while config_dir is in use.
	var inUseWorkspaces []int64
	for _, fn := range s.trackers {
		inUseWorkspaces = append(inUseWorkspaces, fn(id)...)
	}
	if len(inUseWorkspaces) > 0 {
		return fmt.Errorf("%w (workspaces=%v)", ErrAccountInUse, inUseWorkspaces)
	}

	// Write the delete audit entry BEFORE removing the row. Otherwise the
	// audit INSERT's FK to claude_accounts(id) would fail under PRAGMA
	// foreign_keys=ON (SQLite default for niuniu) and on Postgres, silently
	// losing the audit trail since appendAudit is best-effort. Same root cause
	// as the codex_account.go I2 fix (2026-05-22 review).
	s.appendAudit(ctx, id, caller.ID, "account.delete",
		fmt.Sprintf(`{"name":%q,"config_dir":%q}`, row.Name, row.ConfigDir))
	if err := s.q.DeleteClaudeAccount(ctx, id); err != nil {
		return err
	}
	// Best-effort fs cleanup; errors only logged, not surfaced.
	_ = os.RemoveAll(row.ConfigDir)
	if s.usageEvictor != nil {
		s.usageEvictor.EvictAccount(id)
	}
	return nil
}

// appendAudit writes a single audit log entry. Best-effort: log on failure
// but never return error to caller. Matches niuniu's existing audit pattern
// (see service/org_audit.go).
func (s *ClaudeAccountService) appendAudit(ctx context.Context, accountID, actorUserID int64, action, payload string) {
	if err := s.q.AppendClaudeAccountAudit(ctx, store.AppendClaudeAccountAuditParams{
		AccountID:   sql.NullInt64{Int64: accountID, Valid: accountID > 0},
		ActorUserID: sql.NullInt64{Int64: actorUserID, Valid: actorUserID > 0},
		Action:      action,
		Payload:     payload,
	}); err != nil {
		slog.Warn("appendClaudeAccountAudit failed", "action", action, "accountID", accountID, "err", err)
	}
}

// SetActive sets (or clears, if accountID == nil) the active claude account
// for (ownerType, ownerID). Org-scope authz check (org owner/admin) is the
// API handler's responsibility — this service trusts whatever caller it gets,
// but verifies (a) personal scope can only be set by the user themselves and
// (b) the target account is visible to caller (so private leakage is blocked).
func (s *ClaudeAccountService) SetActive(
	ctx context.Context, ownerType string, ownerID int64,
	accountID *int64, caller callerInfo,
) error {
	if ownerType == "user" && ownerID != caller.ID {
		return errForbidden
	}
	if accountID == nil {
		if err := s.q.ClearActiveClaudeAccount(ctx, store.ClearActiveClaudeAccountParams{
			OwnerType: ownerType, OwnerID: ownerID,
		}); err != nil {
			return err
		}
		s.appendAudit(ctx, 0, caller.ID, "active.clear",
			fmt.Sprintf(`{"owner_type":%q,"owner_id":%d}`, ownerType, ownerID))
		return nil
	}
	acc, err := s.GetByID(ctx, *accountID)
	if err != nil {
		return err
	}
	if !s.visibleTo(acc, caller) {
		return ErrNotVisible
	}
	if err := s.q.SetActiveClaudeAccount(ctx, store.SetActiveClaudeAccountParams{
		OwnerType: ownerType, OwnerID: ownerID, AccountID: *accountID,
	}); err != nil {
		return err
	}
	s.appendAudit(ctx, *accountID, caller.ID, "active.set",
		fmt.Sprintf(`{"owner_type":%q,"owner_id":%d}`, ownerType, ownerID))
	return nil
}

// GetActive returns the active account for (ownerType, ownerID), or nil if
// none is set. Does NOT apply visibility filtering — that's the caller's
// responsibility (e.g. ResolveForWorkspace falls through to default if
// invisible).
func (s *ClaudeAccountService) GetActive(
	ctx context.Context, ownerType string, ownerID int64,
) (*ResolvedAccount, error) {
	id, err := s.q.GetActiveClaudeAccount(ctx, store.GetActiveClaudeAccountParams{
		OwnerType: ownerType, OwnerID: ownerID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return s.GetByID(ctx, id)
}

// SetWorkspaceOverride sets workspaces.claude_account_id (or clears if
// accountID == nil). Workspace write authz is the API handler's job.
// If accountID points to the default row (config_dir == ''), the override
// is silently cleared instead — overriding to default is meaningless;
// "follow owner active" via NULL is the equivalent intent.
//
// When the account changes and both old and new config dirs are known,
// the workspace's Claude projects directory is moved atomically so that
// --resume on the next spawn works without interruption.
func (s *ClaudeAccountService) SetWorkspaceOverride(
	ctx context.Context, workspaceID int64, accountID *int64, caller callerInfo,
) error {
	newConfigDir := ""
	if accountID != nil {
		acc, err := s.GetByID(ctx, *accountID)
		if err != nil {
			return err
		}
		if acc.ConfigDir == "" {
			accountID = nil // default row → clear instead
		} else if !s.visibleTo(acc, caller) {
			return ErrNotVisible
		} else {
			newConfigDir = acc.ConfigDir
		}
	}

	// Capture workspace path and old account config dir before the DB update,
	// so we can attempt to migrate the session directory.
	workspacePath := ""
	oldConfigDir := ""
	if ws, err := s.q.GetWorkspace(ctx, workspaceID); err == nil {
		workspacePath = ws.Path
		if ws.ClaudeAccountID.Valid {
			if oldAcc, err := s.GetByID(ctx, ws.ClaudeAccountID.Int64); err == nil {
				oldConfigDir = claudeConfigDirOrHome(oldAcc.ConfigDir)
			}
		}
	}

	var idParam sql.NullInt64
	action := "workspace.override_clear"
	payload := fmt.Sprintf(`{"workspace_id":%d}`, workspaceID)
	if accountID != nil {
		idParam = sql.NullInt64{Int64: *accountID, Valid: true}
		action = "workspace.override_set"
		payload = fmt.Sprintf(`{"workspace_id":%d,"account_id":%d}`, workspaceID, *accountID)
	}
	if err := s.q.SetWorkspaceClaudeAccount(ctx, store.SetWorkspaceClaudeAccountParams{
		ID: workspaceID, ClaudeAccountID: idParam,
	}); err != nil {
		return err
	}

	// Attempt to move $oldConfigDir/projects/<hash>/ → $newConfigDir/projects/<hash>/
	// so --resume works immediately under the new account. Fall back to clearing
	// the session when migration is not possible (no source dir, cross-device, etc.).
	migrated := workspacePath != "" &&
		migrateClaudeProjectsDir(workspacePath, oldConfigDir, newConfigDir)
	if !migrated {
		if _, err := s.db.ExecContext(ctx,
			"UPDATE workspaces SET session_id = NULL, session_status = 'idle' WHERE id = ?",
			workspaceID,
		); err != nil {
			slog.Warn("claude account: failed to clear workspace session after account override change",
				"workspaceID", workspaceID, "err", err)
		}
		// Invalidate the in-memory session so the next spawn does not pass
		// --resume with a stale session ID that the new account doesn't know.
		for _, fn := range s.sessionClearers {
			fn(ctx, workspaceID)
		}
	}
	auditAcct := int64(0)
	if accountID != nil {
		auditAcct = *accountID
	}
	s.appendAudit(ctx, auditAcct, caller.ID, action, payload)
	return nil
}

// claudeConfigDirOrHome returns configDir as-is when non-empty, or resolves
// the default account path ($HOME/.claude) when configDir is empty.
func claudeConfigDirOrHome(configDir string) string {
	if configDir != "" {
		return configDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// claudePathHash converts an absolute workspace path to the directory name
// Claude CLI uses under $CLAUDE_CONFIG_DIR/projects/. Claude replaces every
// non-alphanumeric byte with '-', e.g.:
//
//	C:\Users\foo\.niuniu\users\2\workspaces\3
//	→ C--Users-foo--niuniu-users-2-workspaces-3
func claudePathHash(path string) string {
	b := []byte(path)
	for i, c := range b {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			b[i] = '-'
		}
	}
	return string(b)
}

// migrateClaudeProjectsDir moves <oldConfigDir>/projects/<hash> to
// <newConfigDir>/projects/<hash> so the next --resume works under the new
// account. Returns true only when the rename succeeds.
func migrateClaudeProjectsDir(workspacePath, oldConfigDir, newConfigDir string) bool {
	if oldConfigDir == "" || newConfigDir == "" || oldConfigDir == newConfigDir {
		return false
	}
	hash := claudePathHash(workspacePath)
	src := filepath.Join(oldConfigDir, "projects", hash)
	if _, err := os.Stat(src); err != nil {
		return false // nothing to migrate
	}
	if err := os.MkdirAll(filepath.Join(newConfigDir, "projects"), 0o755); err != nil {
		return false
	}
	dst := filepath.Join(newConfigDir, "projects", hash)
	if err := os.Rename(src, dst); err != nil {
		slog.Warn("claude account: failed to migrate session dir",
			"src", src, "dst", dst, "err", err)
		return false
	}
	slog.Info("claude account: migrated session dir", "src", src, "dst", dst)
	return true
}

// ResolveForWorkspace resolves the effective Claude account for a spawn.
// Uses workspaces.claude_account_id directly — the account is bound at
// workspace creation time (defaulting to the owner's active account then).
// If no account is bound (old data not yet migrated, or no accounts exist),
// returns a noop account so the spawn proceeds without CLAUDE_CONFIG_DIR
// injection (CLI uses whatever is globally logged in).
//
// userID semantics:
//   - userID > 0 → load role from users table; unknown user → "member".
//   - userID == 0 → autonomous (scheduler / harness):
//       * workspace.owner_type='user' → caller = (owner_id, "member")
//       * workspace.owner_type='org' → caller = (0, "system")
//         "system" role is treated as least-privileged for visibility
//         (only public accounts visible) — prevents cross-owner private
//         leakage. See spec §"Autonomous run 的 caller 派生".
func (s *ClaudeAccountService) ResolveForWorkspace(
	ctx context.Context, workspaceID, userID int64,
) (*ResolvedAccount, error) {
	caller, ws := s.deriveCaller(ctx, workspaceID, userID)

	if ws != nil && ws.ClaudeAccountID.Valid {
		if acc, err := s.GetByID(ctx, ws.ClaudeAccountID.Int64); err == nil && s.visibleTo(acc, caller) {
			return acc, nil
		}
	}

	slog.Debug("claude account: workspace has no bound account, spawning without CLAUDE_CONFIG_DIR",
		"workspaceID", workspaceID)
	return &ResolvedAccount{ConfigDir: "", Status: "pending"}, nil
}

// ResolveConfigDirForUser picks a Claude account `config_dir` for subprocess
// spawns that aren't bound to a specific workspace (e.g., the per-issue
// goal_condition AI suggest endpoint, which has only a userID).
//
// Returns the chosen account's `config_dir` (may be ""). When the result is
// empty the caller MUST NOT inject CLAUDE_CONFIG_DIR; the CLI then falls
// back to its native ~/.claude home — that's a legitimate, working
// configuration in personal mode, not a missing-data sentinel.
//
// Selection: honors only the operator-configured default from
// Settings → Claude 账号 → "新建工作空间的默认账号" (the
// claude_active_account row written by SetActive). When the user has not
// picked an explicit default (dropdown shows "(无默认账号)"), returns ""
// → handler skips CLAUDE_CONFIG_DIR injection → claude CLI uses ~/.claude.
//
// History: prior versions ranked by last_used_at, which drifted onto side
// accounts when a parallel workspace agent bumped a side account's
// timestamp; and a brief attempt to prefer the migration's __default__
// row hard-coded a UX concept that the dropdown deliberately hides. The
// explicit per-owner active selection in claude_active_account is the
// only signal that matches operator intent here.
func (s *ClaudeAccountService) ResolveConfigDirForUser(ctx context.Context, userID int64) string {
	acc := s.ResolveAccountForUser(ctx, userID)
	if acc == nil {
		return ""
	}
	return acc.ConfigDir
}

// ResolveAccountForUser is the richer variant of ResolveConfigDirForUser:
// returns the full chosen ResolvedAccount (or nil) so callers can surface
// the account name / id in operator-facing diagnostics. Selection rules
// are identical to ResolveConfigDirForUser — both read the same
// claude_active_account row, so the two methods can't diverge.
//
// Returns nil when the user has not picked an explicit default; caller
// MUST NOT inject CLAUDE_CONFIG_DIR (CLI falls back to ~/.claude).
func (s *ClaudeAccountService) ResolveAccountForUser(ctx context.Context, userID int64) *ResolvedAccount {
	if userID <= 0 {
		return nil
	}
	acc, err := s.GetActive(ctx, "user", userID)
	if err != nil {
		// Treat lookup errors as "no active set" rather than propagating —
		// the caller's intent is best-effort spawn, and falling back to
		// ~/.claude is safer than a 500.
		slog.Warn("ResolveAccountForUser: GetActive failed; falling through to ~/.claude",
			"user_id", userID, "err", err)
		return nil
	}
	return acc
}

// deriveCaller returns (caller, workspaceRow). All errors degrade to the
// least-privileged caller; the resolution chain handles missing workspaces.
func (s *ClaudeAccountService) deriveCaller(
	ctx context.Context, workspaceID, userID int64,
) (callerInfo, *store.GetWorkspaceForClaudeResolveRow) {
	wsRow, wsErr := s.q.GetWorkspaceForClaudeResolve(ctx, workspaceID)
	var wsPtr *store.GetWorkspaceForClaudeResolveRow
	if wsErr == nil {
		wsPtr = &wsRow
	}

	if userID > 0 {
		return s.loadUserCaller(ctx, userID), wsPtr
	}

	// Autonomous run — derive from workspace owner.
	if wsPtr == nil {
		// Unknown workspace and unknown user → least privileged.
		return callerInfo{ID: 0, Role: "system"}, nil
	}
	switch wsPtr.OwnerType {
	case "user":
		return callerInfo{ID: wsPtr.OwnerID, Role: "member"}, wsPtr
	default: // "org" or unexpected
		return callerInfo{ID: 0, Role: "system"}, wsPtr
	}
}

// loadUserCaller fetches role from users table. Unknown user → "system" (not
// "member") so a deleted/recycled userID can't accidentally view private
// accounts created by the original holder.
func (s *ClaudeAccountService) loadUserCaller(ctx context.Context, userID int64) callerInfo {
	role, err := s.q.GetUserRole(ctx, userID)
	if err != nil {
		return callerInfo{ID: 0, Role: "system"}
	}
	return callerInfo{ID: userID, Role: role}
}

// RefreshFromDisk re-reads <config_dir>/.claude.json (or ~/.claude.json for
// default row) and updates email + status. Caller must already have verified
// the account is visible to the requester (see handler IM-5 fix).
func (s *ClaudeAccountService) RefreshFromDisk(ctx context.Context, id int64) error {
	row, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	var email string
	var hasCreds bool
	if row.ConfigDir == "" {
		email = claudehome.ReadEmailFromHomeJSON()
		hasCreds = claudehome.CredsExistInHome()
	} else {
		email = claudehome.ReadEmailFromConfigDir(row.ConfigDir)
		hasCreds = claudehome.CredsExistInConfigDir(row.ConfigDir)
	}
	status := "pending"
	if email != "" || hasCreds {
		status = "active"
	}
	if err := s.q.UpdateClaudeAccountAfterLogin(ctx, store.UpdateClaudeAccountAfterLoginParams{
		Email:      sql.NullString{String: email, Valid: email != ""},
		Status:     status,
		LastUsedAt: sql.NullInt64{Int64: time.Now().Unix(), Valid: true},
		ID:         id,
	}); err != nil {
		return err
	}
	if row.Status != "active" && status == "active" {
		s.appendAudit(ctx, id, 0, "account.login_succeeded",
			fmt.Sprintf(`{"email":%q}`, email))
	}
	return nil
}

// AuditLogEntry is a flattened audit row for handler responses.
type AuditLogEntry struct {
	ID          int64  `json:"id"`
	AccountID   int64  `json:"account_id,omitempty"`
	ActorUserID int64  `json:"actor_user_id,omitempty"`
	Action      string `json:"action"`
	Payload     string `json:"payload"`
	CreatedAt   string `json:"created_at"`
}

// ListAuditLog returns up to `limit` audit entries for an account, with id < before.
func (s *ClaudeAccountService) ListAuditLog(ctx context.Context, accountID, before, limit int64) ([]AuditLogEntry, error) {
	rows, err := s.q.ListClaudeAccountAudit(ctx, store.ListClaudeAccountAuditParams{
		AccountID: sql.NullInt64{Int64: accountID, Valid: accountID > 0},
		ID:        before,
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]AuditLogEntry, 0, len(rows))
	for _, r := range rows {
		entry := AuditLogEntry{
			ID:        r.ID,
			Action:    r.Action,
			Payload:   r.Payload,
			CreatedAt: r.CreatedAt.Format(time.RFC3339),
		}
		if r.AccountID.Valid {
			entry.AccountID = r.AccountID.Int64
		}
		if r.ActorUserID.Valid {
			entry.ActorUserID = r.ActorUserID.Int64
		}
		out = append(out, entry)
	}
	return out, nil
}

// MarkLoginFailed sets status='failed' + writes audit. Used by the PTY login
// handler when the spawned `claude /login` exits non-zero.
func (s *ClaudeAccountService) MarkLoginFailed(ctx context.Context, id int64) error {
	s.appendAudit(ctx, id, 0, "account.login_failed", "{}")
	return s.q.UpdateClaudeAccountStatus(ctx, store.UpdateClaudeAccountStatusParams{
		Status: "failed", ID: id,
	})
}

// MarkUsed updates last_used_at fire-and-forget. Errors only logged.
func (s *ClaudeAccountService) MarkUsed(ctx context.Context, accountID int64) {
	if err := s.q.TouchClaudeAccount(ctx, store.TouchClaudeAccountParams{
		ID:         accountID,
		LastUsedAt: sql.NullInt64{Int64: time.Now().Unix(), Valid: true},
	}); err != nil {
		slog.Debug("touchClaudeAccount failed", "id", accountID, "err", err)
	}
}

// ClaudeAccountAgentProxyAdapter adapts ClaudeAccountService to the
// agentproxy.ClaudeAccountResolver interface (multi-value return) so the
// agentproxy package can depend on agentproxy-only types and avoid the
// service ↔ agentproxy import cycle.
//
// Wire-up in server.New:
//
//	claudeSvc := service.NewClaudeAccountService(...)
//	agentProxy.SetClaudeAccountService(&service.ClaudeAccountAgentProxyAdapter{Svc: claudeSvc})
type ClaudeAccountAgentProxyAdapter struct {
	Svc *ClaudeAccountService
}

// ResolveForWorkspace returns (accountID, configDir, error) to match the
// interface in agentproxy. configDir == "" means default (no env injection).
func (a *ClaudeAccountAgentProxyAdapter) ResolveForWorkspace(
	ctx context.Context, workspaceID, userID int64,
) (int64, string, error) {
	acc, err := a.Svc.ResolveForWorkspace(ctx, workspaceID, userID)
	if err != nil {
		return 0, "", err
	}
	return acc.ID, acc.ConfigDir, nil
}

// MarkUsed forwards.
func (a *ClaudeAccountAgentProxyAdapter) MarkUsed(ctx context.Context, accountID int64) {
	a.Svc.MarkUsed(ctx, accountID)
}
