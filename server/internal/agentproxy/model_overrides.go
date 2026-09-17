package agentproxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Claude Code (print/SDK mode) writes a stderr diagnostic for every model ID
// that is not in its built-in model catalog:
//
//	[claude-code:unrecognized_model] {"model":"deepseek-v4-flash[1m]","query_source":"sdk"}
//
// Third-party provider setups (ANTHROPIC_BASE_URL + ANTHROPIC_MODEL carrying
// e.g. a DeepSeek/GLM name, optionally with the "[1m]" long-window marker)
// hit this on every spawn. The request itself still goes out and works — the
// line is pure diagnostics — but niuniu keeps an 8KB stderr ring per process
// and only surfaces it when the process exits, so users saw a scary
// "Agent process exited (code -1): [claude-code:unrecognized_model] ..."
// right after a task finished (idle reaper) or a manual stop.
//
// The CLI's own silencing path is the settings.json `modelOverrides` map:
// when an entry's VALUE matches the requested model name, the CLI resolves it
// to the entry's KEY (a catalog model identity) and the diagnostic is never
// produced. The actually-sent model name is still the one from --model / env,
// so the mapping only affects recognition, not traffic.
//
// EnsureClaudeModelOverrides merges an entry per model name this spawn will
// actually use (--model flag + ANTHROPIC_*_MODEL env values) into
// <workDir>/.claude/settings.json, preserving every user-written key. It is
// idempotent (no rewrite when already current) and fails open: callers log and
// spawn proceeds.

// anthropicModelEnvKeys are the env vars whose VALUE is a model name Claude
// will put on the wire for some request slot (main turn, haiku-class helper
// queries, subagents). Each distinct value needs its own override entry.
var anthropicModelEnvKeys = []string{
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL",
}

// overrideIdentityKeys are catalog model IDs verified present in Claude Code
// 2.1.261's model catalog, used as the recognition KEY. Any catalog ID works:
// the key only decides the identity the CLI maps the unknown name back to —
// it never changes which model name goes on the wire.
var overrideIdentityKeys = []string{
	"claude-sonnet-4-5",
	"claude-haiku-4-5",
	"claude-opus-4-6",
}

// EnsureClaudeModelOverrides merges modelOverrides entries for the given model
// names into <workDir>/.claude/settings.json. model is the --model flag value
// ("" when unset); env is the full KEY=VALUE slice handed to the process.
// User-written settings keys and existing modelOverrides entries are preserved;
// a model already covered (as any entry's value) is left untouched. An
// unparseable settings.json is an error (callers log + continue) — never
// clobber a file we can't read.
func EnsureClaudeModelOverrides(workDir, model string, env []string) error {
	models := collectClaudeModelNames(model, env)
	if len(models) == 0 {
		return nil
	}

	settingsPath := filepath.Join(workDir, ".claude", "settings.json")
	root := map[string]any{}
	if existing, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(existing, &root); err != nil {
			return fmt.Errorf("parse existing settings.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read settings.json: %w", err)
	}
	if root == nil {
		root = map[string]any{}
	}

	overrides, err := decodeOverrides(root["modelOverrides"])
	if err != nil {
		return fmt.Errorf("existing modelOverrides: %w", err)
	}

	changed := false
	for _, m := range models {
		if hasOverrideValue(overrides, m) {
			continue // some entry (ours or the user's) already covers this name
		}
		key, ok := pickIdentityKey(overrides)
		if !ok {
			// All identity keys taken by other values — skip rather than
			// overwrite a user's mapping. 3+ distinct third-party models in one
			// workspace is the exotic case; those still work, just unmapped.
			break
		}
		overrides[key] = m
		changed = true
	}
	if !changed {
		return nil
	}
	root["modelOverrides"] = overrides

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("create .claude dir: %w", err)
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings.json: %w", err)
	}
	return os.WriteFile(settingsPath, data, 0o644)
}

// collectClaudeModelNames gathers the distinct model names this spawn will put
// on the wire: the --model flag value plus every ANTHROPIC_*_MODEL env value.
// Names starting with "claude-" are dropped — those are catalog models the CLI
// already recognizes, and mapping them would be a pointless settings rewrite.
func collectClaudeModelNames(model string, env []string) []string {
	seen := map[string]struct{}{}
	var out []string
	push := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || strings.HasPrefix(strings.ToLower(v), "claude-") {
			return
		}
		if _, dup := seen[v]; dup {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	push(model)
	for _, entry := range env {
		key, val, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		for _, want := range anthropicModelEnvKeys {
			if key == want {
				push(val)
			}
		}
	}
	return out
}

// decodeOverrides normalizes the settings.json "modelOverrides" value (absent,
// or a JSON object of string→string) into a plain map, rejecting non-string
// values instead of silently dropping them.
func decodeOverrides(v any) (map[string]string, error) {
	out := map[string]string{}
	if v == nil {
		return out, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object, got %T", v)
	}
	for k, val := range obj {
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("entry %q: expected string, got %T", k, val)
		}
		out[k] = s
	}
	return out, nil
}

// hasOverrideValue reports whether any entry's VALUE already covers m. The
// lookup direction matters: overrides map identity-key → model name, so a
// plain `overrides[m]` would check the wrong side and re-add the same model
// under a second identity on every spawn.
func hasOverrideValue(overrides map[string]string, m string) bool {
	for _, v := range overrides {
		if v == m {
			return true
		}
	}
	return false
}

// pickIdentityKey returns the first overrideIdentityKeys member not yet used
// as a key in overrides, so managed entries never collide with each other or
// with a user's own mapping under the same identity.
func pickIdentityKey(overrides map[string]string) (string, bool) {
	for _, key := range overrideIdentityKeys {
		if _, taken := overrides[key]; !taken {
			return key, true
		}
	}
	return "", false
}
