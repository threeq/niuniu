// Pure logic for the "知识库 MCP 配置" guided flow (issue B: 用户自配知识库 MCP +
// 行业预设). The dialog forks an industry KB preset scene, points its inline http
// MCP server at the user's own endpoint, and binds an API token into credstore —
// niuniu orchestrates the MCP call; the data stays behind the user's MCP.
//
// Kept dependency-free so the transformation is unit-testable without React.

import type { SceneDefinition, SceneMCPDecl } from '@/types/api';
import type { CreateCredentialBody } from '@/types/integration';

/** Credential provider all KB MCP presets bind against (see the builtin
 *  kb-*.yaml `required_credentials`). */
export const KB_CREDENTIAL_PROVIDER = 'knowledge-base';

/** The tag every KB MCP preset carries, used to surface them as industry presets. */
export const KB_PRESET_TAG = 'knowledge-base';

/** Sanitize free text into a slug fragment: lower-case, ascii-ish, dash-joined.
 *  Non-ascii (e.g. Chinese) is dropped, so an all-Chinese name falls back to ''. */
export function slugify(input: string): string {
  return input
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40);
}

/** Per-scene credential alias. A unique alias per configured KB lets a user
 *  wire several KB presets (电商 + 法律 …) without their tokens colliding on a
 *  single shared alias. */
export function kbCredentialAlias(slug: string): string {
  return `kb-${slug}`;
}

/** Deep-ish clone of a scene definition so we never mutate the fetched preset. */
function cloneDefinition(def: SceneDefinition): SceneDefinition {
  return JSON.parse(JSON.stringify(def)) as SceneDefinition;
}

export interface KBConfigInput {
  /** The forked scene's slug — also seeds the credential alias. */
  slug: string;
  /** The user's own KB MCP endpoint (http/sse url). */
  endpoint: string;
}

/**
 * Rewrite a KB preset definition to point at the user's endpoint and a
 * per-scene credential alias. Idempotent and pure: returns a new definition.
 *
 * - the `kb` MCP server's `config.url` becomes the user's endpoint;
 * - its `Authorization` header becomes `Bearer ${cred:<alias>.token}`;
 * - every required_credentials entry (provider knowledge-base) is repointed to
 *   the same per-scene alias so the projector decrypts the right token.
 */
export function applyKBConfig(def: SceneDefinition, input: KBConfigInput): SceneDefinition {
  const alias = kbCredentialAlias(input.slug);
  const next = cloneDefinition(def);
  const placeholder = `Bearer \${cred:${alias}.token}`;

  next.mcp = (next.mcp ?? []).map((m): SceneMCPDecl => {
    if (m.name !== 'kb') return m;
    const config: Record<string, unknown> = { ...(m.config ?? {}) };
    config.type = config.type ?? 'http';
    config.url = input.endpoint.trim();
    const headers: Record<string, unknown> = {
      ...((config.headers as Record<string, unknown> | undefined) ?? {}),
    };
    headers.Authorization = placeholder;
    config.headers = headers;
    return { ...m, config };
  });

  next.required_credentials = (next.required_credentials ?? []).map((c) =>
    c.provider === KB_CREDENTIAL_PROVIDER ? { ...c, alias } : c,
  );

  return next;
}

/** Build the credstore credential body for the KB API token. */
export function buildKBCredentialBody(slug: string, token: string): CreateCredentialBody {
  return {
    provider: KB_CREDENTIAL_PROVIDER,
    alias: kbCredentialAlias(slug),
    config: { token: token.trim() },
  };
}

export interface KBFormErrors {
  name?: string;
  endpoint?: string;
  token?: string;
}

/** Validate the guided form. Endpoint must be an http(s) URL; name must yield a
 *  non-empty slug; token is required. Returns per-field messages (i18n keys). */
export function validateKBForm(name: string, endpoint: string, token: string): KBFormErrors {
  const errors: KBFormErrors = {};
  if (!slugify(name)) errors.name = 'kb.err_name';
  const ep = endpoint.trim();
  if (!ep) {
    errors.endpoint = 'kb.err_endpoint_required';
  } else if (!/^https?:\/\/.+/i.test(ep)) {
    errors.endpoint = 'kb.err_endpoint_url';
  }
  if (!token.trim()) errors.token = 'kb.err_token';
  return errors;
}
