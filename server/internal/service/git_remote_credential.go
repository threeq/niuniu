// Package service — GitRemoteCredentialService manages per-(user, host)
// git remote credentials used to authenticate remote operations (clone,
// push, pull, fetch): 'ssh' private keys (rendered into an ssh_config and
// injected via GIT_SSH_COMMAND) and 'token' secrets (injected into HTTPS
// remote URLs at operation time).
//
// Credentials are consumed in two places, both wired in this package:
//   - RepositoryService.Create → server-side staging clone (ResolveForRemote)
//   - PTY spawn → GIT_SSH_COMMAND env so in-workspace `git push/pull` (B)
//
// Storage shape: the secret is AES-GCM-encrypted (reusing the integration
// keyring) and stored in `git_remote_credentials.encrypted`. The matching
// public key and SHA256 fingerprint are derived at create time so the UI
// can render them without touching the encrypted blob.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
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

	// ErrInvalidCredentialKind is returned for a credential kind other than
	// 'ssh' (private key) or 'token' (HTTPS remote secret).
	ErrInvalidCredentialKind = errors.New("invalid credential kind")

	// ErrHostConflict maps the UNIQUE(user_id, host) constraint violation
	// to a stable error code for the API layer.
	ErrHostConflict = errors.New("a credential for this host already exists")
)

// Credential kinds. 'ssh' wraps a private key used via GIT_SSH_COMMAND /
// ssh_config; 'token' is a secret (access token / password) injected into
// HTTPS remote URLs at operation time — never persisted into the repo URL.
const (
	CredentialKindSSH   = "ssh"
	CredentialKindToken = "token"
)

// NormalizeGitHost canonicalizes a host for storage and lookup: strips any
// scheme prefix (https://, ssh://, …), userinfo, and trailing slash, and
// lowercases. Lets a credential stored as host `cnb.cool` match a remote
// spelled `https://cnb.cool` (the #1 misconfiguration — the toggle page
// accepts either spelling).
func NormalizeGitHost(h string) string {
	h = strings.TrimSpace(h)
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.LastIndex(h, "@"); i >= 0 {
		h = h[i+1:]
	}
	if i := strings.Index(h, "/"); i >= 0 {
		h = h[:i]
	}
	return strings.ToLower(strings.TrimSuffix(h, "/"))
}

// GitRemoteHost extracts the normalized host from any git remote URL shape:
// https://host/path, ssh://git@host:port/path, git@host:path. Returns ""
// when nothing host-like can be parsed.
func GitRemoteHost(remoteURL string) string {
	u := strings.TrimSpace(remoteURL)
	if u == "" {
		return ""
	}
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			rest = rest[:j]
		}
		return NormalizeGitHost(rest)
	}
	// scp-like: [user@]host:path
	if j := strings.Index(u, ":"); j > 0 {
		return NormalizeGitHost(u[:j])
	}
	return NormalizeGitHost(u)
}

// IsSSHRemote reports whether the remote URL uses the SSH transport
// (git@host:path scp-like or ssh:// scheme) as opposed to https://.
func IsSSHRemote(remoteURL string) bool {
	u := strings.TrimSpace(remoteURL)
	if strings.HasPrefix(u, "ssh://") {
		return true
	}
	if strings.Contains(u, "://") {
		return false
	}
	// scp-like: colon before any slash (git@host:path)
	colon := strings.Index(u, ":")
	slash := strings.Index(u, "/")
	return colon > 0 && (slash < 0 || colon < slash)
}

// InjectURLSecret rewrites an HTTPS remote URL with the credential's
// username + secret (`https://user:secret@host/path`) for one
// authentication round-trip. Returns the input unchanged when the URL is
// not a plain https URL or already carries userinfo. The caller must NOT
// persist the result — restore the clean URL after the operation.
func InjectURLSecret(remoteURL, username, secret string) string {
	u := strings.TrimSpace(remoteURL)
	if !strings.HasPrefix(u, "https://") || strings.Contains(u, "@") {
		return remoteURL
	}
	user := username
	if user == "" {
		user = "git"
	}
	rest := u[len("https://"):]
	return "https://" + url.QueryEscape(user) + ":" + url.QueryEscape(secret) + "@" + rest
}

// RemoteCredentialSecret is a decrypted credential ready for use in a git
// operation. Secret holds the SSH PEM (kind=ssh) or the raw token
// (kind=token) — callers must never log or persist it.
type RemoteCredentialSecret struct {
	Kind     string
	Username string
	Secret   string
	Host     string
}

