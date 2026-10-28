// External API Proxy — REST client for provider CRUD.
//
// Backend at server/internal/server/handler_external_proxy.go wraps
// every success response in a `{data: ...}` envelope; this client
// unwraps so callers see the inner payload directly.
//
// The /call endpoint is intentionally not exposed here: it is invoked
// by AI via the MCP tool, not by the SPA.

import { api } from './api';
import type {
  ExternalProviderListItem,
  ExternalProviderDetail,
  CreateProviderBody,
  UpdateProviderBody,
} from '@/types/integration';
import type { ProbeOutcome } from './data-sources-api';

interface ListResp {
  data: { items: ExternalProviderListItem[] };
}

interface DetailResp {
  data: ExternalProviderDetail;
}

interface CreateResp {
  data: ExternalProviderDetail;
}

interface UpdateResp {
  data: ExternalProviderDetail;
}

interface DeleteResp {
  data: { ok: boolean };
}

interface WriteEnabledResp {
  data: { ok: boolean; enabled: boolean };
}

/** Owner scope the write-permission toggle is read/written under. The proxy
 *  gates non-GET calls by the WORKSPACE's owner, so org members manage the
 *  org scope here; defaults to the caller's personal owner when omitted. */
export interface WriteOwnerScope {
  type: 'user' | 'org';
  id: number;
}

const ownerQuery = (owner?: WriteOwnerScope) =>
  owner && owner.id > 0
    ? `?owner_type=${encodeURIComponent(owner.type)}&owner_id=${owner.id}`
    : '';

export const externalProxyApi = {
  listProviders: (owner?: WriteOwnerScope): Promise<ExternalProviderListItem[]> =>
    api
      .get<ListResp>(`/me/external-proxy/providers${ownerQuery(owner)}`)
      .then((r) => r.data.items),

  getProviderSchema: (name: string): Promise<ExternalProviderDetail> =>
    api
      .get<DetailResp>(
        `/me/external-proxy/providers/${encodeURIComponent(name)}/schema`,
      )
      .then((r) => r.data),

  createProvider: (body: CreateProviderBody): Promise<ExternalProviderDetail> =>
    api
      .post<CreateResp>('/me/external-proxy/providers', body)
      .then((r) => r.data),

  updateProvider: (
    id: number,
    body: UpdateProviderBody,
  ): Promise<ExternalProviderDetail> =>
    api
      .put<UpdateResp>(`/me/external-proxy/providers/${id}`, body)
      .then((r) => r.data),

  deleteProvider: (id: number): Promise<void> =>
    api
      .delete<DeleteResp>(`/me/external-proxy/providers/${id}`)
      .then(() => undefined),

  // Per-(owner, provider) write gate. Default off; the proxy rejects every
  // non-GET call against the provider (scoped to the workspace's owner) until
  // this is flipped true.
  setWriteEnabled: (
    id: number,
    enabled: boolean,
    owner?: WriteOwnerScope,
  ): Promise<boolean> =>
    api
      .patch<WriteEnabledResp>(
        `/me/external-proxy/providers/${id}/write-enabled`,
        {
          enabled,
          ...(owner && owner.id > 0
            ? { owner_type: owner.type, owner_id: owner.id }
            : {}),
        },
      )
      .then((r) => r.data.enabled),

  // --- Continuous health + contract drift (#744) -------------------------

  /** Run (or re-run) the provider's verify_operation probe immediately. */
  probeProvider: (id: number): Promise<ProbeOutcome> =>
    api
      .post<{ outcome: ProbeOutcome }>(`/me/integration-health/provider/${id}/probe`)
      .then((r) => r.outcome),

  /** Re-enable a provider after an auto-disable, clearing the failure streak. */
  reenableProvider: (id: number): Promise<void> =>
    api
      .post<{ enabled: boolean }>(`/me/integration-health/provider/${id}/reenable`)
      .then(() => undefined),

  /** Turn a provider off manually. */
  disableProvider: (id: number): Promise<void> =>
    api
      .post<{ enabled: boolean }>(`/me/integration-health/provider/${id}/disable`)
      .then(() => undefined),

  /** Acknowledge upstream contract drift, re-baselining the digest. */
  acknowledgeDrift: (id: number): Promise<void> =>
    api
      .post<{ drift: boolean }>(
        `/me/integration-health/provider/${id}/acknowledge-drift`,
      )
      .then(() => undefined),
};
