import { api } from './api'

export interface AgentFile {
  id: number
  name: string
  description: string
  dir_path: string
  file_hash: string
  source_url: string | null
  driver: string
  capabilities: string
  created_at: string
  updated_at: string
}

export interface AgentFileDetail extends AgentFile {
  content: string
}

export const agentFileApi = {
  list: () =>
    api.get<{ agents: AgentFile[] }>('/agents').then((r) => r.agents),

  get: (id: number) =>
    api.get<AgentFileDetail>(`/agents/${id}`),

  create: (data: { name: string; description: string; content: string; source_url?: string }) =>
    api.post<AgentFile>('/agents', data),

  update: (id: number, data: { description: string; content: string }) =>
    api.put<void>(`/agents/${id}`, data),

  delete: (id: number) =>
    api.delete<void>(`/agents/${id}`),
}
