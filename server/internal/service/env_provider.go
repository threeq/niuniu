package service

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/niuniu-dev/niuniu/internal/sceneenv"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// EnvProviderService manages unified subscription-platform configs. A provider
// holds natural model-endpoint fields (a base_url per protocol, model, per-tier
// models) plus an API key that is a "${ACCOUNT:<name>}" reference to an
// env_accounts row (the credential always lives in an account, never inline).
// Expand (delegated to sceneenv.ExpandProvider) turns those fields into the
// correct environment key/value pair for a target agent, so the user configures
// endpoints once per protocol instead of hand-writing raw env var names per agent.
type EnvProviderService struct {
	q     *store.Queries
	db    *store.DB
	authz *Authz
}

func NewEnvProviderService(q *store.Queries, db *sql.DB, authz *Authz) *EnvProviderService {
	return &EnvProviderService{q: q, db: store.Wrap(db), authz: authz}
}

// Agent CLI-type keys (mirror adapter.Type values). The empty/default string
// means "Claude Code".
const (
	CLILauncherClaude = sceneenv.CLIClaude
	CLILauncherCodex  = sceneenv.CLICodex
	CLILauncherQwen   = sceneenv.CLIQwen
	CLILauncherOmp    = sceneenv.CLIOmp
	CLILauncherGoose  = sceneenv.CLIGoose
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
		BaseUrls:      p.BaseUrls,
		ApiKey:        p.ApiKey,
		Model:         p.Model,
		HaikuModel:    p.HaikuModel,
		SonnetModel:   p.SonnetModel,
		OpusModel:     p.OpusModel,
		SubagentModel: p.SubagentModel,
		ExtraEnv:      p.ExtraEnv,
		ContextWindow: p.ContextWindow,
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
		BaseUrls:      p.BaseUrls,
		ApiKey:        p.ApiKey,
		Model:         p.Model,
		HaikuModel:    p.HaikuModel,
		SonnetModel:   p.SonnetModel,
		OpusModel:     p.OpusModel,
		SubagentModel: p.SubagentModel,
		ExtraEnv:      p.ExtraEnv,
		ContextWindow: p.ContextWindow,
	})
}

func (s *EnvProviderService) Delete(ctx context.Context, id int64) error {
	return s.q.DeleteEnvProvider(ctx, id)
}

// Expand turns a provider's natural fields into the environment key/value pair
// for the given agent CLI type. The protocol is derived from the agent's
// cliType (claude→anthropic, others→openai) and the base_url is looked up from
// the provider's per-protocol base_urls map; the agent cliKey only adds
// per-agent extras (e.g. Codex reads NIUNIU_MODEL). preserveRef=false resolves a "${ACCOUNT:<name>}"
// api_key to the account's literal key (used for previews); preserveRef=true
// keeps the reference for runtime substitution. See sceneenv.ExpandProvider.
func (s *EnvProviderService) Expand(ctx context.Context, p store.EnvProvider, cliKey string, accounts []store.EnvAccount, preserveRef bool) map[string]string {
	return sceneenv.ExpandProvider(p, cliKey, accounts, preserveRef)
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
			Name: "智谱", Platform: "zhipu",
			BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
			Model:    "glm-5.1", HaikuModel: "glm-4.5-air", SonnetModel: "glm-5-turbo", OpusModel: "glm-5.1",
			// GLM-5.x ships a 1M window (glm-4.5's 200K default does not apply).
			ContextWindow: 1_000_000,
			ApiKey:        "${ACCOUNT:智谱}",
		},
		{
			Name: "MiniMax", Platform: "minimax",
			BaseUrls: `{"anthropic":"https://api.minimaxi.com/anthropic","openai":"https://api.minimaxi.com/v1"}`,
			Model:    "MiniMax-M2.7", HaikuModel: "MiniMax-M2.7", SonnetModel: "MiniMax-M2.7", OpusModel: "MiniMax-M2.7",
			ContextWindow: 1_000_000,
			ApiKey:        "${ACCOUNT:MiniMax}",
		},
		{
			Name: "DeepSeek", Platform: "deepseek",
			// DeepSeek exposes both an Anthropic-compatible and an OpenAI-compatible
			// endpoint, so one provider serves Claude (anthropic) and Codex/Qwen (openai).
			BaseUrls: `{"anthropic":"https://api.deepseek.com/anthropic","openai":"https://api.deepseek.com/v1"}`,
			Model:    "deepseek-v4-pro[1m]", HaikuModel: "deepseek-v4-flash", SonnetModel: "deepseek-v4-pro[1m]", OpusModel: "deepseek-v4-pro[1m]",
			SubagentModel: "deepseek-v4-flash", ContextWindow: 1_000_000, ApiKey: "${ACCOUNT:DeepSeek}",
			ExtraEnv: `{"CLAUDE_CODE_EFFORT_LEVEL":"max"}`,
		},
		{
			Name: "通义千问", Platform: "qwen",
			BaseUrls: `{"anthropic":"https://coding.dashscope.aliyuncs.com/apps/anthropic","openai":"https://dashscope.aliyuncs.com/compatible-mode/v1"}`,
			Model:    "qwen3.6-plus", HaikuModel: "qwen3.6-plus", SonnetModel: "qwen3.6-plus", OpusModel: "qwen3.6-plus",
			SubagentModel: "qwen3.6-plus", ContextWindow: 262_144, ApiKey: "${ACCOUNT:通义千问}",
		},
		{
			Name: "Kimi", Platform: "moonshot",
			BaseUrls: `{"anthropic":"https://api.moonshot.cn/anthropic","openai":"https://api.moonshot.cn/v1"}`,
			Model:    "kimi-k2.6", HaikuModel: "kimi-k2.6", SonnetModel: "kimi-k2.6", OpusModel: "kimi-k2.6",
			SubagentModel: "kimi-k2.6", ContextWindow: 262_144, ApiKey: "${ACCOUNT:Kimi}",
			ExtraEnv: `{"ENABLE_TOOL_SEARCH":"false"}`,
		},
		{
			Name: "火山方舟", Platform: "volcengine-ark",
			BaseUrls: `{"anthropic":"https://ark.cn-beijing.volces.com/api/coding","openai":"https://ark.cn-beijing.volces.com/api/v3"}`,
			Model:    "deepseek-v4-pro", HaikuModel: "deepseek-v4-flash", SonnetModel: "deepseek-v4-pro", OpusModel: "deepseek-v4-pro",
			SubagentModel: "deepseek-v4-pro", ContextWindow: 128_000, ApiKey: "${ACCOUNT:火山方舟}",
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
