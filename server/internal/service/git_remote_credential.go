// Package service — GitRemoteCredentialService manages per-(user, host)
// SSH private keys used to authenticate git remote operations (push, pull,
// fetch, clone) from inside a niuniu workspace PTY.
//
// Spec: docs/superpowers/specs/2026-05-19-per-user-git-identity-design.md
//   v5 Phase 2 sub-phase A — storage + management API only. PTY-side
//   GIT_SSH_COMMAND materialization is sub-phase B (next session).
//
// Storage shape: the private key is AES-GCM-encrypted (reusing the
// integration keyring) and stored in `git_remote_credentials.encrypted`.
// The matching public key and SHA256 fingerprint are derived at create
// time so the UI can render them without touching the encrypted blob.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/store"
	"golang.org/x/crypto/ssh"
)

var (
	// ErrInvalidPrivateKey is returned when the supplied PEM cannot be
	// parsed as an SSH private key (wrong format, encrypted-but-passphrase
	// missing, truncated, etc.). The exact PEM error is intentionally NOT
	// leaked to the user — only the fingerprint of the failure is logged.
	ErrInvalidPrivateKey = errors.New("invalid SSH private key")

	// ErrPassphraseRequired is returned when the PEM is encrypted but
	// the caller did not pass a passphrase. niuniu does not store
	// passphrases — the user must decrypt the key locally before
	// pasting it into niuniu.
	ErrPassphraseRequired = errors.New("passphrase-protected SSH keys are not supported; decrypt the key locally first")

	// ErrInvalidHost is returned for empty / whitespace-only host or one
	// containing characters that would break a `Host` line in
	// ssh_config (spaces, control chars).
	ErrInvalidHost = errors.New("invalid host")

	// ErrHostConflict maps the UNIQUE(user_id, host) constraint violation
	// to a stable error code for the API layer.
	ErrHostConflict = errors.New("a credential for this host already exists")
)

