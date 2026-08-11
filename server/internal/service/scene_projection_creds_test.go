package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// staticLookup returns a lookup closure backed by a fixed alias.field → value
// table, for exercising resolveCredEnv as a pure function.
func staticLookup(table map[string]string) func(alias, field string) (string, bool) {
	return func(alias, field string) (string, bool) {
		v, ok := table[alias+"."+field]
		return v, ok
	}
}

func TestResolveCredEnv_SingleAndMultiPlaceholder(t *testing.T) {
	lookup := staticLookup(map[string]string{
		"mailbox.imap_host": "imap.example.com",
		"mailbox.username":  "alice@example.com",
	})
	env := map[string]string{
		"IMAP_HOST": "${cred:mailbox.imap_host}",
		"COMBINED":  "user=${cred:mailbox.username};host=${cred:mailbox.imap_host}",
		"LITERAL":   "plain-value",
	}
	out, missing := resolveCredEnv(env, lookup)
	require.Empty(t, missing)
	assert.Equal(t, "imap.example.com", out["IMAP_HOST"])
	assert.Equal(t, "user=alice@example.com;host=imap.example.com", out["COMBINED"])
	assert.Equal(t, "plain-value", out["LITERAL"], "non-placeholder text preserved verbatim")
}

func TestResolveCredEnv_MissingFieldReported(t *testing.T) {
	lookup := staticLookup(map[string]string{
		"mailbox.imap_host": "imap.example.com",
		// username/password absent
	})
	env := map[string]string{
		"IMAP_HOST": "${cred:mailbox.imap_host}",
		"IMAP_USER": "${cred:mailbox.username}",
	}
	_, missing := resolveCredEnv(env, lookup)
	require.Equal(t, []string{"mailbox"}, missing, "any unresolved placeholder records its alias once")
}

func TestResolveCredEnv_SecurityEnumMapped(t *testing.T) {
	cases := map[string]string{
		"ssl":      "true",
		"tls":      "true",
		"starttls": "false",
		"none":     "false",
	}
	for in, want := range cases {
		lookup := staticLookup(map[string]string{"mailbox.security": in})
		out, missing := resolveCredEnv(map[string]string{"IMAP_SECURE": "${cred:mailbox.security}"}, lookup)
		require.Empty(t, missing)
		assert.Equalf(t, want, out["IMAP_SECURE"], "security %q → IMAP_SECURE", in)
	}
}

func TestMapCredFieldValue_NonSecurityPassThrough(t *testing.T) {
	assert.Equal(t, "imap.example.com", mapCredFieldValue("imap_host", "imap.example.com"))
	assert.Equal(t, "993", mapCredFieldValue("imap_port", "993"))
}

func TestStringifyCredValue_PortFloatRendersAsInt(t *testing.T) {
	// JSON round-trip decodes numbers as float64; an imap_port of 993 must
	// render "993", not "993.000000".
	assert.Equal(t, "993", stringifyCredValue(float64(993)))
	assert.Equal(t, "true", stringifyCredValue(true))
	assert.Equal(t, "", stringifyCredValue(nil))
	assert.Equal(t, "plain", stringifyCredValue("plain"))
}

// newImapCredSvc builds an ExternalCredentialService on the scene test DB with
// a throwaway keyring, for the injection integration tests.
func newImapCredSvc(t *testing.T, db *sql.DB) *ExternalCredentialService {
	t.Helper()
	return NewExternalCredentialService(store.New(db), db, newTestKeyring(t), integration.NewRegistry())
}

func TestResolveProjectionCredentials_InjectsAndMapsSecurity(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	owner := OwnerRef{Type: "user", ID: 1}

	cred := newImapCredSvc(t, db)
	_, err := cred.Create(ctx, ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: 1, UserID: 1,
		Provider: integration.ProviderName("imap"),
		Alias:    "mailbox",
		RawConfig: map[string]any{
			"imap_host": "imap.example.com",
			"imap_port": 993, // number → float64 after JSON round-trip
			"username":  "alice@example.com",
			"password":  "s3cret",
			"security":  "ssl",
		},
	})
	require.NoError(t, err)

	projector := NewSceneProjector(db, dataDir, nil, nil, nil, cred)
	proj := NewProjection()
	proj.MergeFrom(&SceneDefinition{
		MCP: []MCPDecl{{Name: "imap-mail", Config: map[string]any{
			"command": "npx",
			"args":    []any{"-y", "imap-mcp-server@1.0.0"},
			"env": map[string]any{
				"IMAP_HOST":   "${cred:mailbox.imap_host}",
				"IMAP_PORT":   "${cred:mailbox.imap_port}",
				"IMAP_USER":   "${cred:mailbox.username}",
				"IMAP_PASS":   "${cred:mailbox.password}",
				"IMAP_SECURE": "${cred:mailbox.security}",
			},
		}}},
		RequiredCredentials: []RequiredCredential{{Alias: "mailbox", Provider: "imap"}},
	}, LayerOrigin(1))

	resolved, dropped, missing := projector.resolveProjectionCredentials(ctx, owner, 1, proj)
	require.Empty(t, dropped)
	require.Empty(t, missing)
	env := resolved["imap-mail"]["env"].(map[string]any)
	assert.Equal(t, "imap.example.com", env["IMAP_HOST"])
	assert.Equal(t, "993", env["IMAP_PORT"], "port number renders as plain int")
	assert.Equal(t, "alice@example.com", env["IMAP_USER"])
	assert.Equal(t, "s3cret", env["IMAP_PASS"])
	assert.Equal(t, "true", env["IMAP_SECURE"], "security ssl → IMAP_SECURE true")

	// The persisted projection MCPConfigs must NOT be mutated (deep copy).
	orig := proj.MCPConfigs["imap-mail"]["env"].(map[string]any)
	assert.Equal(t, "${cred:mailbox.password}", orig["IMAP_PASS"], "source projection env untouched")
}

