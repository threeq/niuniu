import { apiFetch } from './api';

/**
 * Per-workspace local-runner REST + stream contract (#526·子A).
 *
 * The backend now serves these routes (config storage, online gate, conditional
 * MCP/prompt injection, log stream — 子B/子D landed); the desktop runner opens
 * the live reverse channel. These helpers remain the single source of truth for
 * the wire shape, consumed by `stores/local-runner-store.ts`.
 *
 * Routes:
 *   GET    /api/workspaces/:id/local-runner        → LocalRunnerStateDTO
 *   PUT    /api/workspaces/:id/local-runner         (LocalRunnerConfigDTO) → LocalRunnerStateDTO
 *   DELETE /api/workspaces/:id/local-runner
 *   SSE/WS /ws/workspaces/:id/local-runner/logs    → LocalRunnerLogEntry stream
 *
 * All calls pass `suppressError` so a transient failure doesn't raise the global
 * "API Error" toast — the store keeps its localStorage copy (which the desktop
 * bridge also harvests) as a fallback.
 */

export type LocalRunnerStatusDTO = 'unbound' | 'connecting' | 'active' | 'error';

/** Snake_case wire DTO for the runner config. */
export interface LocalRunnerConfigDTO {
  local_dir: string;
  prompt_snippet: string;
  allowed_commands: string[];
  always_allow_persist: boolean;
}

export interface LocalRunnerStateDTO {
  status: LocalRunnerStatusDTO;
  config: LocalRunnerConfigDTO | null;
}

export const localRunnerApi = {
  get: (workspaceId: string) =>
    apiFetch<LocalRunnerStateDTO>(`/workspaces/${workspaceId}/local-runner`, {
      method: 'GET',
      suppressError: true,
    }),

  put: (workspaceId: string, config: LocalRunnerConfigDTO) =>
    apiFetch<LocalRunnerStateDTO>(`/workspaces/${workspaceId}/local-runner`, {
      method: 'PUT',
      body: JSON.stringify(config),
      suppressError: true,
    }),

  delete: (workspaceId: string) =>
    apiFetch<void>(`/workspaces/${workspaceId}/local-runner`, {
      method: 'DELETE',
      suppressError: true,
    }),

  /** Relative WS URL for the log stream — token appended by the caller. */
  logsStreamUrl: (workspaceId: string) =>
    `/ws/workspaces/${workspaceId}/local-runner/logs`,
};
