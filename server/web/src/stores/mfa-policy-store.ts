import { create } from 'zustand'
import { apiFetch } from '@/lib/api'
import type { MfaPolicy } from '@/types/api'

interface MfaPolicyStoreState {
  policy: MfaPolicy | null
  /** True once a fetch has resolved (success or handled failure). */
  loaded: boolean
  /** Fetch the current user's mandatory-enrollment policy. Non-fatal on error. */
  fetch: () => Promise<void>
  /** Clear the blocking state after the user finishes enrolling. */
  markEnrolled: () => void
}

export const useMfaPolicyStore = create<MfaPolicyStoreState>((set) => ({
  policy: null,
  loaded: false,
  fetch: async () => {
    try {
      const policy = await apiFetch<MfaPolicy>('/auth/mfa/policy', { suppressError: true })
      set({ policy, loaded: true })
    } catch {
      // Non-fatal: the gate fails open on error (no overlay) so a transient
      // hiccup can't strand the user behind a page they cannot dismiss. The
      // backend MFAEnrollGuard fails closed and is the real enforcement point.
      set({ loaded: true })
    }
  },
  markEnrolled: () =>
    set((s) => ({
      policy: s.policy ? { ...s.policy, enabled: true, needs_setup: false } : s.policy,
    })),
}))
