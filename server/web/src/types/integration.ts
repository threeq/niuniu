// External issue tracker integration — shared types.
// See docs/superpowers/specs/2026-05-12-external-issue-tracker-integration-design.md
//
// `jira` is a future provider, scaffolded for the L4 MCP intent tools
// (docs/superpowers/plans/2026-05-15-l4-mcp-intent-tools.md). Existing
// credential dialog UI only exposes github/tapd; jira can still appear
// in write-permission prefs and identity mappings.

export type ProviderName = 'github' | 'jira' | 'tapd' | 'imap';

export interface ExternalCredential {
  id: number;
  provider: ProviderName;
  alias: string;
  last_verified_at: string | null;
}

// Compact shape used by the source-binding credential picker.
export type CredentialChoice = Pick<ExternalCredential, 'id' | 'alias' | 'last_verified_at'>;

/** Generic credential create body — `provider` is matched by string
 *  against the providers configured in external_providers (including
 *  user-defined ones), and `config` is the free-form auth payload the
 *  proxy will read at call time (e.g. {token}, {api_user, api_password}). */
export type CreateCredentialBody = {
  alias: string;
  provider: string;
  config: Record<string, unknown>;
  /** Owner the credential belongs to. Omitted / personal (`type:'user'`)
   *  creates a current-user credential; `type:'org'` creates a team-shared
   *  one. Only forwarded to the API when `id > 0`. */
  owner?: { type: 'user' | 'org'; id: number };
};

/** PATCH /me/external-credentials/:id body. Either field may be absent
 *  but at least one must be present. `config` replaces the entire
 *  encrypted payload (the SPA always sends the auth_mode + the freshly
 *  entered secret fields); `alias` renames the credential. */
export type PatchCredentialBody = {
  alias?: string;
  config?: Record<string, unknown>;
};

// Returned by the backend with HTTP 409 when a credential is referenced by one
// or more external sources. Consumers read it via `(err as ApiError).body`.
export interface CredentialInUseError {
  error: string;
  error_kind: 'in_use';
  in_use_by: CredentialInUseSource[];
}

export interface CredentialInUseSource {
  id: number;
  project_id: number;
  project_name: string;
  provider: string;
  source_key: string;
}

export interface ExternalSource {
  id: number;
  project_id: number;
  provider: ProviderName;
  source_key: string;
  credential_id: number | null;
  config: Record<string, unknown>;
  created_at: string;
}

/** v1.1 read-only snapshot of an upstream comment. Stored as JSON on
 *  issues.external_comments_snapshot; rendered above the niuniu timeline
 *  when present. Never persisted into niuniu's own issue_comments table.
 *  Field shapes mirror the Go integration.ExternalComment struct. */
export interface ExternalComment {
  id: string;
  author: string;
  body: string;
  /** as-provided by source: RFC3339 for GitHub, "YYYY-MM-DD HH:MM:SS" for TAPD */
  created_at: string;
  /** direct link to the comment in the external UI */
  url: string;
}

// External API Proxy — provider definitions for the generic
// call_external_api MCP tool. Each row stores the API base URL, auth
// wiring, JSON whitelist, and (optionally) an OpenAPI spec URL or an
// AI-generated profile. Backend at server/internal/server/handler_external_proxy.go.
//
// `name` is the global identifier credentials and AI reference (NOT
// restricted to the hardcoded ProviderName union); the legacy
// github/jira/tapd credentials still match these names but additional
// providers (linear, asana, ...) can now be added without code changes.
export type ProxyAuthType =
  | 'bearer'
  | 'basic'
  | 'custom_header'
  | 'query_param'
  | 'oauth'
  | 'imap';

/** Auth modes a credential can be saved as. A provider's `auth_modes`
 *  list narrows which of these the AddCredentialDialog will offer. The
 *  `imap` mode collects mailbox connection fields (host/port/user/password/
 *  security) for the office-mail scene rather than an HTTP auth header. The
 *  `query_param` mode collects a single api_key the proxy injects into the URL
 *  query string (e.g. SerpAPI) instead of an Authorization header. */
export type ProxyAuthMode =
  | 'bearer'
  | 'basic'
  | 'custom_header'
  | 'query_param'
  | 'imap';

export interface ExternalProviderListItem {
  id: number;
  name: string;
  label: string;
  api_base_url: string;
  /** Per-provider on/off (global to the provider). When false, the proxy
   *  refuses all calls regardless of write_enabled or whitelist. */
  enabled: boolean;
  /** Per-(user, provider) write gate. When false (default), the proxy
   *  rejects every non-GET call with PROXY_ERROR code "write_disabled". */
  write_enabled: boolean;
  auth_type: ProxyAuthType;
  /** Modes this provider accepts; >= 1 entry. Drives the credential
   *  dialog: one mode renders that mode's fields directly; multiple
   *  modes render a mode toggle (e.g. TAPD = ["basic", "bearer"]). */
  auth_modes: ProxyAuthMode[];
  openapi_url?: string;
  /** `'system'` for built-in providers (read-only except for enabled);
   *  `'user'` (or empty) for user-created providers. */
  created_by: string;
}

/** Helper: a provider seeded by the server (created_by === 'system'). */
export function isSystemProvider(p: { created_by?: string }): boolean {
  return p.created_by === 'system';
}

export interface ExternalProviderDetail extends ExternalProviderListItem {
  auth_header: string;
  auth_prefix: string;
  profile: string;
  whitelist: string;
}

export interface CreateProviderBody {
  name: string;
  label: string;
  api_base_url: string;
}

export interface UpdateProviderBody {
  label?: string;
  api_base_url?: string;
  auth_type?: ProxyAuthType;
  auth_header?: string;
  auth_prefix?: string;
  whitelist?: string;
  profile?: string;
  openapi_url?: string;
  enabled?: boolean;
}
