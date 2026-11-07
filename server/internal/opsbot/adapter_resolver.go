package opsbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// CodeCapabilityUnavailable marks a pack/credential gap the publish
// orchestrator can degrade to a manual package.
const CodeCapabilityUnavailable = "capability_unavailable"

// AdapterBotStore is the bot lookup AdapterResolver needs. *store.Queries
// satisfies it.
type AdapterBotStore interface {
	GetOpsBot(ctx context.Context, id int64) (store.OpsBot, error)
}

var _ AdapterBotStore = (*store.Queries)(nil)

// CredentialReader resolves the decrypted credential config for one account.
// The implementation owns decryption; plaintext must never be logged.
type CredentialReader interface {
	Decrypted(ctx context.Context, ownerType string, ownerID int64, provider, alias string) (map[string]any, error)
}

// AdapterResolver turns a bot id into a ChannelAdapter by reading the bot's
// Pack bindings and its account credential. Every failure is an AdapterError
// with CodeCapabilityUnavailable so the caller degrades to a manual package
// instead of retrying forever.
type AdapterResolver struct {
	Bots  AdapterBotStore
	Packs *packs.Registry
	Creds CredentialReader
	// Worker runs browser recipes. Nil makes browser-bound packs unavailable.
	Worker channel.BrowserWorker
	// Manual is the adapter for packs bound to manual hand-off.
	Manual *channel.ManualAdapter

	// WeChatClient overrides the WeChat client factory in tests.
	WeChatClient func(ctx context.Context, bot store.OpsBot, cfg map[string]any) (channel.WeChatClient, error)
	// FacebookClient overrides the Graph client factory in tests.
	FacebookClient func(ctx context.Context, bot store.OpsBot, cfg map[string]any) (channel.FacebookClient, error)

	mu    sync.Mutex
	cache map[int64]resolvedAdapter
}

type resolvedAdapter struct {
	version int64
	updated int64
	adapter channel.ChannelAdapter
}

// AdapterFor resolves the account's platform adapter. The result is cached per
// bot until the pack version or bot config changes.
func (r *AdapterResolver) AdapterFor(ctx context.Context, botID int64) (channel.ChannelAdapter, error) {
	if r == nil || r.Bots == nil || r.Packs == nil {
		return nil, &channel.AdapterError{
			Capability: channel.CapPublish,
			Code:       CodeCapabilityUnavailable,
			Err:        errors.New("opsbot: adapter resolver is not configured"),
		}
	}
	bot, err := r.Bots.GetOpsBot(ctx, botID)
	if err != nil {
		return nil, err
	}
	if cached, ok := r.cached(bot); ok {
		return cached, nil
	}

	manifest, err := r.manifest(ctx, bot)
	if err != nil {
		return nil, unavailable(channel.CapPublish, fmt.Sprintf("bot %d: load pack %s: %v", bot.ID, bot.PackID, err))
	}
	binding, ok := packBinding(manifest)
	if !ok {
		return nil, unavailable(channel.CapPublish, fmt.Sprintf("pack %s has no usable binding", manifest.ID))
	}

	var adapter channel.ChannelAdapter
	switch binding.Transport {
	case "manual":
		if r.Manual == nil {
			return nil, unavailable(channel.CapPublish, "manual adapter is not configured")
		}
		adapter = r.Manual
	case "browser":
		built, buildErr := r.browserAdapter(bot, manifest)
		if buildErr != nil {
			return nil, buildErr
		}
		adapter = built
	case "openapi":
		built, buildErr := r.openAPIAdapter(ctx, bot, manifest, binding)
		if buildErr != nil {
			return nil, buildErr
		}
		adapter = built
	case "mcp":
		built, buildErr := r.mcpAdapter(ctx, bot, binding)
		if buildErr != nil {
			return nil, buildErr
		}
		adapter = built
	default:
		return nil, unavailable(channel.CapPublish, fmt.Sprintf("pack %s: transport %q is not supported", manifest.ID, binding.Transport))
	}

	r.store(bot, adapter)
	return adapter, nil
}

func (r *AdapterResolver) manifest(ctx context.Context, bot store.OpsBot) (packs.Manifest, error) {
	ownerType, ownerID := bot.OwnerType, bot.OwnerID
	if bot.PackVersion > 0 {
		return r.Packs.Load(ownerType, ownerID, bot.PackID, int(bot.PackVersion))
	}
	return r.Packs.LatestPublished(ownerType, ownerID, bot.PackID)
}

