// Team-edition cross-user integration audit view (issue #743).
// Mirrors service.OrgActivityItem — every field below is optional on the wire
// because the backend omits kind-specific columns (`omitempty`), so the SPA
// must switch on `kind` before reading them.

export type OrgActivityKind = 'api' | 'data' | 'approval'
export type OrgActivityOutcome = 'ok' | 'error' | 'pending'

/** Owner of the credential (or data source) a row was executed with. */
export type CredentialScope = 'org' | 'user'

export interface OrgActivityItem {
  id: number
  kind: OrgActivityKind
  user_id: number
  user_name?: string
  outcome: OrgActivityOutcome
  status: string
  created_at: string

  // kind === 'api'
  provider_id?: number
  provider_name?: string
  credential_id?: number
  credential_alias?: string
  credential_owner_type?: CredentialScope | ''
  credential_owner_id?: number
  method?: string
  path?: string
  status_code?: number
  body_sha256?: string
  body_bytes?: number
  body_fields?: string[]

  // kind === 'data'
  source_id?: number
  source_name?: string
  source_owner_type?: CredentialScope | ''
  source_owner_id?: number
  access_mode?: string
  database_name?: string
  objects?: string[]
  statement_summary?: string
  rows?: number
  duration_ms?: number

  // kind === 'approval'
  action?: string
  actor_label?: string
  requested_by?: number
  subject_type?: string
  subject_id?: number
  subject_label?: string
  summary?: string
  diff?: string
  note?: string
  required_role?: string
}

export interface OrgActivityPage {
  items: OrgActivityItem[]
  total: number
  limit: number
  offset: number
}

export interface OrgActivityFilters {
  kind?: OrgActivityKind | ''
  member_id?: number
  project_id?: number
  credential_scope?: CredentialScope | ''
  outcome?: OrgActivityOutcome | ''
  from?: string
  to?: string
}
