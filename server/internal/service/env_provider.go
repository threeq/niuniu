package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/sceneenv"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// EnvProviderService manages unified subscription-platform configs. A provider
// holds natural model-endpoint fields (base_url, model, per-tier models) plus an
// API key that is either a literal secret or a "${ACCOUNT:<name>}" reference to
// an env_accounts row. Expand turns those fields into the correct environment
// key/value pair for a target agent's CLI type, so the user configures
// model/endpoint once instead of hand-writing raw env var names per agent.
type EnvProviderService struct {
	q     *store.Queries
	db    *store.DB
	authz *Authz
}

func NewEnvProviderService(q *store.Queries, db *sql.DB, authz *Authz) *EnvProviderService {
	return &EnvProviderService{q: q, db: store.Wrap(db), authz: authz}
}

// Agent CLI-type keys (mirror adapter.Type values). The empty/default string
// means "Claude Code", the primary Anthropic-compatible target.
const (
	CLILauncherClaude = "claude"
	CLILauncherCodex  = "codex"
	CLILauncherQwen   = "qwen"
	CLILauncherOmp    = "omp"
	CLILauncherGoose  = "goose"
)

func (s *EnvProviderService) List(ctx context.Context) ([]store.EnvProvider, error) {
	return s.q.ListEnvProviders(ctx)
}

func (s *EnvProviderService) ListForUser(ctx context.Context, userID int64) ([]store.EnvProvider, error) {
	owners, err := s.authz.Accessible(ctx, userID)
	if err != nil {
		return nil, err
	}
	orgIDs := owners.OrgIDs
	if len(orgIDs) == 0 {
		orgIDs = []int64{-1}
	}
	return s.q.ListEnvProvidersForOwners(ctx, store.ListEnvProvidersForOwnersParams{
		OwnerID: owners.UserID,
		OrgIds:  orgIDs,
	})
}

func (s *EnvProviderService) Get(ctx context.Context, id int64) (store.EnvProvider, error) {
	return s.q.GetEnvProvider(ctx, id)
}

func (s *EnvProviderService) Create(ctx context.Context, p store.EnvProvider) (store.EnvProvider, error) {
	return s.q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name:          p.Name,
		Platform:      p.Platform,
		Description:   p.Description,
		BaseUrl:       p.BaseUrl,
		ApiKey:        p.ApiKey,
		Model:         p.Model,
		HaikuModel:    p.HaikuModel,
		SonnetModel:   p.SonnetModel,
		OpusModel:     p.OpusModel,
		SubagentModel: p.SubagentModel,
		ExtraEnv:      p.ExtraEnv,
		OwnerType:     p.OwnerType,
		OwnerID:       p.OwnerID,
	})
}

func (s *EnvProviderService) Update(ctx context.Context, id int64, p store.EnvProvider) error {
	return s.q.UpdateEnvProvider(ctx, store.UpdateEnvProviderParams{
		ID:            id,
		Name:          p.Name,
		Platform:      p.Platform,
		Description:   p.Description,
		BaseUrl:       p.BaseUrl,
		ApiKey:        p.ApiKey,
		Model:         p.Model,
		HaikuModel:    p.HaikuModel,
		SonnetModel:   p.SonnetModel,
		OpusModel:     p.OpusModel,
		SubagentModel: p.SubagentModel,
		ExtraEnv:      p.ExtraEnv,
	})
}

func (s *EnvProviderService) Delete(ctx context.Context, id int64) error {
	return s.q.DeleteEnvProvider(ctx, id)
}

// Expand turns a provider's natural fields into the environment key/value pair
// for the given agent CLI type. Only non-empty fields are emitted; the API key
// undergoes ${ACCOUNT:<name>} substitution via the supplied accounts (see
// resolveProviderKey), and any extra_env passthrough entries are merged last so
// users can override a generated key. Returns an empty map for an empty
// provider (no fields set).
//
// cliKey is one of the CLILauncher* constants; "" defaults to Claude Code.
func (s *EnvProviderService) Expand(ctx context.Context, p store.EnvProvider, cliKey string, accounts []store.EnvAccount) map[string]string {
	if cliKey == "" || cliKey == CLILauncherClaude {
		return s.expandClaude(p, accounts)
	}
	out := map[string]string{}
	if p.BaseUrl != "" {
		out["OPENAI_BASE_URL"] = p.BaseUrl
	}
	if key := resolveProviderKey(p.ApiKey, accounts); key != "" {
		out["OPENAI_API_KEY"] = key
	}
	if p.Model != "" {
		out["OPENAI_MODEL"] = p.Model
	}
	// niuniu's Codex path reads the model from NIUNIU_MODEL control key.
	if cliKey == CLILauncherCodex && p.Model != "" {
		out["NIUNIU_MODEL"] = p.Model
	}
	mergeExtraEnv(out, p.ExtraEnv)
	return out
}

