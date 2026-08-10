// REST wrappers for the data-source settings UI (plan task B5). Backend
// handlers in server/internal/api/data_source.go; routes under /api/me.
//
// The list endpoint returns config REDACTED (host/port retained, password
// stripped) — see DataSource.config below. Create/update send the full config
// which the server AES-GCM encrypts.
import { api } from '@/lib/api'
import type { DataSourceKind } from '@/types/data'

export type AccessMode = 'read' | 'readwrite'
export type RequireConfirm = 'always' | 'writes_only' | 'never'

export type SSHAuthMethod = 'password' | 'private_key'

/**
 * Optional SSH-tunnel config. When `enabled`, the backend routes the DB
 * connection to host:port through an SSH local port forward. Secrets
 * (`password`, `private_key`) are redacted on list/get responses — leave them
 * blank on edit to keep the stored value; `has_private_key` signals a key is
 * already stored. `host_key`, when set, pins the bastion's SHA256 fingerprint.
 */
export interface SSHTunnelConfig {
  enabled?: boolean
  host?: string
  port?: number
  user?: string
  auth_method?: SSHAuthMethod
  password?: string
  private_key?: string
  host_key?: string
  /** Read-only hint on responses: a private key is stored (never the key itself). */
  has_private_key?: boolean
}

/** Connection params. On list responses the password is omitted (redacted). */
export interface DataSourceConfig {
  host?: string
  port?: number
  user?: string
  password?: string
  database?: string
  /**
   * Kind-specific extras forwarded to dataconn.ConnConfig.Options (e.g.
   * trino `schema`, elasticsearch `api_key`, mongo URI query params such as
   * `authSource`, http `scheme`/`base_path`/`headers`). Values are usually
   * strings; http `headers` is a nested object — hence `unknown`. Secrets
   * (`api_key`, `headers`) are redacted on list responses, like the password.
   */
  options?: Record<string, unknown>
  /** Optional SSH tunnel. Omitted/`enabled:false` means a direct connection. */
  ssh?: SSHTunnelConfig
}

/** Data-scope allow/deny lists. Empty arrays mean "no restriction". */
export interface ScopeConfig {
  databases?: string[]
  tables_allow?: string[]
  tables_deny?: string[]
  /**
   * http-only: allow-list of HTTP verbs (e.g. ["GET","POST"]). Empty = no
   * method restriction beyond the read/write gate.
   */
  methods?: string[]
}

/** A visibility binding target type. Project is the only supported target. */
export type BindingTargetType = 'project'

/**
 * A single visibility binding. A source is visible to a workspace's agent only
 * when it is bound to that workspace's project (exactly like external sources).
 * A source with NO bindings is invisible to every agent.
 */
export interface DataSourceBinding {
  target_type: BindingTargetType
  target_id: number
}

export interface DataSource {
  id: number
  name: string
  kind: DataSourceKind
  config: DataSourceConfig
  scope_config: ScopeConfig
  default_access_mode: AccessMode
  require_confirm: RequireConfirm
  last_verified_at?: string | null
  /** Visibility bindings; empty means invisible to every agent. */
  bindings: DataSourceBinding[]
}

export interface CreateDataSourceBody {
  name: string
  kind: DataSourceKind
  config: DataSourceConfig
  scope_config: ScopeConfig
  default_access_mode: AccessMode
  require_confirm: RequireConfirm
  bindings: DataSourceBinding[]
}

export type UpdateDataSourceBody = Partial<CreateDataSourceBody>

export interface VerifyResult {
  ok: boolean
  message?: string
}

export async function listDataSources(): Promise<DataSource[]> {
  // The backend wraps the list in an `items` envelope (see
  // server/internal/api/data_source.go List). apiFetch returns the parsed body
  // directly, so unwrap here — typing it as a bare array left `data.length`
  // undefined and the panel rendered neither the list nor the empty state.
  const r = await api.get<{ items: DataSource[] }>('/me/data-sources')
  return r.items ?? []
}

export async function createDataSource(
  body: CreateDataSourceBody,
): Promise<DataSource> {
  return api.post<DataSource>('/me/data-sources', body)
}

export async function updateDataSource(
  id: number,
  body: UpdateDataSourceBody,
): Promise<DataSource> {
  return api.patch<DataSource>(`/me/data-sources/${id}`, body)
}

export async function removeDataSource(id: number): Promise<void> {
  await api.delete<void>(`/me/data-sources/${id}`)
}

export async function verifyDataSource(id: number): Promise<VerifyResult> {
  return api.post<VerifyResult>(`/me/data-sources/${id}/verify`)
}

// --- Project-scoped data-source association (project settings) ---------------

/** Minimal data-source shape returned by the project association list. */
export interface ProjectDataSource {
  id: number
  name: string
  kind: DataSourceKind
}

export async function listProjectDataSources(
  projectId: number,
): Promise<ProjectDataSource[]> {
  const r = await api.get<{ items: ProjectDataSource[] }>(
    `/projects/${projectId}/data-sources`,
  )
  return r.items ?? []
}

export async function addProjectDataSource(
  projectId: number,
  sourceId: number,
): Promise<void> {
  await api.post<void>(`/projects/${projectId}/data-sources`, {
    source_id: sourceId,
  })
}

export async function removeProjectDataSource(
  projectId: number,
  sourceId: number,
): Promise<void> {
  await api.delete<void>(`/projects/${projectId}/data-sources/${sourceId}`)
}
