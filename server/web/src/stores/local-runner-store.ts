import { create } from 'zustand';
import {
  localRunnerApi,
  type LocalRunnerConfigDTO,
  type LocalRunnerStatusDTO,
} from '@/lib/local-runner-api';
import {
  harvestRunnerConfigToDesktop,
  unbindRunnerFromDesktop,
} from '@/lib/desktop-runner-context';

/**
 * Per-workspace local-runner state (#526·子A).
 *
 * State machine: `unbound → connecting → active → error`.
 *   - unbound:    no directory bound yet → "启动本地执行器" CTA.
 *   - connecting: config saved, runner spinning up → loading state.
 *   - active:     bound + running → status dot + "本地执行日志" + gear.
 *   - error:      bind/connection failed → error affordance.
 *
 * Persistence: config is written to `localStorage` (the record the desktop
 * "保存即连接" bridge harvests — #526·子E) AND best-effort forwarded to the REST
 * contract (`localRunnerApi`), which the backend now serves. When the SPA runs
 * inside a desktop remote connection, saving here is what triggers the desktop
 * to create the binding and open the live reverse channel; in a plain browser
 * the REST copy is persisted and the runner comes online when a desktop opens
 * the same connection. A failed REST forward is swallowed — the localStorage
 * copy still drives the desktop bridge.
 */

export type LocalRunnerStatus = LocalRunnerStatusDTO;

export interface LocalRunnerConfig {
  /** Absolute path to the local working directory to bind. */
  localDir: string;
  /** Prompt fragment injected to steer the agent toward local tools. */
  promptSnippet: string;
  /** Command whitelist the runner may execute without prompting. */
  allowedCommands: string[];
  /** Persist "always allow" decisions across sessions. */
  alwaysAllowPersist: boolean;
}

export interface LocalRunnerLogEntry {
  id: string;
  ts: number;
  level: 'command' | 'stdout' | 'stderr' | 'system';
  text: string;
}

export interface WorkspaceRunnerState {
  status: LocalRunnerStatus;
  config: LocalRunnerConfig | null;
  logs: LocalRunnerLogEntry[];
  error: string | null;
}

export const DEFAULT_RUNNER_STATE: WorkspaceRunnerState = {
  status: 'unbound',
  config: null,
  logs: [],
  error: null,
};

/**
 * Default command whitelist — common safe build/test verbs.
 *
 * The default *prompt* fragment is user-facing localized copy and lives in i18n
 * (`localRunner.config.defaultPrompt`), not here — the config dialog seeds the
 * textarea from `t()`.
 */
export const DEFAULT_ALLOWED_COMMANDS = [
  'npm',
  'pnpm',
  'yarn',
  'node',
  'go',
  'make',
  'python',
  'git',
];

const STORAGE_PREFIX = 'niuniu.localRunner.';

function storageKey(workspaceId: string): string {
  return `${STORAGE_PREFIX}${workspaceId}`;
}

function toDTO(config: LocalRunnerConfig): LocalRunnerConfigDTO {
  return {
    local_dir: config.localDir,
    prompt_snippet: config.promptSnippet,
    allowed_commands: config.allowedCommands,
    always_allow_persist: config.alwaysAllowPersist,
  };
}

/** Read a persisted config from localStorage, tolerating absent/corrupt data. */
function readPersisted(workspaceId: string): LocalRunnerConfig | null {
  try {
    const raw = localStorage.getItem(storageKey(workspaceId));
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<LocalRunnerConfig>;
    if (typeof parsed.localDir !== 'string' || !parsed.localDir) return null;
    return {
      localDir: parsed.localDir,
      promptSnippet:
        typeof parsed.promptSnippet === 'string' ? parsed.promptSnippet : '',
      allowedCommands: Array.isArray(parsed.allowedCommands)
        ? parsed.allowedCommands.filter((c): c is string => typeof c === 'string')
        : [],
      alwaysAllowPersist: parsed.alwaysAllowPersist === true,
    };
  } catch {
    return null;
  }
}

function writePersisted(workspaceId: string, config: LocalRunnerConfig): void {
  try {
    localStorage.setItem(storageKey(workspaceId), JSON.stringify(config));
  } catch {
    // Storage disabled/full — the in-memory store still reflects the config for
    // the session; nothing else to do.
  }
}

function removePersisted(workspaceId: string): void {
  try {
    localStorage.removeItem(storageKey(workspaceId));
  } catch {
    /* ignore */
  }
}

