package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// OneShotProviderMarker is a control key an owner adds to an env preset's env
// map to designate that preset as the provider that one-shot AI helper
// subprocesses (goal-condition suggest/classify, column-op suggest) authenticate
// against. One-shot calls run outside any workspace, so they cannot read
// workspace_env the way the chat agent does; this marker is how an owner points
// them at e.g. 智谱. Truthy values: "1"/"true"/"yes"/"on" (case-insensitive).
// The NIUNIU_ prefix means ResolveOneShotEnv (and adapter.InjectEnv) strip it
// before it can reach the CLI as a real env var.
const OneShotProviderMarker = "NIUNIU_ONESHOT_PROVIDER"

type EnvPresetService struct {
	q     *store.Queries
	db    *store.DB
	authz *Authz
}

func NewEnvPresetService(q *store.Queries, db *sql.DB, authz *Authz) *EnvPresetService {
	return &EnvPresetService{q: q, db: store.Wrap(db), authz: authz}
}

func (s *EnvPresetService) List(ctx context.Context) ([]store.EnvPreset, error) {
	return s.q.ListEnvPresets(ctx)
}

// ListForUser returns env presets accessible to userID (personal + org memberships).
func (s *EnvPresetService) ListForUser(ctx context.Context, userID int64) ([]store.EnvPreset, error) {
	owners, err := s.authz.Accessible(ctx, userID)
	if err != nil {
		return nil, err
	}
	orgIDs := owners.OrgIDs
	if len(orgIDs) == 0 {
		orgIDs = []int64{-1}
	}
	return s.q.ListEnvPresetsForOwners(ctx, store.ListEnvPresetsForOwnersParams{
		OwnerID: owners.UserID,
		OrgIds:  orgIDs,
	})
}

func (s *EnvPresetService) Get(ctx context.Context, id int64) (store.EnvPreset, error) {
	return s.q.GetEnvPreset(ctx, id)
}

func (s *EnvPresetService) Create(ctx context.Context, name, description string, env map[string]string, ownerType string, ownerID int64) (store.EnvPreset, error) {
	envJSON, err := json.Marshal(env)
	if err != nil {
		return store.EnvPreset{}, err
	}
	return s.q.CreateEnvPreset(ctx, store.CreateEnvPresetParams{
		Name:        name,
		Description: description,
		Env:         string(envJSON),
		OwnerType:   ownerType,
		OwnerID:     ownerID,
	})
}

func (s *EnvPresetService) Update(ctx context.Context, id int64, name, description string, env map[string]string) error {
	envJSON, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return s.q.UpdateEnvPreset(ctx, store.UpdateEnvPresetParams{
		ID:          id,
		Name:        name,
		Description: description,
		Env:         string(envJSON),
	})
}

func (s *EnvPresetService) Delete(ctx context.Context, id int64) error {
	return s.q.DeleteEnvPreset(ctx, id)
}