// GitRemoteCredential is the redacted in-process representation of a
// stored credential. Private key never appears in this struct.
type GitRemoteCredential struct {
	ID          int64        `json:"id"`
	UserID      int64        `json:"user_id"`
	Host        string       `json:"host"`
	Username    string       `json:"username"`
	Kind        string       `json:"kind"`
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
	// materializeDir is the root directory SSH keys are materialized into
	// (per-user subdirectory) for GIT_SSH_COMMAND injection. Set via
	// SetMaterializeDir; empty disables materialization.
	materializeDir string
}

func NewGitRemoteCredentialService(rawDB *sql.DB, kr *crypto.Keyring) *GitRemoteCredentialService {
	return &GitRemoteCredentialService{db: store.Wrap(rawDB), keyring: kr}
}

// SetMaterializeDir points SSH-key materialization at a root directory
// (per-user subdirectories are created beneath it). Called once at boot.
func (s *GitRemoteCredentialService) SetMaterializeDir(dir string) { s.materializeDir = dir }

// CreateInput is what handlers pass to Create.
type CreateGitRemoteCredentialInput struct {
	UserID     int64
	Host       string
	Username   string // optional; defaults to "git"
	Kind       string // optional; defaults to "ssh". "token" stores a secret for HTTPS remotes.
	PrivateKey string // SSH kind: PEM private key (no passphrase). Token kind: the raw secret.
}

// Create validates and encrypts the credential, then inserts a new row.
// The host is stored normalized (NormalizeGitHost) so a remote spelled
// `https://cnb.cool/...` matches a credential saved as `cnb.cool` or
// `https://cnb.cool` alike.
// Returns:
//   - ErrInvalidPrivateKey when the PEM is malformed (ssh kind)
//   - ErrPassphraseRequired when the PEM is encrypted (ssh kind)
//   - ErrInvalidKind when kind is neither 'ssh' nor 'token'
//   - ErrInvalidHost when the host fails validation
//   - ErrHostConflict when (user_id, host) already exists
func (s *GitRemoteCredentialService) Create(ctx context.Context, in CreateGitRemoteCredentialInput) (*GitRemoteCredential, error) {
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = CredentialKindSSH
	}
	if kind != CredentialKindSSH && kind != CredentialKindToken {
		return nil, ErrInvalidCredentialKind
	}
	host := NormalizeGitHost(in.Host)
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

	var pubKey, fingerprint string
	secret := strings.TrimSpace(in.PrivateKey)
	if secret == "" {
		return nil, ErrInvalidPrivateKey
	}
	if kind == CredentialKindSSH {
		var err error
		pubKey, fingerprint, err = parsePrivateKey([]byte(secret))
		if err != nil {
			return nil, err
		}
	}

	ciphertext, err := s.keyring.Encrypt([]byte(secret + "\n"))
	if err != nil {
		return nil, fmt.Errorf("encrypt credential: %w", err)
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
		  (user_id, host, username, kind, encrypted, public_key, fingerprint)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		in.UserID, host, username, kind, string(ciphertext), pubKey, fingerprint,
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
		Kind:        kind,
		PublicKey:   pubKey,
		Fingerprint: fingerprint,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}, nil
}

