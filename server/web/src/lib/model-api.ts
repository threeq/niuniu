import { api } from './api'
import type { ModelBudget, ModelCandidate, ModelItem } from '@/types/model'

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

export function getBudget(): Promise<ModelBudget> {
  return api.get<ModelBudget>('/model-budget')
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
