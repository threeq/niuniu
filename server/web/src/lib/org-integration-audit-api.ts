import {
  getAccessToken,
  isAccessTokenNearExpiry,
  refreshAccessToken,
} from '@/stores/auth-store'
import { api } from './api'
import type {
  OrgActivityFilters,
  OrgActivityPage,
} from '@/types/org-integration-activity'

// Team-edition cross-user integration audit (issue #743).
//
// Routes live under the license-gated /orgs group and are admin-only; a 403 is
// the real enforcement (the tab's role check is convenience). Both calls take
// the same filter set, so "what I see" and "what I export" can never diverge.

/** Serialises the filter set, dropping blanks so the server sees no filter. */
function toParams(
  filters: OrgActivityFilters,
  extra?: Record<string, string | number | undefined>,
): Record<string, string | number | undefined> {
  return {
    kind: filters.kind || undefined,
    member_id: filters.member_id || undefined,
    project_id: filters.project_id || undefined,
    credential_scope: filters.credential_scope || undefined,
    outcome: filters.outcome || undefined,
    from: filters.from || undefined,
    to: filters.to || undefined,
    ...extra,
  }
}

/** One page of the org's merged integration activity (newest first). */
function list(
  orgID: number,
  filters: OrgActivityFilters,
  limit: number,
  offset: number,
): Promise<OrgActivityPage> {
  return api
    .get<{ data: OrgActivityPage }>(`/orgs/${orgID}/integration-activity`, {
      params: toParams(filters, { limit, offset }),
    })
    .then((r) => r.data)
}

/**
 * Fetches the same view as CSV. The JSON helper cannot be reused: the response
 * is a file, not an envelope, and it streams rows the client never renders.
 * Auth mirrors `apiFetch` (bearer + proactive refresh); failures surface the
 * server's message so the over-cap 400 explains how to narrow the range.
 */
async function exportCsv(
  orgID: number,
  filters: OrgActivityFilters,
): Promise<Blob> {
  if (isAccessTokenNearExpiry()) {
    await refreshAccessToken()
  }
  const token = getAccessToken()
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(toParams(filters))) {
    if (value !== undefined && value !== '') search.append(key, String(value))
  }
  const qs = search.toString()
  const res = await fetch(
    `/api/orgs/${orgID}/integration-activity/export${qs ? `?${qs}` : ''}`,
    { headers: token ? { Authorization: `Bearer ${token}` } : {} },
  )
  if (!res.ok) {
    // Same envelope shapes as apiFetch: {error:{message}} or {error:"..."}.
    const raw = await res.text().catch(() => '')
    let message = `HTTP ${res.status}`
    try {
      const parsed = JSON.parse(raw) as { error?: unknown; message?: string }
      const err = parsed?.error
      if (typeof err === 'string') message = err
      else if (err && typeof err === 'object' && 'message' in err) {
        message = String((err as { message?: string }).message ?? message)
      } else if (parsed?.message) {
        message = parsed.message
      }
    } catch {
      if (raw) message = raw
    }
    throw new Error(message)
  }
  return res.blob()
}

export const orgIntegrationAuditApi = { list, exportCsv }

/** Filename for a saved export; mirrors the server's Content-Disposition. */
export function exportFilename(orgID: number): string {
  const stamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\..+/, 'Z')
  return `integration-activity-org${orgID}-${stamp}.csv`
}
