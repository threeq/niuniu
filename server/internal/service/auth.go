package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/auth"
	"github.com/niuniu-dev/niuniu/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	MFAToken     string `json:"mfa_token,omitempty"`
	MFARequired  bool   `json:"mfa_required,omitempty"`
}

// AuthError is the typed error returned by AuthService for failures that the
// HTTP handler needs to distinguish (lockouts, weak passwords). Other
// errors are returned as generic error and mapped to 500 by the handler.
type AuthError struct {
	Code        string
	Message     string
	RetryAfter  time.Duration
	FailedRules []string
}

func (e *AuthError) Error() string { return e.Message }

const (
	CodeInvalidCredentials = "INVALID_CREDENTIALS"
	CodeAccountLocked      = "ACCOUNT_LOCKED"
	CodeIPLocked           = "IP_LOCKED"
	CodeWeakPassword       = "WEAK_PASSWORD"
)

var errInvalidCredentials = &AuthError{Code: CodeInvalidCredentials, Message: "invalid credentials"}

// dummyBcryptHash is precomputed once and used by LoginWithMeta on the
// unknown-username path so an attacker cannot enumerate valid usernames by
// measuring response time. Hashing cost matches the real user path; init
// cost is paid once (~70 ms on a typical box).
var dummyBcryptHash []byte

func init() {
	h, err := bcrypt.GenerateFromPassword([]byte("anti-enumeration-sentinel"), bcrypt.DefaultCost)
	if err != nil {
		// init-time bcrypt failure is unrealistic in practice; fall back to a
		// known-invalid hash so CompareHashAndPassword still spends some time.
		dummyBcryptHash = []byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvalidi.")
		return
	}
	dummyBcryptHash = h
}

type AuthService struct {
	q             *store.Queries
	db            *store.DB
	secret        string
	tokenExpiry   time.Duration
	refreshExpiry time.Duration
	audit         *LoginAudit
	policy        auth.Policy
	MFA           *MFAService
}

func NewAuthService(q *store.Queries, db *sql.DB, secret, tokenExpiry, refreshExpiry string) *AuthService {
	te, err := time.ParseDuration(tokenExpiry)
	if err != nil || te == 0 {
		if err != nil {
			slog.Warn("invalid auth.token_expiry, using default 15m", "value", tokenExpiry, "error", err)
		}
		te = 15 * time.Minute
	}
	re, err := time.ParseDuration(refreshExpiry)
	if err != nil || re == 0 {
		if err != nil {
			slog.Warn("invalid auth.refresh_expiry, using default 168h", "value", refreshExpiry, "error", err)
		}
		re = 7 * 24 * time.Hour
	}
	return &AuthService{
		q:             q,
		db:            store.Wrap(db),
		secret:        secret,
		tokenExpiry:   te,
		refreshExpiry: re,
		audit:         NewLoginAudit(q, DefaultLockoutConfig()),
		policy:        auth.DefaultPolicy(),
	}
}

// SetPolicy overrides the password policy. Exposed so tests and operators
// can relax / tighten the policy without rebuilding the service.
func (s *AuthService) SetPolicy(p auth.Policy) { s.policy = p }

// Audit returns the embedded LoginAudit. Exposed for handlers that need to
// list login history.
func (s *AuthService) Audit() *LoginAudit { return s.audit }

// Secret returns the JWT signing secret. Exposed for MFA handler.
func (s *AuthService) Secret() string { return s.secret }

// GenerateTokenPair is the public wrapper around generateTokenPair for
// callers outside this package (e.g. MFA handler).
func (s *AuthService) GenerateTokenPair(ctx context.Context, user store.User) (*TokenPair, error) {
	return s.generateTokenPair(ctx, user)
}

func (s *AuthService) CreateUser(ctx context.Context, username, password, displayName, role string) (store.User, error) {
	if err := s.validatePassword(password, username, displayName); err != nil {
		return store.User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return store.User{}, fmt.Errorf("hash password: %w", err)
	}
	return s.q.CreateUser(ctx, store.CreateUserParams{
		Username:     username,
		PasswordHash: string(hash),
		DisplayName:  displayName,
		Role:         role,
	})
}

