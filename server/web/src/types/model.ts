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

/** One bucket of a usage breakdown (model name, CLI name, or an id string). */
export interface ModelUsageRow {
  key: string
  total_tokens: number
  requests: number
}

/** Which breakdown the summary endpoint should return. Omit for totals only. */
export type ModelUsageGroup = 'model' | 'provider' | 'project' | 'workspace' | 'cli'

/**
 * The endpoint returns one breakdown per call; the dimensions other than the
 * requested one stay absent.
 */
export interface ModelUsageSummary {
  total_tokens: number
  requests: number
  by_model?: ModelUsageRow[]
  by_provider?: ModelUsageRow[]
  by_project?: ModelUsageRow[]
  by_workspace?: ModelUsageRow[]
  by_cli?: ModelUsageRow[]
}
