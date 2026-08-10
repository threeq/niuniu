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

export const externalProxyApi = {
  listProviders: (): Promise<ExternalProviderListItem[]> =>
    api.get<ListResp>('/me/external-proxy/providers').then((r) => r.data.items),

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

  // Per-(user, provider) write gate. Default off; the proxy rejects every
  // non-GET call against the provider until this is flipped true.
  setWriteEnabled: (id: number, enabled: boolean): Promise<boolean> =>
    api
      .patch<WriteEnabledResp>(
        `/me/external-proxy/providers/${id}/write-enabled`,
        { enabled },
      )
      .then((r) => r.data.enabled),
};
