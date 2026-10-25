package service

import (
	"runtime"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// genTestEd25519PEM generates a fresh Ed25519 private key in OpenSSH PEM
// format. The matching authorized_keys line is returned alongside.
func genTestEd25519PEM(t *testing.T) (pemBytes []byte, authorizedLine string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	block, err := gossh.MarshalPrivateKey(priv, "niuniu-test")
	require.NoError(t, err)
	pemBytes = pem.EncodeToMemory(block)

	sshPub, err := gossh.NewPublicKey(pub)
	require.NoError(t, err)
	authorizedLine = string(gossh.MarshalAuthorizedKey(sshPub))
	// MarshalAuthorizedKey appends a trailing newline; the service strips
	// it before storing, so do the same here for equality assertions.
	for len(authorizedLine) > 0 && (authorizedLine[len(authorizedLine)-1] == '\n' || authorizedLine[len(authorizedLine)-1] == '\r') {
		authorizedLine = authorizedLine[:len(authorizedLine)-1]
	}
	return pemBytes, authorizedLine
}

func newTestKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	kr, err := crypto.LoadOrCreate(filepath.Join(t.TempDir(), "secret"))
	require.NoError(t, err)
	return kr
}

func newGitRemoteCredSvc(t *testing.T) (*GitRemoteCredentialService, int64) {
	t.Helper()
	db := openOrgTestDB(t)
	userID := newOrgTestUser(t, db, "alice", "member")
	svc := NewGitRemoteCredentialService(db, newTestKeyring(t))
	return svc, userID
}

func TestGitRemoteCredential_Create_DerivesPublicKey(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	pemBytes, authLine := genTestEd25519PEM(t)

	cred, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID:     userID,
		Host:       "github.com",
		PrivateKey: string(pemBytes),
	})
	require.NoError(t, err)
	assert.Equal(t, "github.com", cred.Host)
	assert.Equal(t, "git", cred.Username, "username defaults to 'git'")
	assert.Equal(t, authLine, cred.PublicKey)
	assert.Regexp(t, `^SHA256:[A-Za-z0-9+/]{43}$`, cred.Fingerprint)
}

func TestGitRemoteCredential_Create_HonorsCustomUsername(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	pemBytes, _ := genTestEd25519PEM(t)
	cred, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "internal-host", Username: "deploy",
		PrivateKey: string(pemBytes),
	})
	require.NoError(t, err)
	assert.Equal(t, "deploy", cred.Username)
}

func TestGitRemoteCredential_Create_RejectsGarbagePEM(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	_, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID:     userID,
		Host:       "github.com",
		PrivateKey: "not-a-real-private-key",
	})
	assert.True(t, errors.Is(err, ErrInvalidPrivateKey))
}

func TestGitRemoteCredential_Create_RejectsEmptyHost(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	pemBytes, _ := genTestEd25519PEM(t)
	_, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "  ", PrivateKey: string(pemBytes),
	})
	assert.True(t, errors.Is(err, ErrInvalidHost))
}

func TestGitRemoteCredential_Create_RejectsHostWithInjection(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	pemBytes, _ := genTestEd25519PEM(t)
	// Only embedded control / shell-injecting chars — trailing whitespace
	// is intentionally allowed (TrimSpace normalizes it).
	cases := []string{
		"git hub.com",         // space mid-host
		"git\thub.com",        // tab mid-host
		"git\nhub.com",        // newline mid-host (would inject extra ssh_config lines)
		`host"injection`,      // quote — would break Host directive parsing
		`host\backslash`,      // backslash — same
		"#commenthost",        // would be treated as a comment in ssh_config
	}
	for _, h := range cases {
		_, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
			UserID: userID, Host: h, PrivateKey: string(pemBytes),
		})
		assert.True(t, errors.Is(err, ErrInvalidHost),
			"host=%q expected ErrInvalidHost, got %v", h, err)
	}
}

func TestGitRemoteCredential_Create_Conflict(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	pemBytes, _ := genTestEd25519PEM(t)
	_, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "github.com", PrivateKey: string(pemBytes),
	})
	require.NoError(t, err)
	pemBytes2, _ := genTestEd25519PEM(t)
	_, err = svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "github.com", PrivateKey: string(pemBytes2),
	})
	assert.True(t, errors.Is(err, ErrHostConflict))
}

func TestGitRemoteCredential_List_SortedByHost(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	pem1, _ := genTestEd25519PEM(t)
	pem2, _ := genTestEd25519PEM(t)
	_, _ = svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "github.com", PrivateKey: string(pem1),
	})
	_, _ = svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "gitlab.com", PrivateKey: string(pem2),
	})

	creds, err := svc.List(context.Background(), userID)
	require.NoError(t, err)
	require.Len(t, creds, 2)
	assert.Equal(t, "github.com", creds[0].Host)
	assert.Equal(t, "gitlab.com", creds[1].Host)
}

