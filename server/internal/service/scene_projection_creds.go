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
	"fmt"
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
// A placeholder resolved into an http/sse server's header is then proxified
// (see proxifyCredentialHeaders): the resolved value is discarded and only a
// loopback pointer token reaches the workspace file, with the real credential
// re-resolved per request.
//
// When extCred is nil (test projectors, or a build without the credential
// service wired) every placeholder misses, so any server using placeholders is
// dropped and its aliases reported missing — a safe degrade, never a leak.
func (p *SceneProjector) resolveProjectionCredentials(
	ctx context.Context, owner OwnerRef, userID, wsID int64, proj *Projection,
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
		// A placeholder in the server's own URL would resolve the secret INTO
		// the url field, where no header injection can replace it. Fail closed:
		// drop the server rather than materialize the secret.
		if rawURL, _ := newCfg["url"].(string); credPlaceholderRe.MatchString(rawURL) {
			dropped[name] = true
			for _, a := range credPlaceholderAliases(rawURL) {
				missing[a] = true
			}
			slog.Warn("scene projector: ${cred:} in an MCP server url is not supported; server dropped", "server", name)
			continue
		}

		// Resolve placeholders in every credential-bearing sub-map. `env` is used
		// by stdio MCP servers; `headers` by http/sse MCP servers (e.g. a
		// knowledge-base MCP whose auth is `Authorization: Bearer ${cred:kb.token}`).
		// A miss in ANY sub-map drops the whole server — never emit a half-filled
		// auth (spec §4.2.4).
		var serverMissing []string
		// headers keys whose RAW value carried a placeholder: after resolution
		// these hold the real secret and must be kept out of the workspace file.
		secretHeaders := map[string]bool{}
		// Aliases feeding those headers, so a server we refuse to proxify can be
		// reported as a missing-credential card instead of vanishing silently.
		var secretAliases []string
		seenAlias := map[string]bool{}
		envHasPlaceholder := false
		for _, sub := range []string{"env", "headers"} {
			raw, ok := newCfg[sub]
			if !ok {
				continue
			}
			m := anyToStringMap(raw)
			if len(m) == 0 {
				continue
			}
			for k, v := range m {
				if !credPlaceholderRe.MatchString(v) {
					continue
				}
				if sub == "headers" {
					secretHeaders[k] = true
				} else {
					envHasPlaceholder = true
				}
				for _, a := range credPlaceholderAliases(v) {
					if !seenAlias[a] {
						seenAlias[a] = true
						secretAliases = append(secretAliases, a)
					}
				}
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
		// A resolved secret header MUST leave the file again through the proxy.
		// If it cannot, drop the server rather than materialize the secret —
		// same fail-closed discipline as an unresolved placeholder above.
		if !p.proxifyCredentialHeaders(wsID, name, newCfg, secretHeaders) {
			dropped[name] = true
			for _, a := range secretAliases {
				missing[a] = true
			}
			continue
		}
		if envHasPlaceholder {
			slog.Warn("scene projector: stdio MCP server resolves a credential into env; "+
				"it stays in the workspace config (documented residual risk)", "server", name)
		}
		resolved[name] = newCfg
	}
	return resolved, dropped, missing
}

// ResolveProxyTarget re-resolves, at request time, the upstream URL and the
// credential headers for one projected MCP server — the lookup behind
// CredProxyService. It reads the CURRENT projection and the CURRENT credential
// binding, so a changed/removed scene or an unbound credential takes effect on
// the next request without any revocation step.
//
// Only the headers that carried a ${cred:...} placeholder are returned; the
// upstream is the scene-authored url, never agent input.
func (p *SceneProjector) ResolveProxyTarget(ctx context.Context, wsID int64, serverName string) (string, map[string]string, error) {
	ws, err := p.q.GetWorkspace(ctx, wsID)
	if err != nil {
		return "", nil, fmt.Errorf("load workspace: %w", err)
	}
	proj, err := p.Recompute(ctx, wsID)
	if err != nil {
		return "", nil, fmt.Errorf("recompute projection: %w", err)
	}
	cfg, ok := proj.MCPConfigs[serverName]
	if !ok {
		return "", nil, fmt.Errorf("MCP server %q is not in this workspace's projection", serverName)
	}
	upstream, _ := cfg["url"].(string)
	if upstream == "" || credPlaceholderRe.MatchString(upstream) {
		return "", nil, fmt.Errorf("MCP server %q has no safe upstream url", serverName)
	}
	headers := anyToStringMap(cfg["headers"])
	owner := OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}
	userID := int64(0)
	if ws.CreatedBy.Valid {
		userID = ws.CreatedBy.Int64
	}
	aliasProvider := map[string]string{}
	for _, rc := range proj.RequiredCredentials {
		aliasProvider[rc.Alias] = rc.Provider
	}
	lookup := p.credLookup(ctx, owner, userID, aliasProvider)

	out := map[string]string{}
	for k, v := range headers {
		if !credPlaceholderRe.MatchString(v) {
			continue
		}
		resolved, miss := resolveCredEnv(map[string]string{k: v}, lookup)
		if len(miss) > 0 {
			return "", nil, fmt.Errorf("credential %q is not bound", strings.Join(miss, ", "))
		}
		out[k] = resolved[k]
	}
	if len(out) == 0 {
		return "", nil, fmt.Errorf("MCP server %q has no credential header", serverName)
	}
	return upstream, out, nil
}