// GitRemoteCredential is the redacted in-process representation of a
// stored credential. Private key never appears in this struct.
type GitRemoteCredential struct {
	ID          int64        `json:"id"`
	UserID      int64        `json:"user_id"`
	Host        string       `json:"host"`
	Username    string       `json:"username"`
	PublicKey   string       `json:"public_key"`
	Fingerprint string       `json:"fingerprint"`
	LastUsedAt  sql.NullTime `json:"-"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// LastUsedAtTime returns the LastUsedAt value as a pointer for JSON
// encoding (nil when NULL).
func (g GitRemoteCredential) LastUsedAtTime() *time.Time {
	if !g.LastUsedAt.Valid {
		return nil
	}
	return &g.LastUsedAt.Time
}

// GitRemoteCredentialService is the service-layer entry point. Holds a
// wrapped *store.DB and the integration keyring for AES-GCM encrypt /
// decrypt.
type GitRemoteCredentialService struct {
	db      *store.DB
	keyring *crypto.Keyring
}

func NewGitRemoteCredentialService(rawDB *sql.DB, kr *crypto.Keyring) *GitRemoteCredentialService {
	return &GitRemoteCredentialService{db: store.Wrap(rawDB), keyring: kr}
}

// CreateInput is what handlers pass to Create.
type CreateGitRemoteCredentialInput struct {
	UserID     int64
	Host       string
	Username   string // optional; defaults to "git"
	PrivateKey string // PEM-encoded; must not be passphrase-encrypted
}

// Create parses the private key, derives the matching public key +
// fingerprint, encrypts the private key, and inserts a new row.
// Returns:
//   - ErrInvalidPrivateKey when the PEM is malformed
//   - ErrPassphraseRequired when the PEM is encrypted
//   - ErrInvalidHost when the host fails validation
//   - ErrHostConflict when (user_id, host) already exists
func (s *GitRemoteCredentialService) Create(ctx context.Context, in CreateGitRemoteCredentialInput) (*GitRemoteCredential, error) {
	host := strings.TrimSpace(in.Host)
	if err := validateHost(host); err != nil {
		return nil, err
	}
	username := strings.TrimSpace(in.Username)
	if username == "" {
		username = "git"
	}
	if err := validateSSHUsername(username); err != nil {
		return nil, err
	}

	pubKey, fingerprint, err := parsePrivateKey([]byte(in.PrivateKey))
	if err != nil {
		return nil, err
	}

	ciphertext, err := s.keyring.Encrypt([]byte(strings.TrimSpace(in.PrivateKey) + "\n"))
	if err != nil {
		return nil, fmt.Errorf("encrypt private key: %w", err)
	}

	// `RETURNING id, created_at, updated_at` makes the insert one round-trip
	// on both SQLite (3.35+) and Postgres; store.DB rewrites placeholders.
	var (
		id        int64
		createdAt time.Time
		updatedAt time.Time
	)
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO git_remote_credentials
		  (user_id, host, username, encrypted, public_key, fingerprint)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		in.UserID, host, username, string(ciphertext), pubKey, fingerprint,
	).Scan(&id, &createdAt, &updatedAt)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrHostConflict
		}
		return nil, fmt.Errorf("insert git_remote_credentials: %w", err)
	}
	return &GitRemoteCredential{
		ID:          id,
		UserID:      in.UserID,
		Host:        host,
		Username:    username,
		PublicKey:   pubKey,
		Fingerprint: fingerprint,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}, nil
}

// List returns all of the caller's credentials. Encrypted private key is
// NEVER returned — only the redacted metadata.
func (s *GitRemoteCredentialService) List(ctx context.Context, userID int64) ([]GitRemoteCredential, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, host, username, public_key, fingerprint,
		       last_used_at, created_at, updated_at
		FROM git_remote_credentials
		WHERE user_id = ?
		ORDER BY host ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list git_remote_credentials: %w", err)
	}
	defer rows.Close()
	out := []GitRemoteCredential{}
	for rows.Next() {
		var c GitRemoteCredential
		if err := rows.Scan(&c.ID, &c.UserID, &c.Host, &c.Username, &c.PublicKey,
			&c.Fingerprint, &c.LastUsedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Delete removes a credential. Verifies caller owns it (404 if not, to
// avoid leaking whether the ID exists for a different user).
func (s *GitRemoteCredentialService) Delete(ctx context.Context, userID int64, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM git_remote_credentials WHERE id = ? AND user_id = ?`,
		id, userID)
	if err != nil {
		return fmt.Errorf("delete git_remote_credentials: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DecryptForUserHost decrypts the private key for a (user, host) pair.
// Intended for sub-phase B — PTY spawn materializes keys to disk + builds
// ssh config. Returns sql.ErrNoRows when no credential exists.
//
// Bumps last_used_at to NOW() as a side effect so the UI / admin tools
// can show "when was this key last used".
func (s *GitRemoteCredentialService) DecryptForUserHost(ctx context.Context, userID int64, host string) (privateKey []byte, username string, err error) {
	var (
		encrypted string
		uname     string
	)
	err = s.db.QueryRowContext(ctx, `
		SELECT encrypted, username FROM git_remote_credentials
		WHERE user_id = ? AND host = ?`, userID, host).Scan(&encrypted, &uname)
	if err != nil {
		return nil, "", err
	}
	pt, err := s.keyring.Decrypt([]byte(encrypted))
	if err != nil {
		return nil, "", fmt.Errorf("decrypt private key: %w", err)
	}
	// Best-effort: don't fail the call if the timestamp update fails.
	_, _ = s.db.ExecContext(ctx,
		`UPDATE git_remote_credentials SET last_used_at = CURRENT_TIMESTAMP
		 WHERE user_id = ? AND host = ?`, userID, host)
	return pt, uname, nil
}

// --- helpers ---

func validateHost(h string) error {
	if h == "" {
		return ErrInvalidHost
	}
	// Hosts must be a single token; spaces, tabs, control chars all break
	// the `Host` directive in ssh_config and would invite injection if a
	// future sub-phase builds the config from string concatenation.
	for _, r := range h {
		if r < 0x21 || r == '#' || r == '"' || r == '\\' {
			return ErrInvalidHost
		}
	}
	// We accept '*' as a wildcard for "use this key for any host without a
	// more specific match" — ssh_config supports it; the UI labels it
	// explicitly.
	return nil
}

func validateSSHUsername(u string) error {
	if u == "" {
		return ErrInvalidHost // reuse — username share the "no whitespace / control" rule
	}
	for _, r := range u {
		if r < 0x21 {
			return ErrInvalidHost
		}
	}
	return nil
}

// parsePrivateKey parses a PEM-encoded SSH private key (OpenSSH, RSA, EC,
// or Ed25519), returns the SSH "authorized_keys"-format public key and
// the SHA256 fingerprint. Encrypted (passphrase-protected) PEMs return
// ErrPassphraseRequired.
func parsePrivateKey(pem []byte) (publicKey, fingerprint string, err error) {
	pem = []byte(strings.TrimSpace(string(pem)))
	if len(pem) == 0 {
		return "", "", ErrInvalidPrivateKey
	}
	signer, parseErr := ssh.ParsePrivateKey(pem)
	if parseErr != nil {
		// ssh.PassphraseMissingError is the sentinel for "encrypted PEM,
		// no passphrase supplied". Map it to the public error.
		if _, ok := parseErr.(*ssh.PassphraseMissingError); ok {
			return "", "", ErrPassphraseRequired
		}
		return "", "", ErrInvalidPrivateKey
	}
	pub := signer.PublicKey()
	pubLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	fp := ssh.FingerprintSHA256(pub)
	return pubLine, fp, nil
}

// isUniqueConstraintErr detects both SQLite ("UNIQUE constraint failed:")
// and PostgreSQL ("duplicate key value violates unique constraint")
// flavors. The wrapped driver does not unify these, so a substring match
// is the pragmatic choice.
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value violates unique constraint")
}
