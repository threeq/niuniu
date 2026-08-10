package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// ErrInvalidEmail is returned by GitIdentityService.UpdateEmail when the
// supplied email fails the same minimal RFC-ish regex used by the git
// package, or contains control characters.
var ErrInvalidEmail = errors.New("invalid email")

// ErrInvalidName is returned when a per-(user, repo) override's name
// contains control characters that would break the `git -c user.name=`
// argument or the `git_author/committer_name` environment value. Distinct
// from ErrInvalidEmail so the API can surface a different error code.
var ErrInvalidName = errors.New("invalid name")

// gitIdentityEmailRegex must accept empty (caller clears) AND a minimal "x@y.z".
var gitIdentityEmailRegex = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// fallbackEmailDomain is the synthetic domain used when a user has not set an
// email yet. The .local TLD is reserved (RFC 6761) — anything mailed to it
// cannot leave the host — so commits made with this address won't accidentally
// reach a real inbox if a downstream system tries to notify the "author".
const fallbackEmailDomain = "niuniu.local"

// GitIdentityService maps a niuniu user_id to the (Name, Email) pair niuniu
// will inject as GIT_AUTHOR_* / GIT_COMMITTER_* into agent subprocesses and
// pass to git.CommitAs for server-initiated commits.
//
// Phase 0 stays narrow: read/write users.{display_name,email,username} via
// raw SQL through the driver-aware *store.DB wrapper. No sqlc regen, no
// touching the User model, so the blast radius is one file plus the schema
// migration.
type GitIdentityService struct {
	db *store.DB
}

func NewGitIdentityService(db *sql.DB) *GitIdentityService {
	return &GitIdentityService{db: store.Wrap(db)}
}

// Resolve returns the git author identity for the given user. Falls back to
// "<username>" for Name and "<username>@niuniu.local" for Email when the
// user has not yet configured them. Returns Identity{} (zero) when userID is
// 0 — callers treat that as "don't inject, let git use OS-global config",
// which preserves personal-edition behavior where one OS user owns the box.
func (s *GitIdentityService) Resolve(ctx context.Context, userID int64) (git.Identity, error) {
	if userID == 0 || s == nil || s.db == nil {
		return git.Identity{}, nil
	}
	var username, displayName, email string
	err := s.db.QueryRowContext(ctx,
		`SELECT username, display_name, COALESCE(email, '') FROM users WHERE id = ?`,
		userID,
	).Scan(&username, &displayName, &email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Unknown user_id — degrade to zero identity rather than error.
			// Callers can still spawn the agent; commits will fall through to
			// git's normal config resolution.
			return git.Identity{}, nil
		}
		return git.Identity{}, err
	}
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = username
	}
	if email == "" {
		email = fmt.Sprintf("%s@%s", username, fallbackEmailDomain)
	}
	return git.Identity{Name: name, Email: email}, nil
}

// ResolveNameEmail is the agentproxy-friendly variant of Resolve. Returns
// ("", "", nil) for zero / unknown user_id so agentproxy can stay decoupled
// from internal/git. Defined separately (rather than reusing Resolve) to
// keep the agentproxy interface narrow.
func (s *GitIdentityService) ResolveNameEmail(ctx context.Context, userID int64) (name, email string, err error) {
	id, err := s.Resolve(ctx, userID)
	if err != nil {
		return "", "", err
	}
	return id.Name, id.Email, nil
}

// EnvVars returns the GIT_AUTHOR_NAME/EMAIL + GIT_COMMITTER_NAME/EMAIL pairs
// for injecting into a child process environment. Returns nil when id is
// zero — the caller appends ...nil safely, and an unset env means git falls
// back to repo / OS-global config as before.
func EnvVarsForIdentity(id git.Identity) []string {
	if id.IsZero() {
		return nil
	}
	return []string{
		"GIT_AUTHOR_NAME=" + id.Name,
		"GIT_AUTHOR_EMAIL=" + id.Email,
		"GIT_COMMITTER_NAME=" + id.Name,
		"GIT_COMMITTER_EMAIL=" + id.Email,
	}
}

// UpdateEmail writes users.email after validating the input. Empty string is
// allowed (clears the field; Resolve will fall back to the synthetic
// "<username>@niuniu.local"). Returns ErrInvalidEmail on shape failure or if
// the value contains control characters.
func (s *GitIdentityService) UpdateEmail(ctx context.Context, userID int64, email string) error {
	if userID == 0 {
		return errors.New("userID required")
	}
	email = strings.TrimSpace(email)
	if email != "" {
		if !gitIdentityEmailRegex.MatchString(email) {
			return ErrInvalidEmail
		}
		if strings.ContainsAny(email, "\n\r\x00") {
			return ErrInvalidEmail
		}
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET email = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		email, userID)
	return err
}

// GetEmail returns the raw email value stored for the user (empty string when
// unset). Distinct from Resolve which substitutes the synthetic fallback —
// the settings UI wants to show the actual stored value so the user can tell
// "I haven't set it" from "I set it to <something>".
func (s *GitIdentityService) GetEmail(ctx context.Context, userID int64) (string, error) {
	if userID == 0 || s == nil || s.db == nil {
		return "", nil
	}
	var email string
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(email, '') FROM users WHERE id = ?`, userID,
	).Scan(&email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return email, nil
}

// --- Per-(user, repository) overrides (v5.1 Phase 2 sub-phase A) ---

// RepositoryGitIdentityOverride is the raw row stored in
// user_repository_git_identities. Either name or email may be empty —
// the empty side falls through to the user's global setting at Resolve
// time.
type RepositoryGitIdentityOverride struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	RepositoryID int64  `json:"repository_id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
}

