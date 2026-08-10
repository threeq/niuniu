import { create } from 'zustand'

interface ConfigState {
  authEnabled: boolean
  /** Server build version reported by /api/health. Empty until loaded;
   *  defaults to "dev" on builds without -X ldflags. */
  serverVersion: string
  /** Convenience derived flag: personal/embedded edition runs auth-disabled.
   *  A team-edition deployment with auth-disabled is unsupported, so we treat
   *  `!authEnabled` as "personal mode" — same convention used in
   *  api/system_deps.go which derives this identically. */
  personalMode: boolean
  loaded: boolean
  /** Idempotent. Call once at SPA boot to populate authEnabled+serverVersion. */
  load: () => Promise<void>
}

/**
 * Reads the server's edition flag + version (`/api/health`).
 *
 * - Team edition: auth_enabled = true (org/user-management UI shows).
 * - Personal edition: auth_enabled = false (UI hides org chrome, About
 *   panel offers update check).
 *
 * If the request fails we fail closed (treat as personal) — better to hide
 * an org link in team edition for a moment than to expose a dead-end
 * /settings/orgs in personal edition.
 */
export const useConfigStore = create<ConfigState>((set, get) => ({
  authEnabled: false,
  serverVersion: '',
  personalMode: true,
  loaded: false,
  load: async () => {
    if (get().loaded) return
    try {
      const res = await fetch('/api/health')
      const data = await res.json()
      const authEnabled = data.auth_enabled === true
      set({
        authEnabled,
        serverVersion: typeof data.version === 'string' ? data.version : '',
        personalMode: !authEnabled,
        loaded: true,
      })
    } catch {
      // Fail closed: if /api/health is unreachable, assume personal edition
      // and hide org chrome. Better to under-show entries than to expose a
      // dead-end /settings/orgs in personal.
      set({ authEnabled: false, serverVersion: '', personalMode: true, loaded: true })
    }
  },
}))
