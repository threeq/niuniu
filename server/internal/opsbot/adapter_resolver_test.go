package opsbot

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/opsbot/browser"
	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type resolverWorker struct {
	calls int
}

func (w *resolverWorker) Run(_ context.Context, task browser.Task) (browser.Result, error) {
	w.calls++
	return browser.Result{Values: map[string]string{"url": "https://example.test/post/" + task.Recipe}}, nil
}

type resolverCreds struct {
	cfg map[string]map[string]any
	err error
}

func (c resolverCreds) Decrypted(_ context.Context, ownerType string, ownerID int64, provider, alias string) (map[string]any, error) {
	if c.err != nil {
		return nil, c.err
	}
	if cfg, ok := c.cfg[provider+"|"+alias]; ok {
		return cfg, nil
	}
	return nil, errors.New("no credential")
}

func newTestResolver(t *testing.T, creds CredentialReader) (*AdapterResolver, *resolverWorker) {
	t.Helper()
	store.Driver = "sqlite"
	raw, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	_, err = raw.Exec(store.Schema)
	require.NoError(t, err)

	bots := []struct {
		id      int64
		pack    string
		version int64
		account string
	}{
		{1, "xiaohongshu", 1, "brand"},
		{2, "zhihu", 1, "zhihu-brand"},
		{3, "wechat_mp", 1, "gh_123"},
		{4, "facebook", 1, "page-1"},
		{5, "ghost", 1, "ghost-account"},
		{6, "wechat_mp", 1, "gh_456"},
		{7, "mcpdemo", 1, "mcp-account"},
	}
	for _, b := range bots {
		_, err := raw.Exec(`INSERT INTO ops_bots
			(id, owner_type, owner_id, role, pack_id, pack_version, account_ref, display_name, status)
			VALUES (?, 'user', 2, 'account', ?, ?, ?, ?, 'active')`,
			b.id, b.pack, b.version, b.account, b.pack)
		require.NoError(t, err)
	}
	// Bot 6 pins an explicit credential alias; the resolver must honor it
	// instead of falling back to account_ref.
	_, err = raw.Exec(`UPDATE ops_bots SET credential_refs_json = '{"account":"wx-alias"}' WHERE id = 6`)
	require.NoError(t, err)

	registry := &packs.Registry{DataDir: t.TempDir()}
	require.NoError(t, packs.InstallBuiltins(registry, "user", 2))
	_, err = registry.Save("user", 2, packs.Manifest{
		ID: "mcpdemo", Version: 1, Status: "published", DisplayName: "MCP Demo",
		Capabilities: []string{"publish", "metrics"},
		ContentSchema: packs.ContentSchema{Formats: map[string]packs.ContentFormat{
			"post": {Fields: []string{"title", "body"}, MaxChars: 2000, MaxTitleChars: 100},
		}},
		MetricsMap: packs.MetricsMap{Fields: map[string]string{"views": "views"}},
		Bindings: map[string]packs.Binding{
			"publish": {Transport: "mcp", Provider: "demo-mcp", Operation: "publish_tool"},
			"metrics": {Transport: "mcp", Provider: "demo-mcp", Operation: "metrics_tool"},
		},
	}, nil)
	require.NoError(t, err)

	worker := &resolverWorker{}
	resolver := &AdapterResolver{
		Bots:        store.NewQueries(raw),
		Packs:       registry,
		Creds:       creds,
		Worker:      worker,
		Integration: &resolverOpsCaller{},
		MCP:         &resolverOpsCaller{},
	}
	return resolver, worker
}

func TestAdapterResolverBrowserPack(t *testing.T) {
	resolver, worker := newTestResolver(t, resolverCreds{})
	adapter, err := resolver.AdapterFor(context.Background(), 1)
	require.NoError(t, err)
	browserAdapter, ok := adapter.(*channel.BrowserAdapter)
	require.True(t, ok, "got %T", adapter)
	require.True(t, browserAdapter.Capabilities().Supports(channel.CapPublish))

	// The resolver bound the bot, so a non-numeric account ref still works.
	require.NoError(t, browserAdapter.EnsureSession(context.Background(), "brand"))

	receipt, err := browserAdapter.Publish(context.Background(), channel.BrowserPublishPayload{
		Title:  "笔记标题",
		Body:   "笔记正文",
		Images: []string{"/tmp/a.jpg"},
	})
	require.NoError(t, err)
	require.Contains(t, receipt.URL, "publish")
	require.Positive(t, worker.calls)
}

