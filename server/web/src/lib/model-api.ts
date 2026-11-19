import { api } from './api'
import type {
  ModelBudget,
  ModelCandidate,
  ModelItem,
  ModelUsageGroup,
  ModelUsageSummary,
} from '@/types/model'

/** Optional explicit owner scope; omitted = the caller's personal owner. */
export interface OwnerParam {
  type: 'user' | 'org'
  id: number
}

function ownerParams(owner?: OwnerParam): Record<string, string> | undefined {
  if (!owner) return undefined
  return { owner_type: owner.type, owner_id: String(owner.id) }
}

// Owner-scoped shared model library. Names are shared by every provider and
// CLI; the library never belongs to a specific provider.
export function listModels(includeDisabled = false): Promise<ModelItem[]> {
  return api.get<ModelItem[]>('/models', {
    params: includeDisabled ? { all: 1 } : undefined,
  })
}

export function upsertModel(
  name: string,
  displayName: string,
  enabled = true
): Promise<ModelItem> {
  return api.post<ModelItem>('/models', {
    name,
    display_name: displayName,
    enabled,
  })
}

export function updateModel(
  id: number,
  patch: { display_name?: string; enabled?: boolean }
): Promise<ModelItem> {
  return api.patch<ModelItem>(`/models/${id}`, patch)
}

export function deleteModel(id: number): Promise<void> {
  return api.delete<void>(`/models/${id}`)
}

export function refreshPreview(providerId: number): Promise<ModelCandidate[]> {
  return api.post<ModelCandidate[]>('/models/refresh-preview', {
    provider_id: providerId,
  })
}

export function importModels(
  names: string[]
): Promise<{ added: number; skipped: number }> {
  return api.post<{ added: number; skipped: number }>('/models/import', {
    names,
  })
}

export function getBudget(owner?: OwnerParam): Promise<ModelBudget> {
  return api.get<ModelBudget>('/model-budget', {
    params: ownerParams(owner),
  })
}

/**
 * Aggregate model usage for one owner. The backend returns one breakdown per
 * call: omit groupBy for the window total, or pass a dimension to get its rows.
 * The window defaults to the current UTC calendar month.
 */
export function getUsage(
  groupBy?: ModelUsageGroup,
  owner?: OwnerParam
): Promise<ModelUsageSummary> {
  const params: Record<string, string> = { ...(ownerParams(owner) ?? {}) }
  if (groupBy) params.group_by = groupBy
  return api.get<ModelUsageSummary>('/model-usage/summary', {
    params: Object.keys(params).length > 0 ? params : undefined,
  })
}

export function setBudget(monthlyTokens: number): Promise<void> {
  return api.put<void>('/model-budget', { monthly_tokens: monthlyTokens })
}

export function setProjectBudget(
  projectId: number,
  monthlyTokens: number
): Promise<void> {
  return api.put<void>(`/model-budget/projects/${projectId}`, {
    monthly_tokens: monthlyTokens,
  })
}

export function deleteProjectBudget(projectId: number): Promise<void> {
  return api.delete<void>(`/model-budget/projects/${projectId}`)
}
