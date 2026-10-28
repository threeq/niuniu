package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoteHost(t *testing.T) {
	cases := map[string]string{
		"https://github.com/org/repo.git":            "github.com",
		"https://user:token@gitlab.local:8443/x.git": "gitlab.local",
		"ssh://git@example.com:22/org/repo.git":      "example.com",
		"git@github.com:org/repo.git":                "github.com",
		"file:///srv/repos/x.git":                    "",
		"/srv/repos/x.git":                           "",
		"github.com:org/repo.git":                    "github.com",
		"C:/repos/x.git":                             "",
	}
	for in, want := range cases {
		require.Equal(t, want, remoteHost(in), "input %q", in)
	}
}

func TestGuardRemoteEgress(t *testing.T) {
	prev := KBAllowLocalSources()
	t.Cleanup(func() { SetKBAllowLocalSources(prev) })
	ctx := context.Background()

	// No remote recorded: nothing to check.
	require.NoError(t, guardRemoteEgress(ctx, ""))

	SetKBAllowLocalSources(true) // personal edition: local-first remotes allowed
	require.NoError(t, guardRemoteEgress(ctx, "https://127.0.0.1/x.git"))
	require.NoError(t, guardRemoteEgress(ctx, "file:///srv/x.git"))

	SetKBAllowLocalSources(false) // hosted edition: block internal egress
	require.Error(t, guardRemoteEgress(ctx, "file:///srv/x.git"))
	require.Error(t, guardRemoteEgress(ctx, "https://127.0.0.1/x.git"))
	require.Error(t, guardRemoteEgress(ctx, "http://10.0.0.5/x.git"))
	require.Error(t, guardRemoteEgress(ctx, "https://169.254.169.254/latest/meta-data"))
	require.Error(t, guardRemoteEgress(ctx, "git@192.168.1.10:org/repo.git"))
}
