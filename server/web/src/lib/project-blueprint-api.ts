import { apiFetch } from './api'
import type { OwnerRef } from '@/types/org'

// A project blueprint (UI: "项目模板" / project template) is a reusable snapshot
// of a project's kanban columns + default scenes. Saved from an existing project
// or seeded as a builtin, and applied when creating a new one. NOT the
// decommissioned legacy "workflow".
export interface ProjectBlueprintSummary {
  id: number
  name: string
  description: string
  owner: OwnerRef | null
  source: string // 'user' | 'builtin'
  slug: string
  is_builtin: boolean
  column_count: number
  scene_count: number
  created_at: string
}

export interface SaveBlueprintBody {
  name: string
  description?: string
}

// One column inside a template. position is server-assigned on write (by array
// order) and only meaningful on reads.
export interface BlueprintColumn {
  name: string
  position: number
  op_primitive: string // 'none' | 'instruct' | 'complete'
  op_instruction: string
  when_to_use: string
  lifecycle_mapping: string
}

export interface BlueprintScene {
  slug: string
  display_name: string
  source: string
}

export interface ProjectBlueprintDetail extends ProjectBlueprintSummary {
  columns: BlueprintColumn[]
  scenes: BlueprintScene[]
}

export interface BlueprintWriteBody {
  name: string
  description?: string
  columns: BlueprintColumn[]
  scenes?: BlueprintScene[]
  owner?: OwnerParam
}

interface OwnerParam {
  type: string
  id: number
}

function ownerQuery(owner?: OwnerParam): string {
  if (!owner || !owner.type) return ''
  return `?owner_type=${encodeURIComponent(owner.type)}&owner_id=${owner.id}`
}

export const projectBlueprintApi = {
  // List templates usable by an owner (builtins + that owner's own). Omit owner
  // to scope to the caller's personal space.
  list: (owner?: OwnerParam) =>
    apiFetch<ProjectBlueprintSummary[]>(`/project-blueprints${ownerQuery(owner)}`),

  // The blueprint id pre-selected when creating a project for the owner.
  getDefault: (owner?: OwnerParam) =>
    apiFetch<{ blueprint_id: number }>(`/project-blueprints/default${ownerQuery(owner)}`),

  // Set the owner's default template.
  setDefault: (id: number, owner?: OwnerParam) =>
    apiFetch<void>(`/project-blueprints/${id}/default`, {
      method: 'PUT',
      body: JSON.stringify(owner ? { owner_type: owner.type, owner_id: owner.id } : {}),
    }),

  saveFromProject: (projectId: number, body: SaveBlueprintBody) =>
    apiFetch<ProjectBlueprintSummary>(`/projects/${projectId}/blueprints`, {
      method: 'POST',
      body: JSON.stringify({ name: body.name, description: body.description ?? '' }),
    }),

  // Full template detail (columns + scenes).
  getDetail: (id: number) =>
    apiFetch<ProjectBlueprintDetail>(`/project-blueprints/${id}`),

  // Create a brand-new template from explicit columns + scenes.
  create: (body: BlueprintWriteBody) =>
    apiFetch<ProjectBlueprintDetail>('/project-blueprints', {
      method: 'POST',
      body: JSON.stringify({
        name: body.name,
        description: body.description ?? '',
        columns: body.columns,
        scenes: body.scenes ?? [],
        owner: body.owner,
      }),
    }),

  // Edit name / description / columns / scenes.
  update: (id: number, body: BlueprintWriteBody) =>
    apiFetch<ProjectBlueprintDetail>(`/project-blueprints/${id}`, {
      method: 'PUT',
      body: JSON.stringify({
        name: body.name,
        description: body.description ?? '',
        columns: body.columns,
        scenes: body.scenes ?? [],
      }),
    }),

  // Copy an existing template (incl. builtins) into a new user template.
  duplicate: (id: number, name?: string, owner?: OwnerParam) =>
    apiFetch<ProjectBlueprintDetail>(`/project-blueprints/${id}/duplicate`, {
      method: 'POST',
      body: JSON.stringify({ name: name ?? '', owner }),
    }),

  remove: (id: number) =>
    apiFetch<void>(`/project-blueprints/${id}`, { method: 'DELETE' }),
}
