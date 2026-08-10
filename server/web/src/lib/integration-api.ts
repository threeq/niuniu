import { api } from './api';
import type {
  CredentialInUseSource,
  ExternalCredential,
  ExternalSource,
  ProviderName,
  CreateCredentialBody,
  PatchCredentialBody,
} from '@/types/integration';

// External issue tracker integration — REST client.
//
// Path note: `api.get` is a thin wrapper over `apiFetch` that prepends `/api`
// to every endpoint, so callers pass paths starting with `/me/...`,
// `/projects/...`, etc.

export const integrationApi = {
  // Credentials — owner-scoped (personal by default; pass an org owner to
  // address that org's shared credentials), addressed by numeric id (alias is
  // the user-facing label, id is the stable handle).
  listCredentials: (
    provider?: ProviderName,
    owner?: { type: 'user' | 'org'; id: number },
  ): Promise<ExternalCredential[]> => {
    const params = new URLSearchParams();
    // Only an org owner narrows the scope on the wire; absent / personal owner
    // keeps the legacy current-user query for backward compatibility.
    if (owner && owner.type === 'org' && owner.id > 0) {
      params.set('owner_type', 'org');
      params.set('owner_id', String(owner.id));
    }
    if (provider) params.set('provider', provider);
    const qs = params.toString();
    return api
      .get<{ items: ExternalCredential[] }>(
        `/me/external-credentials${qs ? `?${qs}` : ''}`,
      )
      .then(r => r.items);
  },

  createCredential: (
    body: CreateCredentialBody,
  ): Promise<{ id: number; provider: ProviderName; alias: string }> => {
    // Only forward a concrete owner (id>0); a personal/zero owner is omitted so
    // the backend defaults to the calling user.
    const { owner, ...rest } = body;
    const payload =
      owner && owner.id > 0 ? { ...rest, owner } : rest;
    return api.post('/me/external-credentials', payload);
  },

  patchCredential: (id: number, body: PatchCredentialBody): Promise<void> =>
    api.patch(`/me/external-credentials/${id}`, body),

  // Bind-time IMAP login probe — resolves on 200 {ok:true}, throws on
  // auth (422) / connect (502) failure so the caller can toast the reason.
  verifyImap: (id: number): Promise<{ ok: boolean }> =>
    api.post(`/me/external-credentials/${id}/verify-imap`, {}),

  credentialUsages: (id: number): Promise<CredentialInUseSource[]> =>
    api
      .get<{ items: CredentialInUseSource[] }>(
        `/me/external-credentials/${id}/usages`,
      )
      .then((r) => r.items),

  deleteCredential: (id: number): Promise<void> =>
    api.delete(`/me/external-credentials/${id}`),

  // Sources — per-project external source bindings. The UI manages CRUD here;
  // browsing / importing upstream issues is AI-driven through MCP, so we no
  // longer expose browseIssues / importIssue.
  listSources: (projectId: number): Promise<ExternalSource[]> =>
    api
      .get<{ items: ExternalSource[] }>(`/projects/${projectId}/external-sources`)
      .then(r => r.items),

  addSource: (
    projectId: number,
    body: {
      provider: ProviderName;
      source_key: string;
      credential_id: number;
      config?: Record<string, unknown>;
    },
  ): Promise<ExternalSource> =>
    api.post<ExternalSource>(`/projects/${projectId}/external-sources`, body),

  deleteSource: (projectId: number, sourceId: number): Promise<void> =>
    api.delete(`/projects/${projectId}/external-sources/${sourceId}`),
};
