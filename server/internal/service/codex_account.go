package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/niuniu-dev/niuniu/internal/paths"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// ResolvedCodexAccount mirrors store.CodexAccount with nullables flattened.
type ResolvedCodexAccount struct {
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

// CodexAccountService provides CRUD + spawn-time resolution for managed Codex
// CLI accounts. Mirrors ClaudeAccountService but is simpler:
//   - no "default" row (config_dir=”) — unbound workspaces fall back to the
//     CLI's native ~/.codex/ via env omission
//   - no per-owner active-account secondary table — binding is direct via
//     workspaces.codex_account_id
//   - no projects/<hash>/ directory migration on account switch (codex stores
//     state under $CODEX_HOME and does not segregate per-workspace caches)
type CodexAccountService struct {
	db              *store.DB
	q               *store.Queries
	dataDir         string
	tokens          *loginTokenStore
	trackers        []SpawnTrackerFn
	sessionClearers []SessionClearerFn
}

// NewCodexAccountService constructs the service. db must already be store.Wrap'd.
func NewCodexAccountService(db *store.DB, q *store.Queries, dataDir string) *CodexAccountService {
	return &CodexAccountService{db: db, q: q, dataDir: dataDir, tokens: newLoginTokenStore()}
}

// RegisterSpawnTracker / RegisterSessionClearer mirror the claude API.
func (s *CodexAccountService) RegisterSpawnTracker(fn SpawnTrackerFn) {
	s.trackers = append(s.trackers, fn)
}
func (s *CodexAccountService) RegisterSessionClearer(fn SessionClearerFn) {
	s.sessionClearers = append(s.sessionClearers, fn)
}

// IssueLoginToken / VerifyLoginToken mirror claude tokens.
func (s *CodexAccountService) IssueLoginToken(accountID int64) (string, error) {
	return s.tokens.Issue(accountID)
}
func (s *CodexAccountService) VerifyLoginToken(tok string) (int64, error) {
	return s.tokens.Verify(tok)
}

// VisibleTo is the public form for handler-side gating.
func (s *CodexAccountService) VisibleTo(acc *ResolvedCodexAccount, caller callerInfo) bool {
	return s.visibleTo(acc, caller)
}

// Sentinel errors (separate from claude's so handlers can disambiguate).
var (
	ErrCodexNameTaken         = errors.New("codex account: name taken")
	ErrCodexAccountNotFound   = errors.New("codex account: not found")
	ErrCodexNotVisible        = errors.New("codex account: not visible to caller")
	ErrCodexInvalidVisibility = errors.New("codex account: invalid visibility")
	ErrCodexAccountInUse      = errors.New("codex account: in use by running agent(s)")
)

func (s *CodexAccountService) visibleTo(acc *ResolvedCodexAccount, caller callerInfo) bool {
	if acc.Visibility == "public" {
		return true
	}
	return caller.ID == acc.CreatedBy || caller.Role == "admin"
}

// CreateCodexAccountInput is the input DTO for Create.
type CreateCodexAccountInput struct {
	Name       string
	Visibility string
	CallerID   int64
}

// Create inserts a new managed codex account, creates its config_dir on disk,
// and appends an audit entry (best-effort).
func (s *CodexAccountService) Create(ctx context.Context, in CreateCodexAccountInput) (*ResolvedCodexAccount, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	vis := in.Visibility
	if vis == "" {
		vis = "public"
	}
	if vis != "public" && vis != "private" {
		return nil, ErrCodexInvalidVisibility
	}

	id := uuid.NewString()
	configDir := paths.CodexAccountDir(s.dataDir, id)

	now := time.Now().Unix()
	rowID, err := s.q.CreateCodexAccount(ctx, store.CreateCodexAccountParams{
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
			return nil, ErrCodexNameTaken
		}
		return nil, err
	}

	if err := os.MkdirAll(configDir, 0o700); err != nil {
		_ = s.q.DeleteCodexAccount(ctx, rowID)
		return nil, fmt.Errorf("mkdir config_dir: %w", err)
	}

	s.appendAudit(ctx, rowID, in.CallerID, "account.create",
		fmt.Sprintf(`{"name":%q,"visibility":%q}`, in.Name, vis))

	return &ResolvedCodexAccount{
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
func (s *CodexAccountService) List(ctx context.Context, caller callerInfo) ([]ResolvedCodexAccount, error) {
	adminFlag := int64(0)
	if caller.Role == "admin" {
		adminFlag = 1
	}
	rows, err := s.q.ListCodexAccountsForCaller(ctx, store.ListCodexAccountsForCallerParams{
		Column1:   adminFlag,
		CreatedBy: sql.NullInt64{Int64: caller.ID, Valid: caller.ID > 0},
	})
	if err != nil {
		return nil, err
	}
	out := make([]ResolvedCodexAccount, 0, len(rows))
	for _, r := range rows {
		out = append(out, sqlcCodexAccountToResolved(r))
	}
	return out, nil
}

func sqlcCodexAccountToResolved(r store.CodexAccount) ResolvedCodexAccount {
	return ResolvedCodexAccount{
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

// UpdateCodexAccountInput input DTO.
type UpdateCodexAccountInput struct {
	ID         int64
	Name       *string
	Visibility *string
	Caller     callerInfo
}

// GetByID fetches a single account row.
func (s *CodexAccountService) GetByID(ctx context.Context, id int64) (*ResolvedCodexAccount, error) {
	r, err := s.q.GetCodexAccount(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrCodexAccountNotFound
		}
		return nil, err
	}
	out := sqlcCodexAccountToResolved(r)
	return &out, nil
}

// CanAccessCodexAccount resolves an account by ID and verifies the caller is
// allowed to see it.
func (s *CodexAccountService) CanAccessCodexAccount(ctx context.Context, caller callerInfo, accountID int64) (*ResolvedCodexAccount, error) {
	acc, err := s.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !s.visibleTo(acc, caller) {
		return nil, ErrCodexNotVisible
	}
	return acc, nil
}

// Update changes name and/or visibility. Authz: admin OR creator.
func (s *CodexAccountService) Update(ctx context.Context, in UpdateCodexAccountInput) error {
	row, err := s.GetByID(ctx, in.ID)
	if err != nil {
		return err
	}
	if in.Caller.Role != "admin" && in.Caller.ID != row.CreatedBy {
		return errForbidden
	}
	if in.Visibility != nil && *in.Visibility != "public" && *in.Visibility != "private" {
		return ErrCodexInvalidVisibility
	}
	if in.Name != nil {
		if err := s.q.UpdateCodexAccountName(ctx, store.UpdateCodexAccountNameParams{
			Name: *in.Name, ID: in.ID,
		}); err != nil {
			if isUniqueViolation(err) {
				return ErrCodexNameTaken
			}
			return err
		}
		s.appendAudit(ctx, in.ID, in.Caller.ID, "account.update_name",
			fmt.Sprintf(`{"old":%q,"new":%q}`, row.Name, *in.Name))
	}
	if in.Visibility != nil {
		if err := s.q.UpdateCodexAccountVisibility(ctx, store.UpdateCodexAccountVisibilityParams{
			Visibility: *in.Visibility, ID: in.ID,
		}); err != nil {
			return err
		}
		s.appendAudit(ctx, in.ID, in.Caller.ID, "account.update_visibility",
			fmt.Sprintf(`{"old":%q,"new":%q}`, row.Visibility, *in.Visibility))
	}
	return nil
}

// Delete removes an account row + its config_dir. Authz: admin OR creator.
// Refuses if a registered SpawnTracker reports running agents using the
// account's CODEX_HOME. Also clears workspaces.codex_account_id for any rows
// pointing at this account (ON DELETE SET NULL handles FK; this is explicit
// for the audit trail).
func (s *CodexAccountService) Delete(ctx context.Context, id int64, caller callerInfo) error {
	row, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if caller.Role != "admin" && caller.ID != row.CreatedBy {
		return errForbidden
	}

	var inUseWorkspaces []int64
	for _, fn := range s.trackers {
		inUseWorkspaces = append(inUseWorkspaces, fn(id)...)
	}
	if len(inUseWorkspaces) > 0 {
		return fmt.Errorf("%w (workspaces=%v)", ErrCodexAccountInUse, inUseWorkspaces)
	}

	// Pre-clear any workspace bindings (explicit; FK ON DELETE SET NULL is the
	// safety net but we audit the unbind separately).
	if err := s.q.ClearWorkspaceCodexAccountByAccountID(ctx, sql.NullInt64{Int64: id, Valid: true}); err != nil {
		slog.Warn("codex account: failed to clear workspace bindings before delete",
			"accountID", id, "err", err)
	}

	// Write the delete audit entry BEFORE removing the row. Otherwise the
	// audit INSERT's FK to codex_accounts(id) would either fail on
	// PRAGMA foreign_keys=ON (SQLite default for niuniu) and on Postgres,
	// silently losing the audit trail. Review I2 (2026-05-22).
	s.appendAudit(ctx, id, caller.ID, "account.delete",
		fmt.Sprintf(`{"name":%q,"config_dir":%q}`, row.Name, row.ConfigDir))

	if err := s.q.DeleteCodexAccount(ctx, id); err != nil {
		return err
	}
	_ = os.RemoveAll(row.ConfigDir)
	return nil
}

func (s *CodexAccountService) appendAudit(ctx context.Context, accountID, actorUserID int64, action, payload string) {
	if err := s.q.AppendCodexAccountAudit(ctx, store.AppendCodexAccountAuditParams{
		AccountID:   sql.NullInt64{Int64: accountID, Valid: accountID > 0},
		ActorUserID: sql.NullInt64{Int64: actorUserID, Valid: actorUserID > 0},
		Action:      action,
		Payload:     payload,
	}); err != nil {
		slog.Warn("appendCodexAccountAudit failed", "action", action, "accountID", accountID, "err", err)
	}
}

// SetWorkspaceBinding sets workspaces.codex_account_id (or clears if nil).
// Workspace write authz is the API handler's job. Visibility check enforced.
func (s *CodexAccountService) SetWorkspaceBinding(
	ctx context.Context, workspaceID int64, accountID *int64, caller callerInfo,
) error {
	var idParam sql.NullInt64
	action := "workspace.binding_clear"
	payload := fmt.Sprintf(`{"workspace_id":%d}`, workspaceID)
	auditAcct := int64(0)
	if accountID != nil {
		acc, err := s.GetByID(ctx, *accountID)
		if err != nil {
			return err
		}
		if !s.visibleTo(acc, caller) {
			return ErrCodexNotVisible
		}
		idParam = sql.NullInt64{Int64: *accountID, Valid: true}
		auditAcct = *accountID
		action = "workspace.binding_set"
		payload = fmt.Sprintf(`{"workspace_id":%d,"account_id":%d}`, workspaceID, *accountID)
	}
	if err := s.q.SetWorkspaceCodexAccount(ctx, store.SetWorkspaceCodexAccountParams{
		ID: workspaceID, CodexAccountID: idParam,
	}); err != nil {
		return err
	}
	// Switching codex account mid-session: clear in-memory session so next
	// spawn does not pass `--resume` with a session ID the new account
	// doesn't know about. Session row's session_id is left as-is (codex
	// resume is opt-in; the next turn just starts fresh).
	for _, fn := range s.sessionClearers {
		fn(ctx, workspaceID)
	}
	s.appendAudit(ctx, auditAcct, caller.ID, action, payload)
	return nil
}

// ResolveForWorkspace resolves the effective Codex account for spawn.
// Returns nil (not an error) when the workspace has no binding — caller MUST
// skip CODEX_HOME injection so the CLI falls back to ~/.codex/.
func (s *CodexAccountService) ResolveForWorkspace(
	ctx context.Context, workspaceID, userID int64,
) (*ResolvedCodexAccount, error) {
	ws, err := s.q.GetWorkspaceForCodexResolve(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !ws.CodexAccountID.Valid {
		return nil, nil
	}
	caller := s.deriveCaller(ctx, ws.OwnerType, ws.OwnerID, userID)
	acc, err := s.GetByID(ctx, ws.CodexAccountID.Int64)
	if err != nil {
		return nil, err
	}
	if !s.visibleTo(acc, caller) {
		// Private leakage protection: bound account no longer visible to
		// caller (membership changed / account turned private). Skip injection.
		slog.Warn("codex account: bound account not visible to caller; falling back to ~/.codex/",
			"workspaceID", workspaceID, "accountID", acc.ID, "callerID", caller.ID)
		return nil, nil
	}
	return acc, nil
}

func (s *CodexAccountService) deriveCaller(ctx context.Context, ownerType string, ownerID, userID int64) callerInfo {
	if userID > 0 {
		role, err := s.q.GetUserRole(ctx, userID)
		if err != nil {
			return callerInfo{ID: 0, Role: "system"}
		}
		return callerInfo{ID: userID, Role: role}
	}
	switch ownerType {
	case "user":
		return callerInfo{ID: ownerID, Role: "member"}
	default:
		return callerInfo{ID: 0, Role: "system"}
	}
}

// RefreshFromDisk re-reads <config_dir>/auth.json and updates email+status.
// Codex stores OAuth credentials there (vs claude's .claude.json). Best-effort:
// missing/malformed file leaves status='pending' but does not error.
func (s *CodexAccountService) RefreshFromDisk(ctx context.Context, id int64) error {
	row, err := s.GetByID(ctx, id)
	if err != nil {
		return err
	}
	email, hasCreds := readCodexAuth(row.ConfigDir)
	status := "pending"
	if email != "" || hasCreds {
		status = "active"
	}
	if err := s.q.UpdateCodexAccountAfterLogin(ctx, store.UpdateCodexAccountAfterLoginParams{
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

// DefaultCodexAuthStatus describes the host's native CODEX_HOME auth state
// (defaults to ~/.codex/). Used by the settings UI to render a synthetic
// "system default" account row when running in personal/embedded mode.
type DefaultCodexAuthStatus struct {
	Email     string `json:"email"`
	Status    string `json:"status"` // "active" or "pending"
	ConfigDir string `json:"config_dir"`
}

// GetDefaultStatus probes the host's native CODEX_HOME (defaults to
// ~/.codex/) and reports whether `codex auth login` has been completed.
// Owner-agnostic — the host shell's auth applies to anyone driving the
// server in personal mode; in hosted mode the caller (frontend) should
// not render the synthetic default row at all.
func (s *CodexAccountService) GetDefaultStatus(_ context.Context) (DefaultCodexAuthStatus, error) {
	configDir := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return DefaultCodexAuthStatus{}, fmt.Errorf("user home dir: %w", err)
		}
		configDir = filepath.Join(home, ".codex")
	}
	email, hasCreds := readCodexAuth(configDir)
	status := "pending"
	if email != "" || hasCreds {
		status = "active"
	}
	return DefaultCodexAuthStatus{
		Email:     email,
		Status:    status,
		ConfigDir: configDir,
	}, nil
}

// readCodexAuth probes <configDir>/auth.json. Returns (email, hasCreds).
// The file shape from `codex auth login`:
//
//	{"tokens":{"id_token":"...","access_token":"..."},"OPENAI_API_KEY":null,"last_refresh":"..."}
//
// Email lives inside the JWT id_token claims, but we don't decode JWT here —
// presence of any token field is sufficient for active status. The email
// can be filled in by a later /api/codex-accounts/:id/refresh once codex
// exposes profile info, or via a separate sync path.
func readCodexAuth(configDir string) (string, bool) {
	if configDir == "" {
		return "", false
	}
	path := filepath.Join(configDir, "auth.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var v struct {
		Tokens struct {
			IDToken     string `json:"id_token"`
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
		OpenAIAPIKey *string `json:"OPENAI_API_KEY"`
	}
	if json.Unmarshal(b, &v) != nil {
		return "", false
	}
	hasCreds := v.Tokens.IDToken != "" || v.Tokens.AccessToken != "" ||
		(v.OpenAIAPIKey != nil && *v.OpenAIAPIKey != "")
	// Best-effort email decode from id_token JWT payload.
	email := decodeJWTEmail(v.Tokens.IDToken)
	return email, hasCreds
}

// decodeJWTEmail extracts the "email" claim from a JWT payload without
// signature verification (we only need the email for display). Returns "" on
// any failure.
func decodeJWTEmail(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return ""
	}
	// JWT uses base64url without padding.
	payload, err := base64URLDecode(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Email
}

func base64URLDecode(s string) ([]byte, error) {
	// JWT payloads use base64url without padding; RawURLEncoding accepts that.
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// CodexAuditLogEntry shape mirrors AuditLogEntry; kept distinct so handler
// JSON tags can diverge later if needed.
type CodexAuditLogEntry struct {
	ID          int64  `json:"id"`
	AccountID   int64  `json:"account_id,omitempty"`
	ActorUserID int64  `json:"actor_user_id,omitempty"`
	Action      string `json:"action"`
	Payload     string `json:"payload"`
	CreatedAt   string `json:"created_at"`
}

// ListAuditLog returns up to `limit` audit entries for an account.
func (s *CodexAccountService) ListAuditLog(ctx context.Context, accountID, before, limit int64) ([]CodexAuditLogEntry, error) {
	rows, err := s.q.ListCodexAccountAudit(ctx, store.ListCodexAccountAuditParams{
		AccountID: sql.NullInt64{Int64: accountID, Valid: accountID > 0},
		ID:        before,
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]CodexAuditLogEntry, 0, len(rows))
	for _, r := range rows {
		entry := CodexAuditLogEntry{
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

// MarkLoginFailed sets status='failed' + writes audit.
func (s *CodexAccountService) MarkLoginFailed(ctx context.Context, id int64) error {
	s.appendAudit(ctx, id, 0, "account.login_failed", "{}")
	return s.q.UpdateCodexAccountStatus(ctx, store.UpdateCodexAccountStatusParams{
		Status: "failed", ID: id,
	})
}

// MarkUsed updates last_used_at fire-and-forget.
func (s *CodexAccountService) MarkUsed(ctx context.Context, accountID int64) {
	if err := s.q.TouchCodexAccount(ctx, store.TouchCodexAccountParams{
		ID:         accountID,
		LastUsedAt: sql.NullInt64{Int64: time.Now().Unix(), Valid: true},
	}); err != nil {
		slog.Debug("touchCodexAccount failed", "id", accountID, "err", err)
	}
}

// CodexAccountAgentProxyAdapter adapts CodexAccountService to a simple
// resolver interface so the agentproxy package can depend on a small
// interface and avoid the service ↔ agentproxy import cycle.
type CodexAccountAgentProxyAdapter struct {
	Svc *CodexAccountService
}

// ResolveForWorkspace returns (accountID, configDir, error). configDir=""
// means the workspace has no binding (caller MUST skip CODEX_HOME injection).
func (a *CodexAccountAgentProxyAdapter) ResolveForWorkspace(
	ctx context.Context, workspaceID, userID int64,
) (int64, string, error) {
	if a == nil || a.Svc == nil {
		return 0, "", nil
	}
	acc, err := a.Svc.ResolveForWorkspace(ctx, workspaceID, userID)
	if err != nil {
		return 0, "", err
	}
	if acc == nil {
		return 0, "", nil
	}
	return acc.ID, acc.ConfigDir, nil
}

// MarkUsed proxies to the underlying service so spawn paths can call MarkUsed
// through the interface without importing the concrete type.
func (a *CodexAccountAgentProxyAdapter) MarkUsed(ctx context.Context, accountID int64) {
	if a == nil || a.Svc == nil {
		return
	}
	a.Svc.MarkUsed(ctx, accountID)
}
