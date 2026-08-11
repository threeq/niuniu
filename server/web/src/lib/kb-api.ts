// REST wrappers for the knowledge-base (KB) management UI (Epic #496 · task
// #498). This file is the producer/consumer CONTRACT for the sibling backend
// task #497 (KB entity + storage + FTS) and #500 (network presets + async
// download). It mirrors the conventions of `lib/data-sources-api.ts`:
//
//   - Owner-level CRUD lives under `/api/me/knowledge-bases` (the calling user
//     or, server-side, their active org owner).
//   - Project-scoped binding lives under `/api/projects/:id/knowledge-bases`.
//   - List endpoints return an `{ items: [...] }` envelope which we unwrap.
//
// A KB is independent of project-learning `memories`: it is a durable corpus
// (local directory / uploaded files / a network dataset) that agents can
// keyword-search (kb_search, #499) and direct-read (#501). Metadata lives in
// the dual-driver main DB; the FTS index is a SQLite sidecar (`kb_index.db`,
// #497) — none of that leaks into this client, which only speaks REST.
import { api, apiFetch, ApiError } from '@/lib/api'
import { getAccessToken } from '@/stores/auth-store'

/**
 * Where a KB's content comes from:
 *  - `local`  : an absolute directory/file path already on the host.
 *  - `upload` : files uploaded through the browser (multipart).
 *  - `url`    : a network address downloaded asynchronously by the backend
 *               (#500). http(s) direct links + zip/tar.gz archives; optional
 *               mirror fallbacks for in-China reachability.
 */
export type KBSourceKind = 'local' | 'upload' | 'url'

/** Enabled KBs are visible to bound agents; disabled ones are kept but inert. */
export type KBStatus = 'enabled' | 'disabled'

/**
 * Ingestion lifecycle for a KB. `local` sources jump straight to `indexing`
 * then `ready`; `url`/`upload` sources walk the full path. The UI polls while
 * the status is transient (see {@link isKBBusy}) and renders a progress bar.
 */
export type KBIngestStatus =
  | 'pending' // queued, not started
  | 'downloading' // fetching remote bytes (url only)
  | 'extracting' // unpacking an archive (url/upload)
  | 'indexing' // chunking + writing the FTS index
  | 'ready' // searchable
  | 'failed' // see `ingest_error`; retryable

/** Transient states the UI polls on; everything else is terminal. */
const BUSY_STATUSES: ReadonlySet<KBIngestStatus> = new Set([
  'pending',
  'downloading',
  'extracting',
  'indexing',
])

export function isKBBusy(kb: Pick<KnowledgeBase, 'ingest_status'>): boolean {
  return BUSY_STATUSES.has(kb.ingest_status)
}

/** A visibility binding target type. Project is the only supported target. */
export type BindingTargetType = 'project'

/**
 * A single visibility binding. A KB is visible to a workspace's agent only when
 * it is bound to that workspace's project (exactly like data sources). A KB
 * with NO bindings is invisible to every agent.
 */
export interface KBBinding {
  target_type: BindingTargetType
  target_id: number
}

export interface KnowledgeBase {
  id: number
  name: string
  description: string
  source_kind: KBSourceKind
  /** Local path or network URL. Never carries a secret, so it is not redacted. */
  source_location: string
  status: KBStatus
  ingest_status: KBIngestStatus
  /** 0–100. Best-effort; some stages cannot report a precise fraction. */
  ingest_progress: number
  /** Human-readable failure reason when `ingest_status === 'failed'`. */
  ingest_error?: string | null
  doc_count: number
  chunk_count: number
  last_indexed_at?: string | null
  /** Visibility bindings; empty means invisible to every agent. */
  bindings: KBBinding[]
  created_at: string
}