func (r *AdapterResolver) browserAdapter(bot store.OpsBot, manifest packs.Manifest) (channel.ChannelAdapter, error) {
	if r.Worker == nil {
		return nil, unavailable(channel.CapPublish, "browser worker is not configured")
	}
	root, err := r.Packs.Root(bot.OwnerType, bot.OwnerID, manifest.ID, manifest.Version)
	if err != nil {
		return nil, unavailable(channel.CapPublish, fmt.Sprintf("pack %s: %v", manifest.ID, err))
	}
	recipes, err := loadPackRecipes(root, manifest)
	if err != nil {
		return nil, unavailable(channel.CapPublish, err.Error())
	}
	limits, metricMap := packLimits(manifest)
	adapter := channel.NewBrowserAdapter(r.Worker, recipes, manifest.Capabilities, limits, metricMap)
	// Browser sessions are keyed by bot id; bind it here so callers that only
	// know the platform account name can still use the adapter.
	adapter.BindAccount(bot.ID)
	return adapter, nil
}

func (r *AdapterResolver) openAPIAdapter(ctx context.Context, bot store.OpsBot, manifest packs.Manifest, binding packs.Binding) (channel.ChannelAdapter, error) {
	switch binding.Provider {
	case "facebook-graph":
		cfg, err := r.credentials(ctx, bot)
		if err != nil {
			return nil, err
		}
		if r.FacebookClient != nil {
			client, err := r.FacebookClient(ctx, bot, cfg)
			if err != nil {
				return nil, unavailable(channel.CapPublish, fmt.Sprintf("facebook client: %v", err))
			}
			return channel.NewFacebookAdapter(client), nil
		}
		token := firstCredential(cfg, "page_token", "access_token", "token")
		if token == "" {
			return nil, unavailable(channel.CapPublish, "facebook page token is not configured")
		}
		return channel.NewFacebookAdapter(channel.NewGraphHTTPClient(token)), nil
	default:
		return nil, unavailable(channel.CapPublish, fmt.Sprintf("pack %s: openapi provider %q is not wired", manifest.ID, binding.Provider))
	}
}

func (r *AdapterResolver) mcpAdapter(ctx context.Context, bot store.OpsBot, binding packs.Binding) (channel.ChannelAdapter, error) {
	switch binding.Provider {
	case "wechat-mp":
		cfg, err := r.credentials(ctx, bot)
		if err != nil {
			return nil, err
		}
		if r.WeChatClient != nil {
			client, err := r.WeChatClient(ctx, bot, cfg)
			if err != nil {
				return nil, unavailable(channel.CapPublish, fmt.Sprintf("wechat client: %v", err))
			}
			return channel.NewWeChatAdapter(client), nil
		}
		appID := firstCredential(cfg, "app_id", "appid")
		secret := firstCredential(cfg, "app_secret", "appsecret", "secret")
		if appID == "" || secret == "" {
			return nil, unavailable(channel.CapPublish, "wechat app_id/app_secret are not configured")
		}
		return channel.NewWeChatAdapter(channel.NewWeChatAPIClient(appID, secret)), nil
	default:
		return nil, unavailable(channel.CapPublish, fmt.Sprintf("pack %s: mcp provider %q is not wired", bot.PackID, binding.Provider))
	}
}

func (r *AdapterResolver) credentials(ctx context.Context, bot store.OpsBot) (map[string]any, error) {
	if r.Creds == nil {
		return nil, unavailable(channel.CapPublish, "credential store is not configured")
	}
	// credential_refs_json may pin an explicit account credential alias; when
	// it does not, the account ref is the alias the onboarding link used.
	alias := credentialAlias(bot)
	cfg, err := r.Creds.Decrypted(ctx, bot.OwnerType, bot.OwnerID, bot.PackID, alias)
	if err != nil {
		return nil, unavailable(channel.CapPublish,
			fmt.Sprintf("credential for %s/%s is unavailable: %v", bot.PackID, alias, err))
	}
	return cfg, nil
}

func credentialAlias(bot store.OpsBot) string {
	var refs map[string]any
	if strings.TrimSpace(bot.CredentialRefsJson) != "" {
		if err := json.Unmarshal([]byte(bot.CredentialRefsJson), &refs); err == nil {
			if alias, ok := refs["account"].(string); ok && strings.TrimSpace(alias) != "" {
				return strings.TrimSpace(alias)
			}
		}
	}
	return strings.TrimSpace(bot.AccountRef)
}

func (r *AdapterResolver) cached(bot store.OpsBot) (channel.ChannelAdapter, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[bot.ID]
	if !ok || entry.version != bot.PackVersion || entry.updated != bot.UpdatedAt.UnixNano() {
		return nil, false
	}
	return entry.adapter, true
}

func (r *AdapterResolver) store(bot store.OpsBot, adapter channel.ChannelAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = make(map[int64]resolvedAdapter)
	}
	r.cache[bot.ID] = resolvedAdapter{version: bot.PackVersion, updated: bot.UpdatedAt.UnixNano(), adapter: adapter}
}