// validatePassword wraps the policy and converts a *auth.PasswordError into
// an *AuthError so handlers can map both via errors.As(err, &authErr).
func (s *AuthService) validatePassword(password, username, displayName string) error {
	err := s.policy.Validate(password, auth.Identity{Username: username, DisplayName: displayName})
	if err == nil {
		return nil
	}
	var pe *auth.PasswordError
	if errors.As(err, &pe) {
		return &AuthError{
			Code:        CodeWeakPassword,
			Message:     "password does not meet policy",
			FailedRules: pe.FailedRules,
		}
	}
	return err
}

// Login is the legacy entry point — no ip/ua metadata, no lockout enforcement
// against IP. New callers should use LoginWithMeta. Kept for tests + scripts.
func (s *AuthService) Login(ctx context.Context, username, password string) (*TokenPair, error) {
	return s.LoginWithMeta(ctx, username, password, "", "", "")
}

// LoginWithMeta runs the full lockout + audit + bcrypt flow. ip and ua may be
// empty (e.g. in tests). The returned error is either a generic error
// (translates to 500/401) or an *AuthError with one of the documented codes.
func (s *AuthService) LoginWithMeta(ctx context.Context, username, password, ip, ua string, mfaTrustCookie string) (*TokenPair, error) {
	user, errUser := s.q.GetUserByUsername(ctx, username)
	var (
		userPtr *store.User
		userID  sql.NullInt64
	)
	if errUser == nil {
		userPtr = &user
		userID = sql.NullInt64{Int64: user.ID, Valid: true}
	} else if !errors.Is(errUser, sql.ErrNoRows) {
		// Surface real DB errors; do not record an attempt row because the
		// lookup itself is unreliable.
		return nil, fmt.Errorf("lookup user: %w", errUser)
	}

	check, err := s.audit.CheckPreLogin(ctx, userPtr, ip)
	if err != nil {
		slog.Warn("login pre-check failed", "username", username, "ip", ip, "error", err)
	}
	if check.Locked {
		s.audit.RecordAttempt(ctx, userID, username, ip, ua, check.Reason, false)
		return nil, &AuthError{
			Code:       lockoutCode(check.Reason),
			Message:    "account or ip temporarily locked",
			RetryAfter: check.RetryAfter,
		}
	}

	if userPtr == nil {
		// Constant-time defence: compare against a dummy hash so the unknown-
		// username branch costs roughly the same as a known-user-wrong-password
		// branch. Without this, response latency leaks whether a username exists.
		_ = bcrypt.CompareHashAndPassword(dummyBcryptHash, []byte(password))
		s.audit.RecordAttempt(ctx, sql.NullInt64{}, username, ip, ua, "unknown_user", false)
		if err := s.audit.OnFailedAttempt(ctx, nil, ip); err != nil {
			slog.Warn("OnFailedAttempt (unknown user) failed", "ip", ip, "error", err)
		}
		return nil, errInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		s.audit.RecordAttempt(ctx, userID, username, ip, ua, "bad_password", false)
		if err := s.audit.OnFailedAttempt(ctx, userPtr, ip); err != nil {
			slog.Warn("OnFailedAttempt failed", "user_id", user.ID, "ip", ip, "error", err)
		}
		return nil, errInvalidCredentials
	}

	s.audit.RecordAttempt(ctx, userID, username, ip, ua, "ok", true)
	if err := s.audit.OnSuccessfulLogin(ctx, user.ID, ip); err != nil {
		// Audit follow-up is best-effort; do not fail the login on it.
		slog.Warn("OnSuccessfulLogin failed", "user_id", user.ID, "ip", ip, "error", err)
	}

	// MFA gate: if enabled and no valid trusted-device cookie, return a
	// short-lived mfa_token. Client must POST /mfa/verify to proceed.
	if user.MfaEnabled == 1 && s.MFA != nil {
		if mfaTrustCookie != "" {
			if trustedUID, ok := s.MFA.CheckTrustedDevice(ctx, mfaTrustCookie); ok && trustedUID == user.ID {
				return s.generateTokenPair(ctx, user)
			}
		}
		mfaToken, err := auth.GenerateMFAStateToken(user.ID, s.secret, 5*time.Minute)
		if err != nil {
			return nil, fmt.Errorf("generate mfa token: %w", err)
		}
		return &TokenPair{
			AccessToken:  "",
			RefreshToken: "",
			ExpiresIn:    0,
			MFAToken:     mfaToken,
			MFARequired:  true,
		}, nil
	}

	return s.generateTokenPair(ctx, user)
}

