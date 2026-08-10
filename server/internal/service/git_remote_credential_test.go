package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/pem"
	"errors"
	"path/filepath"
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

func TestGitRemoteCredential_Decrypt_BumpsLastUsedAt(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	pemBytes, _ := genTestEd25519PEM(t)
	_, err := svc.Create(context.Background(), CreateGitRemoteCredentialInput{
		UserID: userID, Host: "github.com", PrivateKey: string(pemBytes),
	})
	require.NoError(t, err)

	pk, uname, err := svc.DecryptForUserHost(context.Background(), userID, "github.com")
	require.NoError(t, err)
	assert.Equal(t, "git", uname)
	assert.Contains(t, string(pk), "BEGIN OPENSSH PRIVATE KEY")

	creds, _ := svc.List(context.Background(), userID)
	require.Len(t, creds, 1)
	assert.True(t, creds[0].LastUsedAt.Valid, "last_used_at should be set after decrypt")
}

func TestGitRemoteCredential_Decrypt_NotFoundReturnsErrNoRows(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	_, _, err := svc.DecryptForUserHost(context.Background(), userID, "github.com")
	assert.ErrorIs(t, err, sql.ErrNoRows)
}