// List returns all of the caller's credentials. The encrypted secret is
// NEVER returned — only the redacted metadata.
func (s *GitRemoteCredentialService) List(ctx context.Context, userID int64) ([]GitRemoteCredential, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, host, username, kind, public_key, fingerprint,
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
		if err := rows.Scan(&c.ID, &c.UserID, &c.Host, &c.Username, &c.Kind, &c.PublicKey,
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

// ResolveForHost decrypts the credential for a (user, host) pair. The host
// is matched normalized; as a compatibility fallback the raw spelling is
// retried (rows written before NormalizeGitHost existed may carry a scheme
// prefix). Returns sql.ErrNoRows when no credential exists.
//
// Bumps last_used_at to NOW() as a side effect so the UI / admin tools
// can show "when was this key last used".
func (s *GitRemoteCredentialService) ResolveForHost(ctx context.Context, userID int64, host string) (*RemoteCredentialSecret, error) {
	// GitRemoteHost (not NormalizeGitHost) so callers may pass a whole URL
	// or scp-like remote — every shape resolves to the bare host.
	normalized := GitRemoteHost(host)
	if normalized == "" {
		return nil, sql.ErrNoRows
	}
	var (
		encrypted string
		uname     string
		kind      string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT encrypted, username, kind FROM git_remote_credentials
		WHERE user_id = ? AND host = ?`, userID, normalized).Scan(&encrypted, &uname, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		// Legacy rows written before host normalization may carry the scheme
		// prefix — retry with the raw spelling before giving up.
		if raw := strings.TrimSpace(host); raw != normalized {
			err = s.db.QueryRowContext(ctx, `
				SELECT encrypted, username, kind FROM git_remote_credentials
				WHERE user_id = ? AND host = ?`, userID, raw).Scan(&encrypted, &uname, &kind)
		}
	}
	if err != nil {
		return nil, err
	}
	pt, err := s.keyring.Decrypt([]byte(encrypted))
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	// Best-effort: don't fail the call if the timestamp update fails.
	_, _ = s.db.ExecContext(ctx,
		`UPDATE git_remote_credentials SET last_used_at = CURRENT_TIMESTAMP
		 WHERE user_id = ? AND host = ?`, userID, normalized)
	return &RemoteCredentialSecret{
		Kind:     kind,
		Username: uname,
		Secret:   strings.TrimRight(string(pt), "\n"),
		Host:     normalized,
	}, nil
}

// ResolveForRemote looks up the caller's credential for a remote URL's
// host (any URL shape — https://, ssh://, scp-like). Returns (nil, nil)
// when no credential is stored: callers fall back to a bare git operation,
// which keeps the pre-credential behavior for public remotes.
func (s *GitRemoteCredentialService) ResolveForRemote(ctx context.Context, userID int64, remoteURL string) (*RemoteCredentialSecret, error) {
	host := GitRemoteHost(remoteURL)
	if host == "" || userID <= 0 {
		return nil, nil
	}
	cred, err := s.ResolveForHost(ctx, userID, host)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return cred, nil
}

// MaterializeUserSSHKeys renders the user's SSH credentials under the
// configured materialize dir as an ssh_config plus one 0600 key file per
// host, returning the config path (or "" when the user has no SSH
// credentials or no materialize dir is set — callers then skip
// GIT_SSH_COMMAND entirely). Called on every PTY spawn: idempotent and
// self-healing (a deleted/edited file is rewritten on the next spawn).
//
// The wildcard host ('*') is emitted last — ssh_config is first-match-wins,
// so specific hosts keep priority over the catch-all.
func (s *GitRemoteCredentialService) MaterializeUserSSHKeys(ctx context.Context, userID int64) (string, error) {
	if s.materializeDir == "" {
		return "", nil
	}
	dir := filepath.Join(s.materializeDir, fmt.Sprint(userID))
	return s.materializeUserSSHKeys(ctx, userID, dir)
}

// materializeUserSSHKeys is the directory-taking core (tests pass t.TempDir).
func (s *GitRemoteCredentialService) materializeUserSSHKeys(ctx context.Context, userID int64, dir string) (string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT host, username, encrypted FROM git_remote_credentials
		WHERE user_id = ? AND kind = 'ssh'
		ORDER BY CASE WHEN host = '*' THEN 1 ELSE 0 END, host ASC`, userID)
	if err != nil {
		return "", fmt.Errorf("list ssh credentials: %w", err)
	}
	type entry struct{ host, username, encrypted string }
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.host, &e.username, &e.encrypted); err != nil {
			rows.Close()
			return "", fmt.Errorf("scan ssh credential: %w", err)
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate ssh credentials: %w", err)
	}
	if len(entries) == 0 {
		return "", nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create ssh dir: %w", err)
	}
	var cfg strings.Builder
	cfg.WriteString("# Generated by niuniu — do not edit; rewritten on every session start.\n")
	for _, e := range entries {
		pt, err := s.keyring.Decrypt([]byte(e.encrypted))
		if err != nil {
			slog.Warn("git ssh materialize: decrypt failed; skipping host",
				"user_id", userID, "host", e.host, "err", err)
			continue
		}
		keyPath := filepath.Join(dir, sanitizeHostFileName(e.host)+".key")
		if err := os.WriteFile(keyPath, []byte(strings.TrimRight(string(pt), "\n")+"\n"), 0o600); err != nil {
			return "", fmt.Errorf("write key file: %w", err)
		}
		cfg.WriteString(fmt.Sprintf("Host %s\n\tHostname %s\n\tUser %s\n\tIdentityFile %q\n\tIdentitiesOnly yes\n\tStrictHostKeyChecking accept-new\n\n",
			e.host, e.host, e.username, keyPath))
	}
	if cfg.Len() == len("# Generated by niuniu — do not edit; rewritten on every session start.\n") {
		// Every row failed to decrypt — nothing usable was materialized.
		return "", nil
	}
	cfgPath := filepath.Join(dir, "config")
	if err := os.WriteFile(cfgPath, []byte(cfg.String()), 0o600); err != nil {
		return "", fmt.Errorf("write ssh config: %w", err)
	}
	return cfgPath, nil
}

// sanitizeHostFileName maps a host (possibly '*') to a safe file name base.
func sanitizeHostFileName(host string) string {
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
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