func TestAdapterResolverZhihuUsesGenericBrowserAdapter(t *testing.T) {
	resolver, _ := newTestResolver(t, resolverCreds{})
	adapter, err := resolver.AdapterFor(context.Background(), 2)
	require.NoError(t, err)
	require.IsType(t, &channel.BrowserAdapter{}, adapter)
	require.True(t, adapter.Capabilities().Supports(channel.CapReply))
}

func TestAdapterResolverWeChat(t *testing.T) {
	resolver, _ := newTestResolver(t, resolverCreds{cfg: map[string]map[string]any{
		"wechat_mp|gh_123": {"app_id": "app-id", "app_secret": "app-secret"},
	}})
	adapter, err := resolver.AdapterFor(context.Background(), 3)
	require.NoError(t, err)
	// Declarative pack -> the ONE generic openapi adapter; no WeChat Go code.
	require.IsType(t, &channel.OpenAPIAdapter{}, adapter)
	require.True(t, adapter.Capabilities().Supports(channel.CapPublish))
	require.True(t, adapter.Capabilities().Supports(channel.CapMetrics))
	require.True(t, adapter.Capabilities().Supports(channel.CapEngagements))
	require.True(t, adapter.Capabilities().Supports(channel.CapReply))
}

type resolverOpsCaller struct{}

func (c *resolverOpsCaller) CallOperation(context.Context, channel.OperationCall) (channel.OperationResult, error) {
	return channel.OperationResult{Status: 200, Body: map[string]any{}}, nil
}

func TestAdapterResolverHonorsCredentialAlias(t *testing.T) {
	resolver, _ := newTestResolver(t, resolverCreds{cfg: map[string]map[string]any{
		"wechat_mp|wx-alias": {"app_id": "app-id", "app_secret": "app-secret"},
	}})
	adapter, err := resolver.AdapterFor(context.Background(), 6)
	require.NoError(t, err)
	require.IsType(t, &channel.OpenAPIAdapter{}, adapter)
}

func TestAdapterResolverFacebook(t *testing.T) {
	resolver, _ := newTestResolver(t, resolverCreds{cfg: map[string]map[string]any{
		"facebook|page-1": {"page_token": "page-token"},
	}})
	adapter, err := resolver.AdapterFor(context.Background(), 4)
	require.NoError(t, err)
	require.IsType(t, &channel.OpenAPIAdapter{}, adapter)
	require.True(t, adapter.Capabilities().Supports(channel.CapEngagements))
}

func TestAdapterResolverMCPUsesGenericAdapter(t *testing.T) {
	resolver, _ := newTestResolver(t, resolverCreds{cfg: map[string]map[string]any{
		"mcpdemo|mcp-account": {"token": "tok"},
	}})
	adapter, err := resolver.AdapterFor(context.Background(), 7)
	require.NoError(t, err)
	require.IsType(t, &channel.OpenAPIAdapter{}, adapter)
	require.True(t, adapter.Capabilities().Supports(channel.CapPublish))
	require.True(t, adapter.Capabilities().Supports(channel.CapMetrics))
}

func TestAdapterResolverMissingCredentialIsUnavailable(t *testing.T) {
	resolver, _ := newTestResolver(t, resolverCreds{})
	_, err := resolver.AdapterFor(context.Background(), 3)
	var adapterErr *channel.AdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, CodeCapabilityUnavailable, adapterErr.Code)
}

func TestAdapterResolverUnknownPackIsUnavailable(t *testing.T) {
	resolver, _ := newTestResolver(t, resolverCreds{})
	_, err := resolver.AdapterFor(context.Background(), 5)
	var adapterErr *channel.AdapterError
	require.ErrorAs(t, err, &adapterErr)
	require.Equal(t, CodeCapabilityUnavailable, adapterErr.Code)
}

func TestAdapterResolverCachesPerBot(t *testing.T) {
	resolver, _ := newTestResolver(t, resolverCreds{})
	first, err := resolver.AdapterFor(context.Background(), 1)
	require.NoError(t, err)
	second, err := resolver.AdapterFor(context.Background(), 1)
	require.NoError(t, err)
	require.Same(t, first, second)
}

func TestLoadPackRecipesRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"name":"publish"}`), 0o600))
	recipesDir := filepath.Join(root, "browser", "recipes")
	require.NoError(t, os.MkdirAll(recipesDir, 0o700))
	if err := os.Symlink(target, filepath.Join(recipesDir, "publish.json")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	manifest := packs.Manifest{
		ID: "evil",
		Bindings: map[string]packs.Binding{
			"publish": {Transport: "browser", Recipe: "browser/recipes/publish.json"},
		},
	}
	_, err := loadPackRecipes(root, manifest)
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes the pack root")
}
