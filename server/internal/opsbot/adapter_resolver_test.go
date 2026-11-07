package opsbot

import (
	"context"
	"database/sql"
	"errors"
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

	worker := &resolverWorker{}
	resolver := &AdapterResolver{
		Bots:   store.NewQueries(raw),
		Packs:  registry,
		Creds:  creds,
		Worker: worker,
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
	require.IsType(t, &channel.WeChatAdapter{}, adapter)
	require.True(t, adapter.Capabilities().Supports(channel.CapPublish))
	require.False(t, adapter.Capabilities().Supports(channel.CapEngagements))
}

func TestAdapterResolverHonorsCredentialAlias(t *testing.T) {
	resolver, _ := newTestResolver(t, resolverCreds{cfg: map[string]map[string]any{
		"wechat_mp|wx-alias": {"app_id": "app-id", "app_secret": "app-secret"},
	}})
	adapter, err := resolver.AdapterFor(context.Background(), 6)
	require.NoError(t, err)
	require.IsType(t, &channel.WeChatAdapter{}, adapter)
}

func TestAdapterResolverFacebook(t *testing.T) {
	resolver, _ := newTestResolver(t, resolverCreds{cfg: map[string]map[string]any{
		"facebook|page-1": {"page_token": "page-token"},
	}})
	adapter, err := resolver.AdapterFor(context.Background(), 4)
	require.NoError(t, err)
	require.IsType(t, &channel.FacebookAdapter{}, adapter)
	require.True(t, adapter.Capabilities().Supports(channel.CapEngagements))
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