export interface CreateKnowledgeBaseBody {
  name: string
  description?: string
  source_kind: KBSourceKind
  /**
   * For `local`: an absolute directory/file path. For `url`: the resolved
   * primary URL (after a preset fill, possibly user-edited). Omitted for
   * `upload` (files are sent separately via {@link uploadKnowledgeBaseFiles}).
   */
  source_location?: string
  /**
   * `url` only: ordered mirror fallbacks tried after `source_location` fails
   * (a preset may supply GitHub primary + jsdelivr/gitee mirrors). #500.
   */
  mirror_urls?: string[]
  /** `url` only: the chosen preset id, retained for attribution/metadata. */
  preset_id?: string
  bindings: KBBinding[]
}

/** PATCH body. Only the provided fields change. */
export type UpdateKnowledgeBaseBody = Partial<{
  name: string
  description: string
  status: KBStatus
  bindings: KBBinding[]
}>

/** A document (one source file) inside a KB. */
export interface KBDocument {
  id: number
  kb_id: number
  /** Path relative to the KB's dataset directory. */
  path: string
  title: string
  /** Size in bytes. */
  size: number
  chunk_count: number
}

/** A single FTS hit: a matched chunk plus a pointer back to its document. */
export interface KBSearchHit {
  document_id: number
  document_path: string
  chunk_index: number
  /** A short excerpt around the match (may contain `<mark>`-free plain text). */
  snippet: string
  /** BM25 / ts_rank score; higher is more relevant. */
  score: number
}

/**
 * A built-in network preset (#500). Purely a shortcut: selecting one fills the
 * URL field, which the user may still edit. `urls` is the primary source
 * followed by mirror fallbacks, in try order.
 */
export interface KBPreset {
  id: string
  name: string
  description: string
  urls: string[]
  license?: string
  homepage?: string
}

// --- Owner-level KB CRUD ----------------------------------------------------

export async function listKnowledgeBases(): Promise<KnowledgeBase[]> {
  const r = await api.get<{ items: KnowledgeBase[] }>('/me/knowledge-bases')
  return r.items ?? []
}

export async function getKnowledgeBase(id: number): Promise<KnowledgeBase> {
  return api.get<KnowledgeBase>(`/me/knowledge-bases/${id}`)
}

export async function createKnowledgeBase(
  body: CreateKnowledgeBaseBody,
): Promise<KnowledgeBase> {
  return api.post<KnowledgeBase>('/me/knowledge-bases', body)
}

export async function updateKnowledgeBase(
  id: number,
  body: UpdateKnowledgeBaseBody,
): Promise<KnowledgeBase> {
  return api.patch<KnowledgeBase>(`/me/knowledge-bases/${id}`, body)
}

export async function removeKnowledgeBase(id: number): Promise<void> {
  await api.delete<void>(`/me/knowledge-bases/${id}`)
}

/**
 * Retry a failed ingest, optionally forcing a specific mirror URL (the "换镜像"
 * affordance). The backend re-queues the async download/index task.
 */
export async function retryKnowledgeBaseIngest(
  id: number,
  opts?: { mirror_url?: string },
): Promise<KnowledgeBase> {
  return api.post<KnowledgeBase>(
    `/me/knowledge-bases/${id}/retry`,
    opts?.mirror_url ? { mirror_url: opts.mirror_url } : {},
  )
}

/**
 * Uploads files into an `upload`-kind KB and triggers ingestion. Modeled on
 * `api.uploadAttachment`: multipart/form-data with one or more `files` fields;
 * the auth bearer is attached manually since the JSON `apiFetch` default
 * Content-Type would corrupt the multipart boundary.
 */
export async function uploadKnowledgeBaseFiles(
  id: number,
  files: File[],
): Promise<KnowledgeBase> {
  const formData = new FormData()
  for (const f of files) formData.append('files', f)
  const headers: Record<string, string> = {}
  const token = getAccessToken()
  if (token) headers['Authorization'] = `Bearer ${token}`
  const response = await fetch(`/api/me/knowledge-bases/${id}/files`, {
    method: 'POST',
    headers,
    body: formData,
  })
  if (!response.ok) {
    const err = await response
      .json()
      .catch(() => ({ message: `HTTP ${response.status}` }))
    throw new ApiError(
      response.status,
      err?.error?.message || err?.message || 'Upload failed',
      err,
    )
  }
  return response.json()
}

