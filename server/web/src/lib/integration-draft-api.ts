import { api, ApiError } from './api';
import type {
  DraftKind,
  DraftPublishResult,
  DraftRollbackResult,
  IntegrationDraftView,
  IntegrationVersion,
} from '@/types/integration-draft';

// Integration draft pipeline — REST client (Epic #726 / #734).
//
// These endpoints are the HUMAN surface: the SPA may confirm a draft and
// publish it. The agent-facing MCP surface can create and read drafts but is
// refused publish/rollback server-side.

export const integrationDraftApi = {
  list: (): Promise<IntegrationDraftView[]> =>
    api.get<{ data: IntegrationDraftView[] }>('/me/integration-drafts').then(r => r.data),

  get: (id: number): Promise<IntegrationDraftView> =>
    api.get<{ data: IntegrationDraftView }>(`/me/integration-drafts/${id}`).then(r => r.data),

  confirm: (
    id: number,
    body: { decision: 'approve' | 'reject'; acknowledged: string[]; second_confirmation?: boolean },
  ): Promise<IntegrationDraftView> =>
    api
      .post<{ data: IntegrationDraftView }>(`/me/integration-drafts/${id}/confirm`, body)
      .then(r => r.data),

  // The user's own publish. An agent cannot reach this path: the MCP route
  // marks the request as agent-originated and the service refuses it.
  //
  // A publish whose verification probe fails is a 409 carrying the full result
  // (published:false, rolled_back:true, why the probe failed, what to do next).
  // That is a completed operation with a negative outcome, not a transport
  // failure — so it is unwrapped into the normal return shape. Otherwise the
  // caller would lose the very information it needs to fix the cause.
  publish: (id: number): Promise<DraftPublishResult> =>
    api
      .post<{ data: DraftPublishResult }>(`/me/integration-drafts/${id}/publish`, {})
      .then(r => r.data)
      .catch((err: unknown) => {
        const result = publishResultFrom409(err);
        if (result) return result;
        throw err;
      }),

  remove: (id: number): Promise<void> =>
    api.delete(`/me/integration-drafts/${id}`),

  versions: (kind: DraftKind, targetName: string): Promise<IntegrationVersion[]> =>
    api
      .get<{ data: IntegrationVersion[] }>(
        `/me/integration-versions?kind=${encodeURIComponent(kind)}&target_name=${encodeURIComponent(targetName)}`,
      )
      .then(r => r.data),

  // Restoring an older version can re-grant authority, so a scope-changing
  // rollback answers with {requires_confirmation:true, card} first; the caller
  // shows that card and retries with confirmed=true.
  rollback: (
    versionId: number,
    body: { confirmed?: boolean; acknowledged?: string[] } = {},
  ): Promise<DraftRollbackResult> =>
    api
      .post<{ data: DraftRollbackResult }>(`/me/integration-versions/${versionId}/rollback`, body)
      .then(r => r.data),
};

/** Unwrap a 409 publish conflict into its structured result, or null when the
 *  error is something else (a real failure the caller must surface). */
function publishResultFrom409(err: unknown): DraftPublishResult | null {
  if (!(err instanceof ApiError) || err.status !== 409) return null;
  const data = (err.body as { data?: DraftPublishResult } | null)?.data;
  return data && typeof data === 'object' && 'published' in data ? data : null;
}