// ResolveForRepository returns the effective identity to use for a
// (user, repository) commit. Resolution order (spec §3.1.6):
//  1. user_repository_git_identities override (any non-empty field)
//  2. users.{display_name, email} global default
//  3. synthetic <username>@niuniu.local fallback
//
// Empty fields in the override fall through to global, so the user can
// override only name or only email.
func (s *GitIdentityService) ResolveForRepository(ctx context.Context, userID, repositoryID int64) (git.Identity, error) {
	if userID == 0 || s == nil || s.db == nil {
		return git.Identity{}, nil
	}
	global, err := s.Resolve(ctx, userID)
	if err != nil {
		return git.Identity{}, err
	}
	if repositoryID == 0 {
		return global, nil
	}
	var name, email string
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(name, ''), COALESCE(email, '')
		 FROM user_repository_git_identities
		 WHERE user_id = ? AND repository_id = ?`,
		userID, repositoryID,
	).Scan(&name, &email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return global, nil
		}
		return git.Identity{}, err
	}
	if name == "" {
		name = global.Name
	}
	if email == "" {
		email = global.Email
	}
	return git.Identity{Name: name, Email: email}, nil
}

// GetOverride returns the stored per-(user, repo) override (no
// global-fallback substitution). Returns nil with no error when no
// override exists — distinct from "exists with empty fields", though the
// shape of the API makes them visually similar.
func (s *GitIdentityService) GetOverride(ctx context.Context, userID, repositoryID int64) (*RepositoryGitIdentityOverride, error) {
	if userID == 0 || repositoryID == 0 || s == nil || s.db == nil {
		return nil, nil
	}
	var o RepositoryGitIdentityOverride
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, repository_id, COALESCE(name, ''), COALESCE(email, '')
		 FROM user_repository_git_identities
		 WHERE user_id = ? AND repository_id = ?`,
		userID, repositoryID,
	).Scan(&o.ID, &o.UserID, &o.RepositoryID, &o.Name, &o.Email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &o, nil
}

// ListOverrides returns all of the user's per-repository overrides.
// Used by the settings UI to render a table of "repos with custom
// identity". Empty list when none configured.
func (s *GitIdentityService) ListOverrides(ctx context.Context, userID int64) ([]RepositoryGitIdentityOverride, error) {
	if userID == 0 || s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, repository_id, COALESCE(name, ''), COALESCE(email, '')
		 FROM user_repository_git_identities
		 WHERE user_id = ?
		 ORDER BY repository_id ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RepositoryGitIdentityOverride{}
	for rows.Next() {
		var o RepositoryGitIdentityOverride
		if err := rows.Scan(&o.ID, &o.UserID, &o.RepositoryID, &o.Name, &o.Email); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// UpsertOverride inserts or replaces a per-(user, repo) override. Empty
// name and empty email are both permitted — empty fields delegate to
// the user's global identity at Resolve time.
//
// Validates email format when non-empty (same regex as UpdateEmail);
// strips embedded NULs from name (no other restriction — display names
// can contain pretty much anything).
func (s *GitIdentityService) UpsertOverride(ctx context.Context, userID, repositoryID int64, name, email string) (*RepositoryGitIdentityOverride, error) {
	if userID == 0 {
		return nil, errors.New("userID required")
	}
	if repositoryID == 0 {
		return nil, errors.New("repositoryID required")
	}
	name = strings.TrimSpace(name)
	if strings.ContainsAny(name, "\n\r\x00") {
		return nil, ErrInvalidName
	}
	email = strings.TrimSpace(email)
	if email != "" {
		if !gitIdentityEmailRegex.MatchString(email) {
			return nil, ErrInvalidEmail
		}
		if strings.ContainsAny(email, "\n\r\x00") {
			return nil, ErrInvalidEmail
		}
	}
	// Two-step: try update; if no row affected, insert. Avoid driver-
	// specific ON CONFLICT / UPSERT — store wrapper handles ? rewrite,
	// but not dialect-specific upsert syntax.
	res, err := s.db.ExecContext(ctx,
		`UPDATE user_repository_git_identities
		 SET name = ?, email = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE user_id = ? AND repository_id = ?`,
		name, email, userID, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("update override: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO user_repository_git_identities (user_id, repository_id, name, email)
			 VALUES (?, ?, ?, ?)`,
			userID, repositoryID, name, email)
		if err != nil {
			return nil, fmt.Errorf("insert override: %w", err)
		}
	}
	return s.GetOverride(ctx, userID, repositoryID)
}

// DeleteOverride removes the per-(user, repo) override; resolution will
// fall back to the user's global identity. Returns sql.ErrNoRows when
// nothing existed — the API handler can map that to 204 (idempotent
// delete) or 404 depending on style.
func (s *GitIdentityService) DeleteOverride(ctx context.Context, userID, repositoryID int64) error {
	if userID == 0 || repositoryID == 0 || s == nil || s.db == nil {
		return errors.New("userID and repositoryID required")
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM user_repository_git_identities
		 WHERE user_id = ? AND repository_id = ?`,
		userID, repositoryID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