func unavailable(capability channel.Capability, msg string) error {
	return &channel.AdapterError{Capability: capability, Code: CodeCapabilityUnavailable, Err: errors.New(msg)}
}

// packBinding picks the capability binding that describes the platform
// transport: publish first, then the remaining capabilities in a stable order.
func packBinding(m packs.Manifest) (packs.Binding, bool) {
	if b, ok := m.Bindings[string(channel.CapPublish)]; ok && strings.TrimSpace(b.Transport) != "" {
		return b, true
	}
	keys := make([]string, 0, len(m.Bindings))
	for key := range m.Bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if b := m.Bindings[key]; strings.TrimSpace(b.Transport) != "" {
			return b, true
		}
	}
	return packs.Binding{}, false
}

// optionalRecipeNames are loaded when present even though the manifest does not
// bind them: login/health drive session checks and publish_video is selected at
// publish time for a video payload.
var optionalRecipeNames = []string{"login", "health", "publish_video"}

func optionalRecipe(name string) bool {
	for _, candidate := range optionalRecipeNames {
		if name == candidate {
			return true
		}
	}
	return false
}

func loadPackRecipes(root string, m packs.Manifest) (channel.RecipeSet, error) {
	recipes := channel.RecipeSet{}
	load := func(name, rel string) error {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			return nil
		}
		full, err := insidePack(root, rel)
		if errors.Is(err, os.ErrNotExist) && optionalRecipe(name) {
			return nil
		}
		if err != nil {
			return err
		}
		data, err := os.ReadFile(full)
		if errors.Is(err, os.ErrNotExist) {
			if optionalRecipe(name) {
				return nil
			}
			return fmt.Errorf("pack %s: recipe %s is missing", m.ID, rel)
		}
		if err != nil {
			return fmt.Errorf("pack %s: read recipe %s: %w", m.ID, rel, err)
		}
		var recipe packs.Recipe
		if err := json.Unmarshal(data, &recipe); err != nil {
			return fmt.Errorf("pack %s: decode recipe %s: %w", m.ID, rel, err)
		}
		recipes[name] = recipe
		return nil
	}

	keys := make([]string, 0, len(m.Bindings))
	for key := range m.Bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b := m.Bindings[key]
		if b.Transport == "browser" {
			if err := load(key, b.Recipe); err != nil {
				return nil, err
			}
		}
	}
	for _, name := range optionalRecipeNames {
		if err := load(name, "browser/recipes/"+name+".json"); err != nil {
			return nil, err
		}
	}
	return recipes, nil
}

func insidePack(root, rel string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("pack %s: resolve root: %w", filepath.Base(root), err)
	}
	realRoot = filepath.Clean(realRoot)
	full := filepath.Clean(filepath.Join(realRoot, filepath.FromSlash(rel)))
	if !pathWithin(realRoot, full) {
		return "", fmt.Errorf("pack %s: recipe path %q escapes the pack root", filepath.Base(realRoot), rel)
	}
	// Resolve symlinks in the target too: a recipe file that links outside the
	// pack would otherwise read foreign content.
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		// Optional recipes may legitimately be absent; lexical containment was
		// already checked above, so let the caller's ReadFile report ENOENT.
		if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
			return full, nil
		}
		return "", fmt.Errorf("pack %s: recipe path %q: %w", filepath.Base(realRoot), rel, err)
	}
	if !pathWithin(realRoot, realFull) {
		return "", fmt.Errorf("pack %s: recipe path %q escapes the pack root", filepath.Base(realRoot), rel)
	}
	return realFull, nil
}

func pathWithin(root, target string) bool {
	inside, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return inside != ".." && !strings.HasPrefix(inside, ".."+string(filepath.Separator))

}

func packLimits(m packs.Manifest) (channel.BrowserLimits, map[string]string) {
	format, ok := m.ContentSchema.Formats["post"]
	if !ok {
		format, ok = m.ContentSchema.Formats["article"]
	}
	if !ok {
		format, ok = m.ContentSchema.Formats["note"]
	}
	if !ok {
		keys := make([]string, 0, len(m.ContentSchema.Formats))
		for key := range m.ContentSchema.Formats {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			format = m.ContentSchema.Formats[keys[0]]
		}
	}
	limits := channel.BrowserLimits{
		MaxChars:      format.MaxChars,
		MaxTitleChars: format.MaxTitleChars,
		MaxImages:     format.MaxImages,
		ImageRatio:    format.ImageRatio,
		VideoSec:      format.VideoSec,
	}
	return limits, m.MetricsMap.Fields
}

func firstCredential(cfg map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := cfg[key]
		if !ok {
			continue
		}
		if s, ok := value.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	return ""
}
