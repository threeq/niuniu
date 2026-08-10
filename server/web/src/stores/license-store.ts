import { create } from 'zustand'
import { apiFetch } from '@/lib/api'
import type { LicenseStatus } from '@/types/api'

interface LicenseStoreState {
  status: LicenseStatus | null
  /** Fetch the current license status. Non-fatal: on error the banner hides. */
  fetch: () => Promise<void>
}

export const useLicenseStore = create<LicenseStoreState>((set) => ({
  status: null,
  fetch: async () => {
    try {
      const status = await apiFetch<LicenseStatus>('/license/status', { suppressError: true })
      set({ status })
    } catch {
      // non-fatal — leave previous status (or null) so the banner just hides
    }
  },
}))