// expandClaude produces the Anthropic-compatible env used by Claude Code (and
// any CLI that talks to an /anthropic endpoint). High-confidence mapping.
func (s *EnvProviderService) expandClaude(p store.EnvProvider, accounts []store.EnvAccount) map[string]string {
	out := map[string]string{}
	if p.BaseUrl != "" {
		out["ANTHROPIC_BASE_URL"] = p.BaseUrl
	}
	if key := resolveProviderKey(p.ApiKey, accounts); key != "" {
		out["ANTHROPIC_AUTH_TOKEN"] = key
	}
	if p.Model != "" {
		out["ANTHROPIC_MODEL"] = p.Model
	}
	if p.HaikuModel != "" {
		out["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = p.HaikuModel
	}
	if p.SonnetModel != "" {
		out["ANTHROPIC_DEFAULT_SONNET_MODEL"] = p.SonnetModel
	}
	if p.OpusModel != "" {
		out["ANTHROPIC_DEFAULT_OPUS_MODEL"] = p.OpusModel
	}
	if p.SubagentModel != "" {
		out["CLAUDE_CODE_SUBAGENT_MODEL"] = p.SubagentModel
	}
	mergeExtraEnv(out, p.ExtraEnv)
	return out
}

// resolveProviderKey returns the API key to inject, resolving a
// "${ACCOUNT:<name>}" reference against the supplied accounts. A reference with
// no matching account (or an empty literal) yields "" so the mapping simply
// omits the credential key rather than emitting a broken value.
func resolveProviderKey(raw string, accounts []store.EnvAccount) string {
	if !isProviderAccountRef(raw) {
		return raw
	}
	name := raw[len(sceneenv.AccountRefPrefix) : len(raw)-1]
	for _, a := range accounts {
		if a.Name == name {
			return a.ApiKey
		}
	}
	slog.Warn("expand provider: account ref unresolved", "account", name)
	return ""
}

// isProviderAccountRef reports whether v is exactly "${ACCOUNT:<name>}".
func isProviderAccountRef(v string) bool {
	return len(v) > len(sceneenv.AccountRefPrefix)+1 &&
		strings.HasPrefix(v, sceneenv.AccountRefPrefix) &&
		strings.HasSuffix(v, "}") &&
		!strings.Contains(v[len(sceneenv.AccountRefPrefix):len(v)-1], "}")
}

// mergeExtraEnv decodes a provider's extra_env JSON passthrough and overlays it
// on out (highest priority), so users can add/override provider-specific vars.
func mergeExtraEnv(out map[string]string, raw string) {
	if raw == "" {
		return
	}
	var extra map[string]string
	if json.Unmarshal([]byte(raw), &extra) != nil {
		return
	}
	for k, v := range extra {
		out[k] = v
	}
}

// SeedDefaults seeds a small set of system-wide (owner_id=0) providers for the
// platforms the default env presets reference, with the well-known endpoints
// and default models so a fresh install has somewhere to attach an account.
// Providers are created only if their name is absent; existing rows (including
// user-edited ones) are never overwritten.
func (s *EnvProviderService) SeedDefaults(ctx context.Context) error {
	providers, err := s.q.ListEnvProviders(ctx)
	if err != nil {
		return err
	}
	existing := make(map[string]bool, len(providers))
	for _, p := range providers {
		existing[p.Name] = true
	}

	defaults := []store.CreateEnvProviderParams{
		{
			Name: "智谱", Platform: "zhipu", BaseUrl: "https://open.bigmodel.cn/api/anthropic",
			Model: "glm-5.1", HaikuModel: "glm-4.5-air", SonnetModel: "glm-5-turbo", OpusModel: "glm-5.1",
			ApiKey: "${ACCOUNT:智谱}",
		},
		{
			Name: "MiniMax", Platform: "minimax", BaseUrl: "https://api.minimaxi.com/anthropic",
			Model: "MiniMax-M2.7", HaikuModel: "MiniMax-M2.7", SonnetModel: "MiniMax-M2.7", OpusModel: "MiniMax-M2.7",
			ApiKey: "${ACCOUNT:MiniMax}",
		},
		{
			Name: "DeepSeek", Platform: "deepseek", BaseUrl: "https://api.deepseek.com/anthropic",
			Model: "deepseek-v4-pro[1m]", HaikuModel: "deepseek-v4-flash", SonnetModel: "deepseek-v4-pro[1m]", OpusModel: "deepseek-v4-pro[1m]",
			SubagentModel: "deepseek-v4-flash", ApiKey: "${ACCOUNT:DeepSeek}",
			ExtraEnv: `{"CLAUDE_CODE_EFFORT_LEVEL":"max"}`,
		},
		{
			Name: "通义千问", Platform: "qwen", BaseUrl: "https://coding.dashscope.aliyuncs.com/apps/anthropic",
			Model: "qwen3.6-plus", HaikuModel: "qwen3.6-plus", SonnetModel: "qwen3.6-plus", OpusModel: "qwen3.6-plus",
			SubagentModel: "qwen3.6-plus", ApiKey: "${ACCOUNT:通义千问}",
		},
		{
			Name: "Kimi", Platform: "moonshot", BaseUrl: "https://api.moonshot.cn/anthropic",
			Model: "kimi-k2.6", HaikuModel: "kimi-k2.6", SonnetModel: "kimi-k2.6", OpusModel: "kimi-k2.6",
			SubagentModel: "kimi-k2.6", ApiKey: "${ACCOUNT:Kimi}",
			ExtraEnv: `{"ENABLE_TOOL_SEARCH":"false"}`,
		},
		{
			Name: "火山方舟", Platform: "volcengine-ark", BaseUrl: "https://ark.cn-beijing.volces.com/api/coding",
			Model: "deepseek-v4-pro", HaikuModel: "deepseek-v4-flash", SonnetModel: "deepseek-v4-pro", OpusModel: "deepseek-v4-pro",
			SubagentModel: "deepseek-v4-pro", ApiKey: "${ACCOUNT:火山方舟}",
		},
	}
	for _, d := range defaults {
		if existing[d.Name] {
			continue
		}
		d.OwnerType = "user"
		d.OwnerID = 0
		if _, err := s.q.CreateEnvProvider(ctx, d); err != nil {
			slog.Warn("seed env provider failed", "name", d.Name, "error", err)
		}
	}
	return nil
}