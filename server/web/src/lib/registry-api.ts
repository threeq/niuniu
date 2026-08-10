import { api } from '@/lib/api'
import type { AgentInfo, AgentDetail, AgentRegistryList } from '@/types/registry'

export const registryApi = {
  list: () =>
    api.get<AgentRegistryList>('/agent-registry/list'),

  get: (source: string, name: string) =>
    api.get<AgentDetail>(`/agent-registry/${source}/${name}`),

  clone: (data: { source: string; name: string; new_name?: string }) =>
    api.post<AgentInfo>('/agent-registry/clone', data),

  refresh: (source: string) =>
    api.post<{ count: number }>(`/agent-registry/${source}/refresh`),

  createCustom: (data: { name: string; description: string; content: string }) =>
    api.post<AgentInfo>('/agent-registry/custom', data),

  updateCustom: (name: string, data: { description: string; content: string }) =>
    api.put<void>(`/agent-registry/custom/${name}`, data),

  deleteCustom: (name: string) =>
    api.delete<void>(`/agent-registry/custom/${name}`),
}
