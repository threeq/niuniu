// Package service: credential placeholder injection for scene projection.
//
// Scene-authored MCP `config.env` / `config.headers` values may carry
// ${cred:<alias>.<field>} placeholders (env for stdio servers, headers for
// http/sse servers). At projection time the projector resolves them against the
// (owner, workspace-creator) decrypted credentials so the rendered .mcp.json /
// config.toml carries the real secret — without the secret ever living in the
// scene definition or the persisted projection cache.
//
// This is a GENERAL mechanism (not imap-specific): any future MCP server whose
// scene declares required_credentials can use the same placeholder syntax. The
// only provider-aware part is mapCredFieldValue, which adapts enum-shaped
// credential fields (e.g. an imap `security` enum) to the env value the MCP
// server actually expects.
package service

import (
	"context"
	"database/sql"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/integration"
)

// SetExternalCredentialService back-fills the credential service used to
// resolve ${cred:...} placeholders. Wired by server.New after the keyring-
// dependent ExternalCredentialService is constructed (which happens later than
// the projector). Safe to leave unset — placeholders then degrade to missing.
func (p *SceneProjector) SetExternalCredentialService(s *ExternalCredentialService) {
	p.extCred = s
}

// credPlaceholderRe matches ${cred:<alias>.<field>}. Alias excludes '.' and
// '}' (it is the first segment); field excludes '}' only. Both are user-set so
// the class stays permissive — validation of the names happens elsewhere.
var credPlaceholderRe = regexp.MustCompile(`\$\{cred:([^.}]+)\.([^}]+)\}`)

// resolveCredEnv replaces every ${cred:alias.field} placeholder in an env map
// with its (field-mapped) decrypted value. It is a pure function: all secret
// access is funneled through the lookup closure, which returns (value, found).
//
// Returns (resolvedEnv, missingAliases). A placeholder whose lookup misses
// records its alias in missingAliases (deduped, first-seen order) and is left
// as an empty string in the output — but the caller is expected to discard the
// whole server when missingAliases is non-empty (spec §4.2.4: never write a
// half-filled env). Non-placeholder text is preserved verbatim; a value may mix
// literal text with one or more placeholders.
func resolveCredEnv(env map[string]string, lookup func(alias, field string) (string, bool)) (map[string]string, []string) {
	out := make(map[string]string, len(env))
	var missing []string
	missSeen := map[string]bool{}
	for k, v := range env {
		idx := credPlaceholderRe.FindAllStringSubmatchIndex(v, -1)
		if idx == nil {
			out[k] = v
			continue
		}
		var sb strings.Builder
		last := 0
		for _, m := range idx {
			alias := v[m[2]:m[3]]
			field := v[m[4]:m[5]]
			sb.WriteString(v[last:m[0]])
			if val, ok := lookup(alias, field); ok {
				sb.WriteString(mapCredFieldValue(field, val))
			} else if !missSeen[alias] {
				missSeen[alias] = true
				missing = append(missing, alias)
			}
			last = m[1]
		}
		sb.WriteString(v[last:])
		out[k] = sb.String()
	}
	return out, missing
}

// mapCredFieldValue adapts a raw credential field value to the env value an MCP
// server expects. Today only the imap `security` enum needs translation
// (security → IMAP_SECURE truthiness, per the design doc §7 truth table):
//
//	ssl / tls / implicit → "true"   (implicit TLS, port 993)
//	starttls / none      → "false"  (IMAP_SECURE must be a real boolean; the
//	                                 server would treat "starttls" as falsy)
//
// Any other field passes through unchanged.
func mapCredFieldValue(field, value string) string {
	if strings.EqualFold(field, "security") {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "ssl", "tls", "implicit", "true":
			return "true"
		case "starttls", "none", "false", "":
			return "false"
		}
	}
	return value
}

// resolveProjectionCredentials deep-copies proj.MCPConfigs and resolves the
// ${cred:...} placeholders in each server's "env" and "headers" sub-maps
// (env → stdio servers, headers → http/sse servers), scoped to
// (owner, userID). A server with any unresolved placeholder is omitted from the
// returned config map and its name listed in dropped, so the caller can also
// strip it from the MCP names handed to Generate. The union of missing aliases
// (across all servers) is returned for missing-credential reporting.
//
// When extCred is nil (test projectors, or a build without the credential
// service wired) every placeholder misses, so any server using placeholders is
// dropped and its aliases reported missing — a safe degrade, never a leak.
func (p *SceneProjector) resolveProjectionCredentials(
	ctx context.Context, owner OwnerRef, userID int64, proj *Projection,
) (resolved map[string]map[string]any, dropped map[string]bool, missing map[string]bool) {
	dropped = map[string]bool{}
	missing = map[string]bool{}
	if len(proj.MCPConfigs) == 0 {
		return nil, dropped, missing
	}
	aliasProvider := map[string]string{}
	for _, rc := range proj.RequiredCredentials {
		aliasProvider[rc.Alias] = rc.Provider
	}
	lookup := p.credLookup(ctx, owner, userID, aliasProvider)

	resolved = make(map[string]map[string]any, len(proj.MCPConfigs))
	for name, cfg := range proj.MCPConfigs {
		newCfg := deepCopyAnyMap(cfg)
		// Resolve placeholders in every credential-bearing sub-map. `env` is used
		// by stdio MCP servers; `headers` by http/sse MCP servers (e.g. a
		// knowledge-base MCP whose auth is `Authorization: Bearer ${cred:kb.token}`).
		// A miss in ANY sub-map drops the whole server — never emit a half-filled
		// auth (spec §4.2.4).
		var serverMissing []string
		for _, sub := range []string{"env", "headers"} {
			raw, ok := newCfg[sub]
			if !ok {
				continue
			}
			m := anyToStringMap(raw)
			if len(m) == 0 {
				continue
			}
			resolvedMap, miss := resolveCredEnv(m, lookup)
			if len(miss) > 0 {
				serverMissing = append(serverMissing, miss...)
				continue
			}
			newSub := make(map[string]any, len(resolvedMap))
			for k, v := range resolvedMap {
				newSub[k] = v
			}
			newCfg[sub] = newSub
		}
		if len(serverMissing) > 0 {
			dropped[name] = true
			for _, a := range serverMissing {
				missing[a] = true
			}
			continue
		}
		resolved[name] = newCfg
	}
	return resolved, dropped, missing
}