// ResolveOneShotEnv returns the env (KEY=VALUE strings, NIUNIU_* control keys
// stripped) of the preset marked with OneShotProviderMarker, or nil when none
// is marked. When several presets carry the marker the lowest-ID one wins
// (deterministic) and the collision is logged. Best-effort: any error yields nil
// so one-shot helpers fall back to the host env alone.
//
// Resolution is host-wide (scans every preset, no owner filter): niuniu-desktop
// has a single local owner, and one-shot helpers run without a workspace/owner
// context, so a single host-level designation is the right granularity.
func (s *EnvPresetService) ResolveOneShotEnv(ctx context.Context) []string {
	presets, err := s.q.ListEnvPresets(ctx)
	if err != nil {
		slog.Warn("ResolveOneShotEnv: list presets failed", "error", err)
		return nil
	}
	var chosen *store.EnvPreset
	marked := 0
	for i := range presets {
		env := decodeEnvPresetEnv(presets[i].Env)
		if !isTruthyMarker(env[OneShotProviderMarker]) {
			continue
		}
		marked++
		if chosen == nil || presets[i].ID < chosen.ID {
			p := presets[i]
			chosen = &p
		}
	}
	if chosen == nil {
		return nil
	}
	if marked > 1 {
		slog.Warn("ResolveOneShotEnv: multiple presets marked for one-shot; using lowest ID",
			"chosen_id", chosen.ID, "chosen_name", chosen.Name, "marked_count", marked)
	}
	env := decodeEnvPresetEnv(chosen.Env)
	out := make([]string, 0, len(env))
	for k, v := range env {
		if strings.HasPrefix(k, "NIUNIU_") {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

// decodeEnvPresetEnv parses an env preset's stored JSON env map, returning an
// empty (non-nil) map on any decode error.
func decodeEnvPresetEnv(raw string) map[string]string {
	m := map[string]string{}
	if raw == "" {
		return m
	}
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}

// isTruthyMarker reports whether a marker value enables the flag.
func isTruthyMarker(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func (s *EnvPresetService) SeedDefaults(ctx context.Context) error {
	presets, err := s.q.ListEnvPresets(ctx)
	if err != nil {
		return err
	}
	existing := make(map[string]bool, len(presets))
	for _, p := range presets {
		existing[p.Name] = true
	}

	defaults := []struct {
		name        string
		description string
		env         map[string]string
	}{
		{
			name:        "智谱",
			description: "智谱 GLM 系列模型",
			env: map[string]string{
				"ANTHROPIC_AUTH_TOKEN":                     ".",
				"ANTHROPIC_BASE_URL":                       "https://open.bigmodel.cn/api/anthropic",
				"API_TIMEOUT_MS":                           "3000000",
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":            "glm-4.5-air",
				"ANTHROPIC_DEFAULT_SONNET_MODEL":           "glm-5-turbo",
				"ANTHROPIC_DEFAULT_OPUS_MODEL":             "glm-5.1",
			},
		},
		{
			name:        "MiniMax",
			description: "MiniMax M2.7 模型",
			env: map[string]string{
				"ANTHROPIC_BASE_URL":                       "https://api.minimaxi.com/anthropic",
				"ANTHROPIC_AUTH_TOKEN":                     "sk-cp-",
				"API_TIMEOUT_MS":                           "3000000",
				"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
				"ANTHROPIC_MODEL":                          "MiniMax-M2.7",
				"ANTHROPIC_SMALL_FAST_MODEL":               "MiniMax-M2.7",
				"ANTHROPIC_DEFAULT_SONNET_MODEL":           "MiniMax-M2.7",
				"ANTHROPIC_DEFAULT_OPUS_MODEL":             "MiniMax-M2.7",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":            "MiniMax-M2.7",
			},
		},
		{
			name:        "DeepSeek",
			description: "DeepSeek V4",
			env: map[string]string{
				"ANTHROPIC_BASE_URL":             "https://api.deepseek.com/anthropic",
				"ANTHROPIC_AUTH_TOKEN":           "sk-",
				"ANTHROPIC_MODEL":                "deepseek-v4-pro[1m]",
				"ANTHROPIC_DEFAULT_OPUS_MODEL":   "deepseek-v4-pro[1m]",
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek-v4-pro[1m]",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "deepseek-v4-flash",
				"CLAUDE_CODE_SUBAGENT_MODEL":     "deepseek-v4-flash",
				"CLAUDE_CODE_EFFORT_LEVEL":       "max",
			},
		},
		{
			name:        "通义千问",
			description: "通义千问 Qwen 3.6",
			env: map[string]string{
				"ANTHROPIC_AUTH_TOKEN":           "YOUR_API_KEY",
				"ANTHROPIC_BASE_URL":             "https://coding.dashscope.aliyuncs.com/apps/anthropic",
				"ANTHROPIC_MODEL":                "qwen3.6-plus",
				"ANTHROPIC_SMALL_FAST_MODEL":     "qwen3.6-plus",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "qwen3.6-plus",
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "qwen3.6-plus",
				"ANTHROPIC_DEFAULT_OPUS_MODEL":   "qwen3.6-plus",
				"CLAUDE_CODE_SUBAGENT_MODEL":     "qwen3.6-plus",
			},
		},
		{
			name:        "Kimi",
			description: "Kimi K2.6",
			env: map[string]string{
				"ANTHROPIC_BASE_URL":             "https://api.moonshot.cn/anthropic",
				"ANTHROPIC_AUTH_TOKEN":           "${YOUR_MOONSHOT_API_KEY}",
				"ANTHROPIC_MODEL":                "kimi-k2.6",
				"ANTHROPIC_DEFAULT_OPUS_MODEL":   "kimi-k2.6",
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "kimi-k2.6",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "kimi-k2.6",
				"CLAUDE_CODE_SUBAGENT_MODEL":     "kimi-k2.6",
				"ENABLE_TOOL_SEARCH":             "false",
			},
		},
		{
			name:        "火山方舟",
			description: "火山方舟 DeepSeek V4",
			env: map[string]string{
				"ANTHROPIC_AUTH_TOKEN":           "-",
				"ANTHROPIC_BASE_URL":             "https://ark.cn-beijing.volces.com/api/coding",
				"ANTHROPIC_MODEL":                "deepseek-v4-pro",
				"ANTHROPIC_DEFAULT_OPUS_MODEL":   "deepseek-v4-pro",
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek-v4-pro",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "deepseek-v4-flash",
				"CLAUDE_CODE_SUBAGENT_MODEL":     "deepseek-v4-pro",
			},
		},
	}

	for _, d := range defaults {
		if existing[d.name] {
			continue
		}
		envJSON, _ := json.Marshal(d.env)
		// owner_type='user' + owner_id=0 satisfies the schema CHECK constraint
		// (owner_type IN ('user','org')) while marking the row as system-wide.
		// ListEnvPresetsForOwners surfaces owner_id=0 rows to every caller in
		// addition to their personal/org scope — see env_presets_owner_filter.sql.
		_, err := s.q.CreateEnvPreset(ctx, store.CreateEnvPresetParams{
			Name:        d.name,
			Description: d.description,
			Env:         string(envJSON),
			OwnerType:   "user",
			OwnerID:     0,
		})
		if err != nil {
			slog.Warn("seed env preset failed", "name", d.name, "error", err)
		}
	}
	return nil
}