let logSeq = 0;
function nextLogId(): string {
  logSeq += 1;
  return `log-${Date.now()}-${logSeq}`;
}

interface LocalRunnerStore {
  byWorkspace: Record<string, WorkspaceRunnerState>;
  /** Idempotently hydrate a workspace's state from localStorage. */
  ensureLoaded: (workspaceId: string, workspaceName?: string) => void;
  /** Read current state (never null — defaults to unbound). */
  getState: (workspaceId: string) => WorkspaceRunnerState;
  /** Persist a config and transition unbound → connecting → active. */
  saveConfig: (
    workspaceId: string,
    config: LocalRunnerConfig,
    workspaceName?: string,
  ) => Promise<void>;
  /** Unbind the runner (DELETE contract + clear localStorage). */
  clear: (workspaceId: string) => Promise<void>;
  appendLog: (workspaceId: string, entry: Omit<LocalRunnerLogEntry, 'id'>) => void;
  setStatus: (workspaceId: string, status: LocalRunnerStatus, error?: string | null) => void;
}

export const useLocalRunnerStore = create<LocalRunnerStore>((set, get) => {
  const patch = (workspaceId: string, next: Partial<WorkspaceRunnerState>) => {
    set((s) => {
      const prev = s.byWorkspace[workspaceId] ?? DEFAULT_RUNNER_STATE;
      return {
        byWorkspace: { ...s.byWorkspace, [workspaceId]: { ...prev, ...next } },
      };
    });
  };

  return {
    byWorkspace: {},

    ensureLoaded: (workspaceId, workspaceName = '') => {
      if (get().byWorkspace[workspaceId]) return;
      const config = readPersisted(workspaceId);
      set((s) => ({
        byWorkspace: {
          ...s.byWorkspace,
          [workspaceId]: config
            ? { status: 'active', config, logs: [], error: null }
            : { ...DEFAULT_RUNNER_STATE },
        },
      }));
      // Re-register with the desktop shell on (re)load so an already-configured
      // workspace reconnects its reverse channel after a page reload/reconnect.
      if (config)
        harvestRunnerConfigToDesktop(workspaceId, config.localDir, workspaceName);
    },

    getState: (workspaceId) =>
      get().byWorkspace[workspaceId] ?? DEFAULT_RUNNER_STATE,

    saveConfig: async (workspaceId, config, workspaceName = '') => {
      patch(workspaceId, { status: 'connecting', error: null });
      writePersisted(workspaceId, config);
      // Forward to the backend (source of the authoritative whitelist the desktop
      // gateway reads). Best-effort: if the REST call fails the localStorage copy
      // the desktop bridge harvests still drives binding + connect.
      try {
        await localRunnerApi.put(workspaceId, toDTO(config));
      } catch {
        /* forward failed — localStorage copy already persisted */
      }
      // Drive the desktop shell to bind + open the reverse channel (保存即连接).
      harvestRunnerConfigToDesktop(workspaceId, config.localDir, workspaceName);
      // Optimistic "active"; the real status is driven by the desktop runner via
      // the status stream once the reverse channel actually comes online.
      patch(workspaceId, { status: 'active', config, error: null });
    },

    clear: async (workspaceId) => {
      removePersisted(workspaceId);
      unbindRunnerFromDesktop(workspaceId);
      try {
        await localRunnerApi.delete(workspaceId);
      } catch {
        /* backend absent */
      }
      set((s) => ({
        byWorkspace: { ...s.byWorkspace, [workspaceId]: { ...DEFAULT_RUNNER_STATE } },
      }));
    },

    appendLog: (workspaceId, entry) => {
      set((s) => {
        const prev = s.byWorkspace[workspaceId] ?? DEFAULT_RUNNER_STATE;
        return {
          byWorkspace: {
            ...s.byWorkspace,
            [workspaceId]: {
              ...prev,
              logs: [...prev.logs, { ...entry, id: nextLogId() }],
            },
          },
        };
      });
    },

    setStatus: (workspaceId, status, error = null) => {
      patch(workspaceId, { status, error });
    },
  };
});

/** Selector hook: reactive per-workspace runner state. */
export function useLocalRunner(workspaceId: string): WorkspaceRunnerState {
  return useLocalRunnerStore(
    (s) => s.byWorkspace[workspaceId] ?? DEFAULT_RUNNER_STATE,
  );
}