// lockoutCode maps the LoginAudit Reason field to the public error code.
func lockoutCode(reason string) string {
	switch reason {
	case "account_locked":
		return CodeAccountLocked
	case "ip_locked":
		return CodeIPLocked
	default:
		return CodeInvalidCredentials
	}
}

func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	hash := hashToken(refreshToken)
	rt, err := s.q.GetRefreshToken(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("invalid refresh token")
	}

	// Check expiry BEFORE deleting — if expired, delete and reject
	if !rt.ExpiresAt.IsZero() && time.Now().After(rt.ExpiresAt) {
		s.q.DeleteRefreshToken(ctx, hash) //nolint:errcheck
		return nil, fmt.Errorf("refresh token expired")
	}

	// Delete old token (single-use rotation) — only after expiry check passes
	s.q.DeleteRefreshToken(ctx, hash) //nolint:errcheck

	user, err := s.q.GetUserByID(ctx, rt.UserID)
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	return s.generateTokenPair(ctx, user)
}

func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	return s.q.DeleteRefreshToken(ctx, hashToken(refreshToken))
}

func (s *AuthService) GetUser(ctx context.Context, userID int64) (store.User, error) {
	return s.q.GetUserByID(ctx, userID)
}

func (s *AuthService) ListUsers(ctx context.Context) ([]store.ListUsersRow, error) {
	return s.q.ListUsers(ctx)
}

func (s *AuthService) DeleteUser(ctx context.Context, id int64) error {
	target, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		return err
	}
	if target.Role == "admin" {
		count, err := s.countAdmins(ctx)
		if err != nil {
			return err
		}
		if count <= 1 {
			return fmt.Errorf("cannot remove last admin")
		}
	}

	// Refuse if user is a member of any org. The CLI does the same check —
	// SPA delete must not silently strip org memberships (org_members.user_id
	// is ON DELETE CASCADE so the rows would vanish without an audit entry).
	if slugs, err := s.userOrgSlugs(ctx, id); err != nil {
		return err
	} else if len(slugs) > 0 {
		return fmt.Errorf("user is a member of org(s): %s — remove from all orgs first", strings.Join(slugs, ", "))
	}

	// Refuse if user owns any personal-scoped top-level resource. owner_id has
	// no FK constraint, so deletion would orphan the rows (and the on-disk
	// ~/.niuniu/users/<id>/ directory).
	if tbl, err := s.userOwnedResource(ctx, id); err != nil {
		return err
	} else if tbl != "" {
		return fmt.Errorf("user owns personal resources in %s — transfer or delete them first", tbl)
	}

	s.q.DeleteUserRefreshTokens(ctx, id) //nolint:errcheck
	return s.q.DeleteUser(ctx, id)
}

