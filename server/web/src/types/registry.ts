export interface AgentInfo {
  source: 'local' | 'community' | 'custom' | 'curated'
  name: string
  description: string
  cloned_from?: string
  tags?: string[]
  author?: string
  file_path?: string
  source_url?: string
  /** Localized label (e.g. Chinese name from the curated catalog); name stays the slug. */
  display_name?: string
  /** Decorative glyph surfaced for catalog beautification; not persisted on import. */
  emoji?: string
}

export interface AgentDetail extends AgentInfo {
  content: string
}

export type AgentRegistryList = Record<string, AgentInfo[]>