func TestGitRemoteCredential_List_IsUserScoped(t *testing.T) {
	svc, alice := newGitRemoteCredSvc(t)
	pemBytes, _ := genTestEd25519PEM(t)
	_, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: alice, Host: "github.com", PrivateKey: string(pemBytes),
	})
	require.NoError(t, err)
	// Different user ID → empty list, not alice's row.
	creds, err := svc.List(context.Background(), alice+100)
	require.NoError(t, err)
	assert.Len(t, creds, 0)
}

func TestGitRemoteCredential_Delete_Owned(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	pemBytes, _ := genTestEd25519PEM(t)
	c, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "github.com", PrivateKey: string(pemBytes),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Delete(context.Background(), userID, c.ID))

	creds, err := svc.List(context.Background(), userID)
	require.NoError(t, err)
	assert.Len(t, creds, 0)
}

func TestGitRemoteCredential_Delete_OtherUserReturnsNoRows(t *testing.T) {
	svc, alice := newGitRemoteCredSvc(t)
	pemBytes, _ := genTestEd25519PEM(t)
	c, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: alice, Host: "github.com", PrivateKey: string(pemBytes),
	})
	require.NoError(t, err)
	err = svc.Delete(context.Background(), alice+100, c.ID)
	assert.ErrorIs(t, err, sql.ErrNoRows, "deleting another user's credential should look like 404")
}

func TestGitRemoteCredential_Resolve_BumpsLastUsedAt(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	pemBytes, _ := genTestEd25519PEM(t)
	_, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "github.com", PrivateKey: string(pemBytes),
	})
	require.NoError(t, err)

	cred, err := svc.ResolveForHost(context.Background(), userID, "github.com")
	require.NoError(t, err)
	assert.Equal(t, "git", cred.Username)
	assert.Equal(t, CredentialKindSSH, cred.Kind)
	assert.Contains(t, cred.Secret, "BEGIN OPENSSH PRIVATE KEY")

	creds, _ := svc.List(context.Background(), userID)
	require.Len(t, creds, 1)
	assert.True(t, creds[0].LastUsedAt.Valid, "last_used_at should be set after resolve")
}

func TestGitRemoteCredential_Resolve_NotFoundReturnsErrNoRows(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	_, err := svc.ResolveForHost(context.Background(), userID, "github.com")
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

// ─── host normalization + URL parsing (C) ───────────────────────────────────

func TestNormalizeGitHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"github.com", "github.com"},
		{"https://cnb.cool", "cnb.cool"},
		{"HTTPS://CNB.Cool/", "cnb.cool"},
		{"https://user:tok@cnb.cool/", "cnb.cool"},
		{"ssh://git@cnb.cool:22/", "cnb.cool:22"},
		{"git@cnb.cool", "cnb.cool"},
		{"  cnb.cool  ", "cnb.cool"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, NormalizeGitHost(c.in), "input %q", c.in)
	}
}

func TestGitRemoteHostParsing(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://cnb.cool/agi-tokens/tenant-platform", "cnb.cool"},
		{"https://cnb.cool", "cnb.cool"},
		{"https://u:p@cnb.cool/a/b.git", "cnb.cool"},
		{"git@cnb.cool:agi-tokens/tenant-platform.git", "cnb.cool"},
		{"ssh://git@cnb.cool:22/agi/x.git", "cnb.cool:22"},
		{"", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, GitRemoteHost(c.in), "input %q", c.in)
	}
}

func TestIsSSHRemote(t *testing.T) {
	assert.True(t, IsSSHRemote("git@cnb.cool:agi/x.git"))
	assert.True(t, IsSSHRemote("ssh://git@cnb.cool/x"))
	assert.False(t, IsSSHRemote("https://cnb.cool/agi/x"))
}

// A credential saved under a scheme-prefixed or normalized host must match
// lookups spelled either way — the exact trap the customer hit (host saved
// as `https://cnb.cool`, remote spelled `https://cnb.cool/...`).
func TestResolveForHost_MatchesAcrossHostSpellings(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	pemBytes, _ := genTestEd25519PEM(t)
	// Legacy-style row saved WITH the scheme prefix.
	_, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "https://cnb.cool", PrivateKey: string(pemBytes),
	})
	require.NoError(t, err)
	// Stored normalized.
	creds, err := svc.List(context.Background(), userID)
	require.NoError(t, err)
	require.Len(t, creds, 1)
	assert.Equal(t, "cnb.cool", creds[0].Host)

	// Lookups via every spelling hit the same row.
	for _, spelling := range []string{"cnb.cool", "https://cnb.cool", "https://cnb.cool/"} {
		cred, err := svc.ResolveForHost(context.Background(), userID, spelling)
		require.NoError(t, err, "spelling %q", spelling)
		assert.Equal(t, "cnb.cool", cred.Host)
	}
}

// ─── token kind (A) ──────────────────────────────────────────────────────────