// SECURITY (spec 2026-06-27 规约3): the materialization channel MUST resolve
// credentials only via GetDecryptedConfigByAlias — the (owner, user_id, provider,
// alias) per-user lookup — and MUST NOT call GetBoundByID / any one-to-many
// binding resolution. Materialized secrets land as plaintext in the member's
// workspace; per-user scoping is what stops one member's secret reaching another.
//
// credLookup returns a memoizing closure that decrypts a credential field by
// (alias, field), routed through the alias→provider map declared by the scene.
// Decryption is per-alias and cached (incl. negative results) so a server that
// references several fields of the same alias decrypts once. NEVER logs values.
func (p *SceneProjector) credLookup(
	ctx context.Context, owner OwnerRef, userID int64, aliasProvider map[string]string,
) func(alias, field string) (string, bool) {
	type entry struct {
		cfg map[string]any
		ok  bool
	}
	cache := map[string]entry{}
	return func(alias, field string) (string, bool) {
		e, seen := cache[alias]
		if !seen {
			provider := aliasProvider[alias]
			if provider == "" || p.extCred == nil {
				cache[alias] = entry{ok: false}
				return "", false
			}
			cfg, err := p.extCred.GetDecryptedConfigByAlias(ctx, owner, userID, integration.ProviderName(provider), alias)
			if err != nil {
				// Log alias/field only — never the value or error-wrapped secret.
				slog.Info("scene projector: credential placeholder unresolved", "alias", alias, "field", field)
				cache[alias] = entry{ok: false}
				return "", false
			}
			e = entry{cfg: cfg, ok: true}
			cache[alias] = e
		}
		if !e.ok {
			return "", false
		}
		raw, ok := e.cfg[field]
		if !ok {
			return "", false
		}
		return stringifyCredValue(raw), true
	}
}

// stringifyCredValue renders a decrypted JSON field as the string an env var
// needs. Numbers decoded from JSON arrive as float64 (e.g. an imap_port of
// 993); render them as plain integers, not "993.000000".
func stringifyCredValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		return ""
	}
}

// deepCopyAnyMap clones a map[string]any one level deep enough that mutating a
// nested "env" sub-map on the copy never touches the persisted projection. The
// env sub-map (the only thing we rewrite) is fully cloned; other values are
// shared (we never mutate them).
func deepCopyAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if sub, ok := v.(map[string]any); ok {
			clone := make(map[string]any, len(sub))
			for sk, sv := range sub {
				clone[sk] = sv
			}
			out[k] = clone
			continue
		}
		out[k] = v
	}
	return out
}

// filterOutNames returns names with any entry present in drop removed, order
// preserved. Used to strip dropped (unresolvable-credential) MCP servers from
// the names handed to Generate so they aren't reported as registry-unavailable.
func filterOutNames(names []string, drop map[string]bool) []string {
	if len(drop) == 0 {
		return names
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if drop[n] {
			continue
		}
		out = append(out, n)
	}
	return out
}

// mergeInjectionMissing adds RequiredCredential entries whose alias failed
// injection (missing[alias]) but which findMissingCredentials did not already
// flag (the credential row exists for the provider, but a referenced field was
// absent). Keeps the MissingCredentials list a single source of truth for the
// "bind credential" card.
func mergeInjectionMissing(found []MissingCredential, missing map[string]bool, required []RequiredCredential) []MissingCredential {
	if len(missing) == 0 {
		return found
	}
	already := map[string]bool{}
	for _, m := range found {
		already[m.Alias] = true
	}
	for _, rc := range required {
		if missing[rc.Alias] && !already[rc.Alias] {
			found = append(found, MissingCredential(rc))
			already[rc.Alias] = true
		}
	}
	return found
}

// nullInt64Value unwraps a sql.NullInt64 to its int64 (0 when invalid).
func nullInt64Value(v sql.NullInt64) int64 {
	if v.Valid {
		return v.Int64
	}
	return 0
}