// userOrgSlugs returns the slugs of orgs the user is a member of.
func (s *AuthService) userOrgSlugs(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT o.slug FROM organizations o JOIN org_members m ON o.id = m.org_id WHERE m.user_id = ?`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var slugs []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		slugs = append(slugs, slug)
	}
	return slugs, rows.Err()
}

// userOwnedResource returns the first table name where the user owns a
// personal-scoped row, or "" if none.
func (s *AuthService) userOwnedResource(ctx context.Context, userID int64) (string, error) {
	// harness_specs is intentionally absent: it is a single global library, not
	// an owner-scoped resource, so it never blocks user deletion.
	tables := []string{
		"projects", "repositories", "workspaces",
		"env_presets", "quick_actions",
		"agents",
	}
	for _, tbl := range tables {
		var dummy int64
		err := s.db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT 1 FROM %s WHERE owner_type='user' AND owner_id=? LIMIT 1`, tbl),
			userID).Scan(&dummy)
		if err == nil {
			return tbl, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	return "", nil
}

// UpdateUser updates display_name and/or role. Either or both pointers may be
// non-nil; at least one must be. callerID is the user invoking the update —
// used for the self-demote guard.
//
// Note: the design doc spells the non-admin role as "user", but the actual
// users.role CHECK constraint is ('admin', 'member', 'viewer'). We accept
// "admin" or "member" here to match the schema. Subsequent tasks should use
// "member" consistently.
//
// Returns the updated user, or an error: "no fields to update", "invalid role",
// "cannot demote yourself", "cannot remove last admin", or sql.ErrNoRows.
func (s *AuthService) UpdateUser(ctx context.Context, id, callerID int64, displayName, role *string) (store.User, error) {
	if displayName == nil && role == nil {
		return store.User{}, fmt.Errorf("no fields to update")
	}
	if role != nil && *role != "admin" && *role != "member" {
		return store.User{}, fmt.Errorf("invalid role: %s", *role)
	}

	current, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		return store.User{}, err
	}

	// Compute final values (pointer = "change", nil = "keep current").
	finalDisplay := current.DisplayName
	if displayName != nil {
		finalDisplay = *displayName
	}
	finalRole := current.Role
	if role != nil {
		finalRole = *role
	}

	// Self-demote guard — admin cannot drop self to member.
	if id == callerID && current.Role == "admin" && finalRole != "admin" {
		return store.User{}, fmt.Errorf("cannot demote yourself")
	}

	// Last-admin guard — if we're demoting an admin, ensure another remains.
	if current.Role == "admin" && finalRole != "admin" {
		count, err := s.countAdmins(ctx)
		if err != nil {
			return store.User{}, err
		}
		if count <= 1 {
			return store.User{}, fmt.Errorf("cannot remove last admin")
		}
	}

	if err := s.q.UpdateUser(ctx, store.UpdateUserParams{
		DisplayName: finalDisplay,
		Role:        finalRole,
		ID:          id,
	}); err != nil {
		return store.User{}, err
	}
	return s.q.GetUserByID(ctx, id)
}

func (s *AuthService) countAdmins(ctx context.Context) (int64, error) {
	users, err := s.q.ListUsers(ctx)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, u := range users {
		if u.Role == "admin" {
			n++
		}
	}
	return n, nil
}

// ResetPassword sets a new password for the user (admin-driven flow) and
// revokes all existing refresh tokens, forcing re-login on every device.
// New password is validated against the configured password policy.
func (s *AuthService) ResetPassword(ctx context.Context, id int64, password string) error {
	target, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.validatePassword(password, target.Username, target.DisplayName); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := s.q.UpdateUserPasswordWithTimestamp(ctx, store.UpdateUserPasswordWithTimestampParams{
		PasswordHash: string(hash),
		ID:           id,
	}); err != nil {
		return err
	}
	// Best-effort token revocation; ignore error so a missing-token edge case
	// does not block password reset itself.
	s.q.DeleteUserRefreshTokens(ctx, id) //nolint:errcheck
	return nil
}

