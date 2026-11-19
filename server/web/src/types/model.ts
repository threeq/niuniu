export interface ModelItem {
  id: number
  owner_type: string
  owner_id: number
  name: string
  display_name: string
  enabled: boolean
  source: string
}

export interface ModelCandidate {
  name: string
  already_in_library: boolean
}

export interface ModelProjectBudget {
  project_id: number
  monthly_tokens: number
  used_tokens: number
}

export interface ModelBudget {
  owner_type: string
  owner_id: number
  monthly_tokens: number
  used_tokens: number
  period: string
  projects: ModelProjectBudget[]
}
