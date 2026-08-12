// Provider expansion: turns a unified subscription-platform config
// (env_providers) into the environment key/value pair for a target agent CLI.
//
// A provider declares its API protocol — "anthropic" (base_url is an
// /anthropic-compatible endpoint, e.g. open.bigmodel.cn/api/anthropic) or
// "openai" (base_url is an OpenAI-compatible endpoint, e.g. api.deepseek.com/v1)
// — because different subscription platforms differ in protocol and endpoint.
// The protocol, not the agent type, decides the env family emitted; the agent
// CLI type only adds per-agent extras (e.g. niuniu's Codex reads NIUNIU_MODEL).
package sceneenv

import (
	"encoding/json"
	"log/slog"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Provider protocol keys (env_providers.protocol).
const (
	ProviderProtocolAnthropic = "anthropic"
	ProviderProtocolOpenAI    = "openai"
)

// CLILauncher keys (mirror adapter.Type). The empty/default string means
// "Claude Code".
const (
	CLIClaude = "claude"
	CLICodex  = "codex"
	CLIQwen   = "qwen"
	CLIOmp    = "omp"
	CLIGoose  = "goose"
)

// ProtocolForCLI returns the API protocol a given agent CLI type consumes.
// Claude Code reads Anthropic-protocol env (ANTHROPIC_*); every other CLI
// (codex/qwen/omp/goose) reads OpenAI-protocol env (OPENAI_*). This mapping is
// how one provider — which can hold a base_url per protocol — serves multiple
// agent types from a single config.
func ProtocolForCLI(cliType string) string {
	if cliType == "" || cliType == CLIClaude {
		return ProviderProtocolAnthropic
	}
	return ProviderProtocolOpenAI
}

// ExpandProvider turns a provider's natural fields into the environment
// key/value pair for the given agent CLI type. The protocol is derived from the
// agent's cliType (ProtocolForCLI); the base_url is looked up from the
// provider's per-protocol base_urls map. If the provider has no base_url
// configured for the agent's protocol it returns an empty map (the provider
// doesn't serve that agent type). Only non-empty fields are emitted; the API
// key is injected via providerKey; extra_env is merged last so users can
// override a generated key.
//
// preserveRef=false resolves a "${ACCOUNT:<name>}" api_key to the account's
// literal key (used for previews); preserveRef=true emits the reference
// unchanged so a runtime consumer stays runtime-live.
func ExpandProvider(p store.EnvProvider, cliType string, accounts []store.EnvAccount, preserveRef bool) map[string]string {
	protocol := ProtocolForCLI(cliType)
	baseURL := decodeBaseURLs(p.BaseUrls)[protocol]
	if baseURL == "" {
		return map[string]string{}
	}
	if protocol == ProviderProtocolOpenAI {
		return expandOpenAI(p, baseURL, cliType, accounts, preserveRef)
	}
	return expandAnthropic(p, baseURL, accounts, preserveRef)
}

// decodeBaseURLs parses a provider's stored base_urls JSON (Record<protocol,
// base_url>), returning an empty (non-nil) map on any decode error.
func decodeBaseURLs(raw string) map[string]string {
	m := map[string]string{}
	if raw == "" {
		return m
	}
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}

// expandAnthropic produces the Anthropic-compatible env read by Claude Code
// (and any CLI that talks to an /anthropic endpoint). High-confidence mapping.
func expandAnthropic(p store.EnvProvider, baseURL string, accounts []store.EnvAccount, preserveRef bool) map[string]string {
	out := map[string]string{}
	if baseURL != "" {
		out["ANTHROPIC_BASE_URL"] = baseURL
	}
	if key := providerKey(p.ApiKey, accounts, preserveRef); key != "" {
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

// expandOpenAI produces the OpenAI-compatible env read by Codex/Qwen and other
// OpenAI-protocol CLIs. niuniu's Codex path additionally reads the model from
// the NIUNIU_MODEL control key.
func expandOpenAI(p store.EnvProvider, baseURL, cliType string, accounts []store.EnvAccount, preserveRef bool) map[string]string {
	out := map[string]string{}
	if baseURL != "" {
		out["OPENAI_BASE_URL"] = baseURL
	}
	if key := providerKey(p.ApiKey, accounts, preserveRef); key != "" {
		out["OPENAI_API_KEY"] = key
	}
	if p.Model != "" {
		out["OPENAI_MODEL"] = p.Model
	}
	if cliType == CLICodex && p.Model != "" {
		out["NIUNIU_MODEL"] = p.Model
	}
	mergeExtraEnv(out, p.ExtraEnv)
	return out
}

// providerKey returns the API key to inject for the credential env key. When
// preserveRef is true the provider's api_key is emitted unchanged — so a
// "${ACCOUNT:<name>}" reference stays a reference that SubstituteAccounts
// replaces at spawn time (runtime-live). When false, a "${ACCOUNT:<name>}"
// reference is resolved to the account's literal key (used for previews). A
// reference with no matching account (or an empty literal) yields "" so the
// mapping omits the credential key rather than emitting a broken value.
func providerKey(raw string, accounts []store.EnvAccount, preserveRef bool) string {
	if preserveRef || !isAccountRef(raw) {
		return raw
	}
	name := raw[len(AccountRefPrefix) : len(raw)-1]
	for _, a := range accounts {
		if a.Name == name {
			return a.ApiKey
		}
	}
	slog.Warn("expand provider: account ref unresolved", "account", name)
	return ""
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