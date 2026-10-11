// Integration draft pipeline — shared types (Epic #726 / #734).
//
// Mirrors the Go view in server/internal/service/integration_draft.go. A draft
// is a persisted candidate that is NOT live: creating one changes nothing about
// the running integration, which is why an agent may draft freely while only a
// human may confirm and publish.
//
// `card` is the system-rendered impact decision (see types/integration-confirm.ts
// and IntegrationConfirmCard). It is built by the server from structured fields;
// the UI must render it verbatim rather than compose any of its own.

import type { ConfirmCardModel } from './integration-confirm';

export type DraftKind = 'provider' | 'data_source';

export type DraftStatus =
  | 'draft'
  | 'lint_failed'
  | 'confirmed'
  | 'published'
  | 'rejected';

export interface DraftIssue {
  severity: 'error' | 'warning';
  path: string;
  message: string;
}

export interface IntegrationDraftView {
  id: number;
  kind: DraftKind;
  target_name: string;
  target_id: number;
  status: DraftStatus;
  /** True when the draft came from an agent-facing path. Such a draft can never
   *  be published by an agent — only by the user. */
  agent_authored: boolean;
  /** The candidate payload. Shown for context; the card is the authoritative
   *  rendering of what will be enforced. */
  provider?: ProviderDraftPayload;
  data_source?: DataSourceDraftPayload;
  issues: DraftIssue[];
  card?: ConfirmCardModel;
  confirm_payload?: string;
  /** True when the candidate widens authority (scope widened / writes enabled)
   *  and the explicit second confirmation has not been given. */
  requires_second_confirmation?: boolean;
  pending_risk_codes?: string[];
  created_at: string;
  published_at?: string;
}

export interface ProviderDraftPayload {
  label?: string;
  base_url: string;
  contract?: unknown;
  whitelist?: string;
  openapi_url?: string;
  write_enabled: boolean;
}

export interface DataSourceDraftPayload {
  name: string;
  kind: string;
  config: Record<string, unknown>;
  scope_config: Record<string, unknown>;
  default_access_mode: 'read' | 'readwrite';
  require_confirm: 'always' | 'writes_only';
}

export interface DraftPublishResult {
  published: boolean;
  rolled_back: boolean;
  enabled: boolean;
  target_id?: number;
  version?: number;
  detail?: string;
  verify?: { ok: boolean; status?: number; detail?: string };
  next_action?: string;
  alternatives?: string[];
}

export interface DraftRollbackResult {
  rolled_back: boolean;
  enabled: boolean;
  target_id?: number;
  version?: number;
  /** True when the rollback changes scope and needs a fresh confirmation
   *  before it is applied. `card` then carries what to show. */
  requires_confirmation?: boolean;
  card?: ConfirmCardModel;
  detail?: string;
  next_action?: string;
}

export interface IntegrationVersion {
  id: number;
  kind: DraftKind;
  target_name: string;
  target_id: number;
  version: number;
  active: number;
  published_by: number;
  created_at: string;
}
