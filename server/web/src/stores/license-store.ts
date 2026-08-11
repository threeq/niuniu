import { create } from 'zustand'
import { apiFetch } from '@/lib/api'
import type { LicenseStatus } from '@/types/api'

interface LicenseStoreState {
  status: LicenseStatus | null
  /** 多租户组织（Tier 1）是否被当前许可证启用。 */
  orgEnabled: boolean
  /** Fetch the current license status. Non-fatal: on error the banner hides. */
  fetch: () => Promise<void>
}

/** 功能分级：org 是否启用。features_enabled 未返回时按已启用处理（兼容旧后端）。 */
export function isOrgEnabled(status: LicenseStatus | null): boolean {
  if (!status) return true
  return (status.features_enabled ?? []).includes('org')
}

export const useLicenseStore = create<LicenseStoreState>((set) => ({
  status: null,
  orgEnabled: true,
  fetch: async () => {
    try {
      const status = await apiFetch<LicenseStatus>('/license/status', { suppressError: true })
      set({ status, orgEnabled: isOrgEnabled(status) })
    } catch {
      // non-fatal — leave previous status (or null) so the banner just hides
    }
  },
}))