// ChangePassword lets a logged-in user rotate their own password. The current
// password is required (validated via bcrypt); the new password is enforced
// against the configured policy. On success, all other refresh tokens are
// revoked — sessions on other devices must re-authenticate.
func (s *AuthService) ChangePassword(ctx context.Context, userID int64, currentPassword, newPassword string) error {
	user, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)); err != nil {
		return errInvalidCredentials
	}
	if err := s.validatePassword(newPassword, user.Username, user.DisplayName); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := s.q.UpdateUserPasswordWithTimestamp(ctx, store.UpdateUserPasswordWithTimestampParams{
		PasswordHash: string(hash),
		ID:           userID,
	}); err != nil {
		return err
	}
	s.q.DeleteUserRefreshTokens(ctx, userID) //nolint:errcheck
	return nil
}

func (s *AuthService) generateTokenPair(ctx context.Context, user store.User) (*TokenPair, error) {
	claims := &auth.Claims{
		UserID:   user.ID,
		Username: user.Username,
		Role:     user.Role,
	}
	accessToken, err := auth.GenerateAccessToken(claims, s.secret, s.tokenExpiry)
	if err != nil {
		return nil, fmt.Errorf("generate access token: %w", err)
	}

	refreshToken, err := auth.GenerateRefreshToken()
	if err != nil {
		return nil, err
	}
	hash := hashToken(refreshToken)
	expiresAt := time.Now().Add(s.refreshExpiry)

	_, err = s.q.CreateRefreshToken(ctx, store.CreateRefreshTokenParams{
		UserID:    user.ID,
		TokenHash: hash,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, fmt.Errorf("store refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int(s.tokenExpiry.Seconds()),
	}, nil
}

// SeedUsers upserts each configured user into the database. If the user
// already exists, its password/display_name/role are updated to match the
// config. This makes config.yaml the source of truth: edits to a user's
// password take effect on the next startup instead of being silently ignored.
func (s *AuthService) SeedUsers(ctx context.Context, users []struct {
	Username, Password, DisplayName, Role string
}) error {
	for _, u := range users {
		if u.Username == "" || u.Password == "" {
			continue
		}
		role := u.Role
		if role == "" {
			role = "admin"
		}
		displayName := u.DisplayName
		if displayName == "" {
			displayName = u.Username
		}

		existing, err := s.q.GetUserByUsername(ctx, u.Username)
		if err != nil {
			// Seed bypasses the password policy: config-driven bootstrapping
			// must work even with legacy weak passwords. We warn so operators
			// see the smell in logs.
			if perr := s.policy.Validate(u.Password, auth.Identity{Username: u.Username, DisplayName: displayName}); perr != nil {
				slog.Warn("seeded user password does not meet policy", "username", u.Username, "error", perr)
			}
			hash, hashErr := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
			if hashErr != nil {
				return fmt.Errorf("hash password for %q: %w", u.Username, hashErr)
			}
			if _, err := s.q.CreateUser(ctx, store.CreateUserParams{
				Username:     u.Username,
				PasswordHash: string(hash),
				DisplayName:  displayName,
				Role:         role,
			}); err != nil {
				return fmt.Errorf("seed user %q: %w", u.Username, err)
			}
			continue
		}

		// Password changed? bcrypt.CompareHashAndPassword returns nil on match.
		if err := bcrypt.CompareHashAndPassword([]byte(existing.PasswordHash), []byte(u.Password)); err != nil {
			newHash, hashErr := bcrypt.GenerateFromPassword([]byte(u.Password), bcrypt.DefaultCost)
			if hashErr != nil {
				return fmt.Errorf("hash password for %q: %w", u.Username, hashErr)
			}
			if err := s.q.UpdateUserPassword(ctx, store.UpdateUserPasswordParams{
				ID:           existing.ID,
				PasswordHash: string(newHash),
			}); err != nil {
				return fmt.Errorf("update password for %q: %w", u.Username, err)
			}
		}

		if existing.DisplayName != displayName || existing.Role != role {
			if err := s.q.UpdateUser(ctx, store.UpdateUserParams{
				ID:          existing.ID,
				DisplayName: displayName,
				Role:        role,
			}); err != nil {
				return fmt.Errorf("update user %q: %w", u.Username, err)
			}
		}
	}
	return nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
