// Operable capability description (Epic #726 / #738).
//
// Mirrors service.CapabilityDoc on the backend: the two-layer "how to drive
// this source" document, generated from the CONFIRMED integration contract. It
// is read-only — there is no client-side editor for it.

export type CapabilityTargetKind = 'provider' | 'data_source';

export interface CapabilityParam {
  name: string;
  in: string;
  required?: boolean;
  type?: string;
  description?: string;
}

export interface CapabilityOperation {
  name: string;
  method: string;
  path: string;
  summary?: string;
  read_only: boolean;
  parameters?: CapabilityParam[];
  /** Operation-specific guidance (pagination, rate limits, common errors). */
  notes?: string;
}

export interface CapabilityScope {
  kind: string;
  allow: string[];
  deny?: string[];
  databases?: string[];
  methods?: string[];
  default_access_mode: string;
}

export interface CapabilityExample {
  task: string;
  call: string;
}

export interface CapabilityDoc {
  schema_version: string;
  target_kind: CapabilityTargetKind;
  name: string;
  display_name?: string;
  source_digest: string;
  tool: string;
  operations?: CapabilityOperation[];
  scope?: CapabilityScope;
  limits?: string[];
  usage: string;
  examples?: CapabilityExample[];
}

export interface CapabilityResponse {
  doc: CapabilityDoc;
  /** The exact block injected into the agent's workspace instruction file. */
  agent_block: string;
}