// --- Browse + search --------------------------------------------------------

export async function listKBDocuments(id: number): Promise<KBDocument[]> {
  const r = await api.get<{ items: KBDocument[] }>(
    `/me/knowledge-bases/${id}/documents`,
  )
  return r.items ?? []
}

/** Keyword (FTS) search within one KB. Honest: keyword match, not semantic. */
export async function searchKnowledgeBase(
  id: number,
  query: string,
  limit = 50,
): Promise<KBSearchHit[]> {
  const r = await api.get<{ hits: KBSearchHit[] }>(
    `/me/knowledge-bases/${id}/search`,
    { params: { q: query, limit } },
  )
  return r.hits ?? []
}

/** Built-in network presets (#500). Empty list is a valid, expected response. */
export async function listKBPresets(): Promise<KBPreset[]> {
  const r = await api.get<{ items: KBPreset[] }>('/me/kb-presets')
  return r.items ?? []
}

// --- Project-scoped binding -------------------------------------------------

/** Minimal KB shape returned by the project association list. */
export interface ProjectKnowledgeBase {
  id: number
  name: string
  source_kind: KBSourceKind
  status: KBStatus
}

export async function listProjectKnowledgeBases(
  projectId: number,
): Promise<ProjectKnowledgeBase[]> {
  const r = await api.get<{ items: ProjectKnowledgeBase[] }>(
    `/projects/${projectId}/knowledge-bases`,
  )
  return r.items ?? []
}

export async function addProjectKnowledgeBase(
  projectId: number,
  kbId: number,
): Promise<void> {
  await api.post<void>(`/projects/${projectId}/knowledge-bases`, { kb_id: kbId })
}

export async function removeProjectKnowledgeBase(
  projectId: number,
  kbId: number,
): Promise<void> {
  await apiFetch<void>(`/projects/${projectId}/knowledge-bases/${kbId}`, {
    method: 'DELETE',
  })
}

// --- Workspace mounting (KB as a first-class citizen) -----------------------
// A workspace mounts a KB explicitly (workspace_kbs); the backend materializes
// its content read-only into <workspace>/datasets/<name>/ and auto-ingests it,
// so the workspace file tree and agent can use it directly — mirroring how a
// repository is checked out as a worktree.

export interface WorkspaceKBMount {
  kb_id: number
  name: string
  description: string
  source_kind: KBSourceKind
  /** Read-only materialized dir inside the workspace tree. */
  dataset_path: string
  mounted_at?: string
}

export async function listWorkspaceKnowledgeBases(
  workspaceId: string | number,
): Promise<WorkspaceKBMount[]> {
  const r = await api.get<{ items: WorkspaceKBMount[] }>(
    `/workspaces/${workspaceId}/kbs`,
  )
  return r.items ?? []
}

export async function mountWorkspaceKnowledgeBase(
  workspaceId: string | number,
  kbId: number,
): Promise<WorkspaceKBMount> {
  return api.post<WorkspaceKBMount>(`/workspaces/${workspaceId}/kbs`, {
    kb_id: kbId,
  })
}

export async function syncWorkspaceKnowledgeBase(
  workspaceId: string | number,
  kbId: number,
): Promise<void> {
  await api.post<void>(`/workspaces/${workspaceId}/kbs/${kbId}/sync`)
}

export async function unmountWorkspaceKnowledgeBase(
  workspaceId: string | number,
  kbId: number,
): Promise<void> {
  await api.delete<void>(`/workspaces/${workspaceId}/kbs/${kbId}`)
}
