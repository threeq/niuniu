import i18n from '@/i18n'
import type { WorkspaceStatus } from '@/types/api'

const STATUS_KEYS: WorkspaceStatus[] = ['created', 'running', 'needs_review', 'attention', 'completed', 'deleting']

// Returns the localized label for a workspace status. Reads via `i18n.t` lazily
// so the string updates on language change for callers that re-render.
export function getWorkspaceStatusLabel(status: WorkspaceStatus): string {
  return i18n.t(`workspaces:status.${status}`)
}

// Backwards-compatible Proxy that lazily resolves i18n strings on property
// access. Existing callers (`WORKSPACE_STATUS_LABELS[status]`) keep working
// without changes, while every read picks up the current language.
export const WORKSPACE_STATUS_LABELS = new Proxy({} as Record<WorkspaceStatus, string>, {
  get(_target, prop: string) {
    if (STATUS_KEYS.includes(prop as WorkspaceStatus)) {
      return i18n.t(`workspaces:status.${prop}`)
    }
    return undefined
  },
}) as Record<WorkspaceStatus, string>