// credPlaceholderAliases lists the credential aliases referenced by every
// ${cred:alias.field} placeholder in s (deduped, first-seen order).
func credPlaceholderAliases(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range credPlaceholderRe.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// proxifyCredentialHeaders replaces an http/sse server's placeholder-derived
// headers with a loopback credential-proxy URL, so the resolved secret lives
// only in this process's memory and never in the workspace .mcp.json the agent
// can read. The named headers are removed from the config; the proxy injects
// them on egress.
//
// Only servers with a `url` are eligible — a stdio server (a `command`) takes
// its credential through the child process's environment, which has no egress
// point to intercept. For a stdio server a resolved SECRET HEADER cannot be
// proxied, so the server is refused (returns false) rather than written out.
// A secret in `env` is not tracked here; it is reported as residual risk.
//
// Returns false when a resolved secret would have to stay in the config.
func (p *SceneProjector) proxifyCredentialHeaders(wsID int64, name string, cfg map[string]any, secretHeaders map[string]bool) bool {
	if len(secretHeaders) == 0 {
		return true
	}
	if _, isStdio := cfg["command"].(string); isStdio {
		slog.Warn("scene projector: ${cred:} header on a stdio MCP server cannot be proxied; server dropped", "server", name)
		return false
	}
	upstream, _ := cfg["url"].(string)
	if upstream == "" {
		slog.Warn("scene projector: ${cred:} header on an MCP server with no url cannot be proxied; server dropped", "server", name)
		return false
	}
	if p.credProxy == nil {
		slog.Warn("scene projector: credential proxy not wired; refusing to write a resolved secret to the workspace config", "server", name)
		return false
	}
	hdrs := anyToStringMap(cfg["headers"])
	present := false
	for k := range secretHeaders {
		if _, ok := hdrs[k]; ok {
			present = true
			break
		}
	}
	if !present {
		return true // the placeholder-bearing keys were already absent
	}
	// The minted URL names only the workspace + server; the credential itself is
	// resolved per request (see CredProxyService.Resolve), so nothing secret
	// goes into the token OR the file.
	// The token is pinned to the upstream being replaced, so it cannot later be
	// re-pointed at a different upstream by editing the scene config.
	proxyURL, err := p.credProxy.Issue(wsID, name, upstream)
	if err != nil {
		slog.Warn("scene projector: credential proxy issue failed", "server", name, "err", err)
		return false
	}
	for k := range secretHeaders {
		delete(hdrs, k)
	}
	if len(hdrs) == 0 {
		delete(cfg, "headers")
	} else {
		keep := make(map[string]any, len(hdrs))
		for k, v := range hdrs {
			keep[k] = v
		}
		cfg["headers"] = keep
	}
	cfg["url"] = proxyURL
	return true
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
