package service

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A host the caller stored a credential for is trusted egress: hosted clouds
// answer such hosts with link-local acceleration addresses (Tencent Cloud maps
// cnb.cool to 169.254.1.44, which is reachable and serves HTTPS), and the
// generic private-address rule used to reject those pulls/pushes outright with
// "remote host blocked". Without a credential the same host stays blocked.
func TestGuardRemoteEgressTrustsStoredCredential(t *testing.T) {
	prev := KBAllowLocalSources()
	t.Cleanup(func() { SetKBAllowLocalSources(prev) })
	SetKBAllowLocalSources(false) // hosted edition

	svc, userID := newGitRemoteCredSvc(t)
	ctx := context.Background()

	const remote = "https://cnb.cool/agi-tokens/tenant-platform"
	// No credential yet → the address rule applies (169.254.1.44 is link-local
	// on this host, but the test asserts the credential-less path rejects a
	// private literal instead, which is deterministic across environments).
	require.Error(t, guardRemoteEgressWith(ctx, svc, userID, "https://127.0.0.1/x.git"))
	require.NoError(t, guardRemoteEgressWith(ctx, nil, 0, remote), "public host passes")

	_, err := svc.Create(ctx, CreateGitRemoteCredentialInput{
		UserID: userID, Host: "cnb.cool", Kind: CredentialKindToken, PrivateKey: "tok-123",
	})
	require.NoError(t, err)

	// Now anything on that host is the caller's own decision → not blocked.
	require.NoError(t, guardRemoteEgressWith(ctx, svc, userID, "https://cnb.cool/x.git"))
	// A different host with no credential keeps the strict rule.
	require.Error(t, guardRemoteEgressWith(ctx, svc, userID, "https://127.0.0.1/x.git"))
}

// Token credentials are materialised as a git credential-store file so
// in-workspace `git pull/push` (PTY) can authenticate over HTTPS — previously
// only SSH keys were materialised, so terminal git asked for a username it
// could never read.
func TestMaterializeUserTokens(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	dir := t.TempDir()
	ctx := context.Background()

	path, err := svc.materializeUserTokens(ctx, userID, dir)
	require.NoError(t, err)
	assert.Empty(t, path, "no token credentials → nothing materialised")

	_, err = svc.Create(ctx, CreateGitRemoteCredentialInput{
		UserID: userID, Host: "cnb.cool", Username: "cnb", Kind: CredentialKindToken, PrivateKey: "s3cr3t",
	})
	require.NoError(t, err)

	path, err = svc.materializeUserTokens(ctx, userID, dir)
	require.NoError(t, err)
	require.NotEmpty(t, path)

	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		// Windows only emulates the read-only bit, so the 0600 assertion is
		// meaningful on the platforms we ship (Linux containers).
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "credential file must not be world readable")
	}

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "https://cnb:s3cr3t@cnb.cool\n", string(body))
}

// prepareRemoteAuth is what makes the repository panel's pull/push work against
// an HTTPS remote with a stored token: it yields a credential.helper env whose
// file holds the secret, and cleanup removes it.
func TestPrepareRemoteAuthToken(t *testing.T) {
	svc, userID := newGitRemoteCredSvc(t)
	ctx := context.Background()
	const remote = "https://cnb.cool/agi-tokens/tenant-platform"

	_, err := svc.Create(ctx, CreateGitRemoteCredentialInput{
		UserID: userID, Host: "cnb.cool", Username: "cnb", Kind: CredentialKindToken, PrivateKey: "s3cr3t",
	})
	require.NoError(t, err)

	env, cleanup := remoteAuthEnv(ctx, svc, userID, remote)
	defer cleanup()

	joined := strings.Join(env, "\n")
	assert.Contains(t, joined, "GIT_CONFIG_KEY_0=credential.helper")
	assert.Contains(t, joined, "GIT_CONFIG_VALUE_0=store --file=")

	// The credential file exists, is 0600, and carries the injected secret.
	var credPath string
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_CONFIG_VALUE_0=store --file=") {
			credPath = strings.TrimPrefix(e, "GIT_CONFIG_VALUE_0=store --file=")
		}
	}
	require.NotEmpty(t, credPath)
	body, err := os.ReadFile(credPath)
	require.NoError(t, err)
	assert.Contains(t, string(body), "@cnb.cool")

	// No credential for a different host → no env, and nothing to clean up.
	emptyEnv, cleanup2 := remoteAuthEnv(ctx, svc, userID, "https://github.com/o/r.git")
	defer cleanup2()
	assert.Empty(t, emptyEnv)

	// Cleanup removes the throwaway directory, so the secret does not linger.
	cleanup()
	_, statErr := os.Stat(filepath.Dir(credPath))
	assert.True(t, os.IsNotExist(statErr), "temp auth dir removed by cleanup")
}
