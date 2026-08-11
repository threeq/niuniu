// Package sceneenv resolves a workspace's scene-projected environment variables
// and overlays them under the workspace's explicit env vars.
//
// A scene can declare env_preset assets (KEY=VALUE groups). On projection those
// presets are imported into the owner's env_presets library, but that import
// does NOT make them active in the workspace agent's process — every agent
// spawn path reads only workspace_env. This package bridges that gap: it reads
// the cached scene projection (workspace_scene_projection.projected_definition)
// and merges the scene-declared variables UNDER the explicit workspace_env rows,
// so a mounted scene's env vars actually reach the agent while any value the
// user set directly on the workspace still wins.
//
// It lives in its own leaf package (importing only store) because both the
// service layer (PTY agent) and the agentproxy layer (chat agent) must call it,
// and service already imports agentproxy — a shared helper in either of those
// packages would create an import cycle.
package sceneenv

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Querier is the subset of *store.Queries this package needs. *store.Queries
// satisfies it, and tests can supply a fake.
type Querier interface {
	ListWorkspaceEnv(ctx context.Context, workspaceID int64) ([]store.WorkspaceEnv, error)
	GetProjection(ctx context.Context, workspaceID int64) (store.WorkspaceSceneProjection, error)
	ListEnvAccounts(ctx context.Context) ([]store.EnvAccount, error)
}

// AccountRefPrefix is the placeholder an env value uses to reference a
// subscription-platform account's API key instead of embedding the secret
// inline, e.g. env value "${ACCOUNT:DeepSeek}" resolves to the api_key of the
// env_account named "DeepSeek". Env presets use this to keep credentials out of
// the readable config and let one account feed many presets/agents. The syntax
// is exact: the whole value must be "${ACCOUNT:<name>}".
const AccountRefPrefix = "${ACCOUNT:"

// envPresetAsset mirrors the wire shape of service.EnvPresetAsset — only the
// declared variables are needed. Decoded from the persisted projected_definition
// JSON, whose schema is stable.
type envPresetAsset struct {
	Env map[string]string `json:"env"`
}

type projectedDefinition struct {
	Assets struct {
		EnvPresets []envPresetAsset `json:"env_presets"`
	} `json:"assets"`
}

// SceneVars decodes the workspace's cached scene projection and returns the
// merged scene-declared env vars. Later env_preset entries override earlier ones
// (matching the projection's later-layer-wins merge order). Returns an empty map
// when no projection is cached, the cache is unreadable, or it declares no env
// presets — every failure mode degrades to "no scene env".
func SceneVars(ctx context.Context, q Querier, wsID int64) map[string]string {
	row, err := q.GetProjection(ctx, wsID)
	if err != nil || row.ProjectedDefinition == "" {
		return map[string]string{}
	}
	var def projectedDefinition
	if err := json.Unmarshal([]byte(row.ProjectedDefinition), &def); err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	for _, ep := range def.Assets.EnvPresets {
		for k, v := range ep.Env {
			out[k] = v
		}
	}
	return out
}

// Resolve returns the workspace's explicit env vars overlaid on top of its
// scene-projected env vars: every scene-declared variable is included unless the
// workspace already sets that key directly (explicit workspace_env wins). This
// is the env source every agent spawn path should use instead of a bare
// ListWorkspaceEnv so a mounted scene's variables actually reach the agent.
//
// Any env value that is a "${ACCOUNT:<name>}" reference is replaced with the
// referenced account's api_key (see SubstituteAccounts). Lookup failures keep
// the literal placeholder so the agent fails loudly if an account is missing.
func Resolve(ctx context.Context, q Querier, wsID int64) ([]store.WorkspaceEnv, error) {
	base, err := q.ListWorkspaceEnv(ctx, wsID)
	if err != nil {
		return nil, err
	}
	scene := SceneVars(ctx, q, wsID)
	var out []store.WorkspaceEnv
	if len(scene) == 0 {
		out = base
	} else {
		have := make(map[string]struct{}, len(base))
		for _, e := range base {
			have[e.Key] = struct{}{}
		}
		// Append scene keys the workspace doesn't already set, sorted for a
		// stable spawn environment.
		keys := make([]string, 0, len(scene))
		for k := range scene {
			if _, ok := have[k]; ok {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out = append([]store.WorkspaceEnv(nil), base...)
		for _, k := range keys {
			out = append(out, store.WorkspaceEnv{WorkspaceID: wsID, Key: k, Value: scene[k]})
		}
	}
	accounts, aerr := q.ListEnvAccounts(ctx)
	if aerr != nil {
		// Account lookup failure degrades to leaving placeholders unresolved.
		return out, nil
	}
	return SubstituteAccounts(accounts, out), nil
}

// SubstituteAccounts replaces any env value that is exactly "${ACCOUNT:<name>}"
// with the api_key of the account of that name. Values not shaped like an
// account reference pass through untouched. When an account is referenced but
// does not exist (or has an empty key) the placeholder is left in place so the
// agent fails loudly rather than silently using an empty credential. The input
// slice is not mutated; a new slice is returned.
func SubstituteAccounts(accounts []store.EnvAccount, rows []store.WorkspaceEnv) []store.WorkspaceEnv {
	if len(rows) == 0 {
		return rows
	}
	byName := make(map[string]string, len(accounts))
	for _, a := range accounts {
		byName[a.Name] = a.ApiKey
	}
	out := make([]store.WorkspaceEnv, len(rows))
	for i, e := range rows {
		out[i] = e
		if !isAccountRef(e.Value) {
			continue
		}
		name := e.Value[len(AccountRefPrefix) : len(e.Value)-1]
		if key, ok := byName[name]; ok && key != "" {
			out[i].Value = key
		}
	}
	return out
}

// isAccountRef reports whether v is exactly "${ACCOUNT:<name>}".
func isAccountRef(v string) bool {
	return len(v) > len(AccountRefPrefix)+1 &&
		strings.HasPrefix(v, AccountRefPrefix) &&
		strings.HasSuffix(v, "}") &&
		!strings.Contains(v[len(AccountRefPrefix):len(v)-1], "}")
}