func TestCreate_TokenKind_RoundTrips(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	cred, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "https://cnb.cool", Username: "cnb",
		Kind: CredentialKindToken, PrivateKey: "cnb_secret_token",
	})
	require.NoError(t, err)
	assert.Equal(t, CredentialKindToken, cred.Kind)
	assert.Empty(t, cred.PublicKey, "token rows carry no public key")
	assert.Empty(t, cred.Fingerprint)

	got, err := svc.ResolveForHost(context.Background(), userID, "git@cnb.cool:x/y")
	require.NoError(t, err)
	assert.Equal(t, CredentialKindToken, got.Kind)
	assert.Equal(t, "cnb", got.Username)
	assert.Equal(t, "cnb_secret_token", got.Secret)
}

func TestCreate_TokenKind_RejectsEmptySecret(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	_, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "cnb.cool", Kind: CredentialKindToken, PrivateKey: "  ",
	})
	assert.ErrorIs(t, err, ErrInvalidPrivateKey)
}

func TestCreate_RejectsUnknownKind(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	_, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "cnb.cool", Kind: "gpg", PrivateKey: "x",
	})
	assert.ErrorIs(t, err, ErrInvalidCredentialKind)
}

// ResolveForRemote matches the URL's host; a missing credential is a soft
// nil (bare git op), not an error.
func TestResolveForRemote(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	pemBytes, _ := genTestEd25519PEM(t)
	_, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "cnb.cool", PrivateKey: string(pemBytes),
	})
	require.NoError(t, err)

	cred, err := svc.ResolveForRemote(context.Background(), userID, "git@cnb.cool:agi/x.git")
	require.NoError(t, err)
	require.NotNil(t, cred)
	assert.Equal(t, CredentialKindSSH, cred.Kind)

	none, err := svc.ResolveForRemote(context.Background(), userID, "https://github.com/a/b")
	require.NoError(t, err)
	assert.Nil(t, none, "no credential for github.com → soft nil")

	otherUser, err := svc.ResolveForRemote(context.Background(), userID+1, "git@cnb.cool:agi/x.git")
	require.NoError(t, err)
	assert.Nil(t, otherUser, "another user's lookup must not see alice's row")
}

// ─── materialization (B) ─────────────────────────────────────────────────────

func TestMaterializeUserSSHKeys(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	pem1, _ := genTestEd25519PEM(t)
	pem2, _ := genTestEd25519PEM(t)
	_, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "https://cnb.cool", PrivateKey: string(pem1),
	})
	require.NoError(t, err)
	_, err = svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "*", PrivateKey: string(pem2),
	})
	require.NoError(t, err)
	// A token credential must NOT be materialized as a key file.
	_, err = svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "gitlab.com", Kind: CredentialKindToken, PrivateKey: "tok",
	})
	require.NoError(t, err)

	dir := t.TempDir()
	svc.SetMaterializeDir(dir)
	cfgPath, err := svc.MaterializeUserSSHKeys(context.Background(), userID)
	require.NoError(t, err)
	require.NotEmpty(t, cfgPath)
	// The config lands in the per-user subdirectory of the materialize root.
	assert.Contains(t, cfgPath, filepath.Join(fmt.Sprint(userID), "config"))

	cfg, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	text := string(cfg)
	assert.Contains(t, text, "Host cnb.cool")
	assert.Contains(t, text, "IdentityFile")
	assert.Contains(t, text, "IdentitiesOnly yes")
	// The wildcard host block must come after the specific one
	// (ssh_config is first-match-wins).
	assert.Less(t, strings.Index(text, "Host cnb.cool"), strings.Index(text, "Host *"))

	// Both key files exist and are 0600.
	keys, _ := filepath.Glob(filepath.Join(dir, fmt.Sprint(userID), "*.key"))
	require.Len(t, keys, 2)
	for _, k := range keys {
		st, statErr := os.Stat(k)
		require.NoError(t, statErr)
		if runtime.GOOS != "windows" {
			// Windows filesystems don't present POSIX modes; on Unix the
			// key file must be owner-only.
			assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
		}
	}

	// No user credentials → no config, no error.
	otherSvc, otherUser := newGitRemoteCredSvc(t)
	otherSvc.SetMaterializeDir(t.TempDir())
	cfg2, err := otherSvc.MaterializeUserSSHKeys(context.Background(), otherUser+1)
	require.NoError(t, err)
	assert.Empty(t, cfg2)
}

// The token is rewritten into the clone URL for a single round-trip; the
// caller restores the clean origin afterwards. Non-https URLs and URLs that
// already carry userinfo must come back untouched.
func TestInjectURLSecret(t *testing.T) {
	assert.Equal(t,
		"https://cnb:tok%21@cnb.cool/agi/x.git",
		InjectURLSecret("https://cnb.cool/agi/x.git", "cnb", "tok!"))
	// Default username when none stored.
	assert.Equal(t,
		"https://git:s@cnb.cool/x",
		InjectURLSecret("https://cnb.cool/x", "", "s"))
	// Already has userinfo → untouched.
	assert.Equal(t,
		"https://u:p@cnb.cool/x",
		InjectURLSecret("https://u:p@cnb.cool/x", "cnb", "tok"))
	// Non-https transport → untouched.
	assert.Equal(t, "git@cnb.cool:x", InjectURLSecret("git@cnb.cool:x", "cnb", "tok"))
}
