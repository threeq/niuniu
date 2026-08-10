import { create } from 'zustand'
import { apiFetch } from '@/lib/api'
import type { ConsentStatus } from '@/types/api'

interface ConsentStoreState {
  status: ConsentStatus | null
  /** True once a fetch has resolved (success or handled failure). */
  loaded: boolean
  /** Fetch the current user's consent status. Non-fatal on error. */
  fetch: () => Promise<void>
  /** Record acceptance of the given agreement version. Throws on failure. */
  accept: (version: string) => Promise<void>
}

export const useConsentStore = create<ConsentStoreState>((set) => ({
  status: null,
  loaded: false,
  fetch: async () => {
    try {
      const status = await apiFetch<ConsentStatus>('/consent/status', { suppressError: true })
      set({ status, loaded: true })
    } catch {
      // Non-fatal: leave previous status. The gate fails open on first-load
      // error (no overlay) so a transient hiccup can't lock the user out; the
      // backend ConsentGuard still blocks writes as a safety net.
      set({ loaded: true })
    }
  },
  accept: async (version: string) => {
    const status = await apiFetch<ConsentStatus>('/consent/accept', {
      method: 'POST',
      body: JSON.stringify({ version }),
      suppressError: true,
    })
    set({ status })
  },
}))