// TestResolveProjectionCredentials_InjectsHTTPHeaders covers the user-configured
// knowledge-base MCP path (issue B): an http/sse MCP server authenticates via an
// `Authorization: Bearer ${cred:kb-api.token}` header, and the token is resolved
// from a credstore credential at projection time — never persisted in the scene.
func TestResolveProjectionCredentials_InjectsHTTPHeaders(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	owner := OwnerRef{Type: "user", ID: 1}

	cred := newImapCredSvc(t, db)
	_, err := cred.Create(ctx, ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: 1, UserID: 1,
		Provider:  integration.ProviderName("knowledge-base"),
		Alias:     "kb-api",
		RawConfig: map[string]any{"token": "sk-kb-secret-123"},
	})
	require.NoError(t, err)

	projector := NewSceneProjector(db, dataDir, nil, nil, nil, cred)
	proj := NewProjection()
	proj.MergeFrom(&SceneDefinition{
		MCP: []MCPDecl{{Name: "kb", Config: map[string]any{
			"type": "http",
			"url":  "https://kb.example.com/mcp",
			"headers": map[string]any{
				"Authorization": "Bearer ${cred:kb-api.token}",
			},
		}}},
		RequiredCredentials: []RequiredCredential{{Alias: "kb-api", Provider: "knowledge-base"}},
	}, LayerOrigin(1))

	resolved, dropped, missing := projector.resolveProjectionCredentials(ctx, owner, 1, proj)
	require.Empty(t, dropped)
	require.Empty(t, missing)
	headers := resolved["kb"]["headers"].(map[string]any)
	assert.Equal(t, "Bearer sk-kb-secret-123", headers["Authorization"])
	// URL and other non-secret fields carried through verbatim.
	assert.Equal(t, "https://kb.example.com/mcp", resolved["kb"]["url"])

	// Missing token drops the whole server — never write a half-filled auth header.
	orig := proj.MCPConfigs["kb"]["headers"].(map[string]any)
	assert.Equal(t, "Bearer ${cred:kb-api.token}", orig["Authorization"], "source projection headers untouched")
}

// TestResolveProjectionCredentials_MissingHeaderCredDropsServer ensures an
// http KB server whose header token can't be resolved is dropped entirely.
func TestResolveProjectionCredentials_MissingHeaderCredDropsServer(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	owner := OwnerRef{Type: "user", ID: 1}

	cred := newImapCredSvc(t, db) // no credential bound
	projector := NewSceneProjector(db, dataDir, nil, nil, nil, cred)
	proj := NewProjection()
	proj.MergeFrom(&SceneDefinition{
		MCP: []MCPDecl{{Name: "kb", Config: map[string]any{
			"type":    "http",
			"url":     "https://kb.example.com/mcp",
			"headers": map[string]any{"Authorization": "Bearer ${cred:kb-api.token}"},
		}}},
		RequiredCredentials: []RequiredCredential{{Alias: "kb-api", Provider: "knowledge-base"}},
	}, LayerOrigin(1))

	resolved, dropped, missing := projector.resolveProjectionCredentials(ctx, owner, 1, proj)
	assert.True(t, dropped["kb"], "http server with unresolved header token is dropped")
	assert.NotContains(t, resolved, "kb")
	assert.True(t, missing["kb-api"])
}

func TestResolveProjectionCredentials_MissingCredDropsServer(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	owner := OwnerRef{Type: "user", ID: 1}

	// extCred wired but NO credential bound for alias mailbox.
	cred := newImapCredSvc(t, db)
	projector := NewSceneProjector(db, dataDir, nil, nil, nil, cred)
	proj := NewProjection()
	proj.MergeFrom(&SceneDefinition{
		MCP: []MCPDecl{{Name: "imap-mail", Config: map[string]any{
			"command": "npx",
			"env":     map[string]any{"IMAP_PASS": "${cred:mailbox.password}"},
		}}},
		RequiredCredentials: []RequiredCredential{{Alias: "mailbox", Provider: "imap"}},
	}, LayerOrigin(1))

	resolved, dropped, missing := projector.resolveProjectionCredentials(ctx, owner, 1, proj)
	assert.True(t, dropped["imap-mail"], "server with unresolved placeholder is dropped entirely")
	assert.NotContains(t, resolved, "imap-mail", "dropped server never written")
	assert.True(t, missing["mailbox"], "alias reported missing")
}

func TestResolveProjectionCredentials_CrossUserInvisible(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	owner := OwnerRef{Type: "user", ID: 1}

	cred := newImapCredSvc(t, db)
	// Credential bound for user_id=1 ...
	_, err := cred.Create(ctx, ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: 1, UserID: 1,
		Provider: integration.ProviderName("imap"), Alias: "mailbox",
		RawConfig: map[string]any{"password": "s3cret"},
	})
	require.NoError(t, err)

	projector := NewSceneProjector(db, dataDir, nil, nil, nil, cred)
	proj := NewProjection()
	proj.MergeFrom(&SceneDefinition{
		MCP:                 []MCPDecl{{Name: "imap-mail", Config: map[string]any{"command": "npx", "env": map[string]any{"IMAP_PASS": "${cred:mailbox.password}"}}}},
		RequiredCredentials: []RequiredCredential{{Alias: "mailbox", Provider: "imap"}},
	}, LayerOrigin(1))

	// ... but resolving as user_id=2 must NOT see it.
	_, dropped, missing := projector.resolveProjectionCredentials(ctx, owner, 2, proj)
	assert.True(t, dropped["imap-mail"], "another user's credential is invisible")
	assert.True(t, missing["mailbox"])
}
