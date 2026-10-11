import { api } from './api';
import type {
  FenceAvailability,
  FenceReport,
  FunctionCallRecord,
  FunctionInvokeResult,
  FunctionRollbackResult,
  FunctionVersion,
  OperationFunction,
} from '@/types/operation-function';

// Sandbox operation functions — REST client (Epic #726 / #739).
//
// The SPA surface can list, run, enable/disable, roll back and delete, and can
// read the fence's self-check. It deliberately has NO publish call: publishing
// a function happens through the provider draft pipeline, where the
// confirmation card renders the code, the declared reach and the behaviour
// rehearsal for a human to approve.

export const operationFunctionsApi = {
  list: (): Promise<OperationFunction[]> =>
    api.get<{ data: OperationFunction[] }>('/me/operation-functions').then(r => r.data ?? []),

  audit: (limit = 100): Promise<FunctionCallRecord[]> =>
    api
      .get<{ data: FunctionCallRecord[] }>(`/me/operation-functions/audit?limit=${limit}`)
      .then(r => r.data ?? []),

  sandbox: (refresh = false): Promise<FenceAvailability> =>
    api
      .get<{ data: FenceAvailability }>(
        `/me/operation-functions/sandbox${refresh ? '?refresh=true' : ''}`,
      )
      .then(r => r.data),

  sandboxSelfTest: (): Promise<FenceReport> =>
    api
      .post<{ data: FenceReport }>('/me/operation-functions/sandbox/selftest', {})
      .then(r => r.data),

  setEnabled: (id: number, enabled: boolean): Promise<void> =>
    api
      .put<{ data: unknown }>(`/me/operation-functions/${id}/enabled`, { enabled })
      .then(() => undefined),

  remove: (id: number): Promise<void> =>
    api.delete<{ data: unknown }>(`/me/operation-functions/${id}`).then(() => undefined),

  invoke: (
    id: number,
    body: { input?: unknown; idempotency_key?: string },
  ): Promise<FunctionInvokeResult> =>
    api
      .post<{ data: FunctionInvokeResult }>(`/me/operation-functions/${id}/invoke`, body)
      .then(r => r.data),

  versions: (id: number): Promise<FunctionVersion[]> =>
    api
      .get<{ data: FunctionVersion[] }>(`/me/operation-functions/${id}/versions`)
      .then(r => r.data ?? []),

  observation: (id: number): Promise<FunctionCallRecord[]> =>
    api
      .get<{ data: FunctionCallRecord[] }>(`/me/operation-functions/${id}/observation`)
      .then(r => r.data ?? []),

  // A rollback that changes what the function may reach comes back with
  // requires_confirmation=true and the authority diff, and applies only when
  // called again with confirmed=true.
  rollback: (
    id: number,
    body: { version_id: number; confirmed?: boolean; acknowledged?: string[] },
  ): Promise<FunctionRollbackResult> =>
    api
      .post<{ data: FunctionRollbackResult }>(`/me/operation-functions/${id}/rollback`, body)
      .then(r => r.data),
};
