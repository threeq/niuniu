// Integration authorization confirmation card — shared types.
//
// Mirrors the Go model in server/internal/service/integration_confirm.go
// (issue #746). A card renders the authorization decision for an integration
// draft: the operations and scopes the system will actually enforce. Every
// field is structured fact. `advisory` is the only free-text region — it is
// untrusted document/model prose shown for human review and is never a
// decision input.

export type ConfirmDecisionKind = 'provider' | 'data_source';

export type UntrustedOriginKind =
  | 'openapi'
  | 'markdown'
  | 'html'
  | 'user_note'
  | 'model';

/** Where a document's content came from. Always untrusted. */
export interface UntrustedOrigin {
  kind: UntrustedOriginKind;
  ref?: string;
  fetched_at?: string;
}

/** One operation in the decision area. Only the enforced fields are present. */
export interface ConfirmOperation {
  name: string;
  method: string;
  path: string;
  read_only: boolean;
  /** Convenience flag mirroring a risk that targets this operation. */
  high_risk: boolean;
}

export type ScopeChange =
  | 'added'
  | 'removed'
  | 'widened'
  | 'narrowed'
  | 'changed'
  | 'unchanged';

export interface ConfirmScope {
  key: string;
  values: string[];
  change: ScopeChange;
}

export interface ConfirmDiff {
  field: string;
  before?: string;
  after?: string;
  change: string;
}

export type RiskCode =
  | 'cross_origin_host'
  | 'new_write_operation'
  | 'operation_not_in_document'
  | 'scope_widened'
  | 'write_enabled';

/** A high-risk item the user must acknowledge item by item. */
export interface ConfirmRisk {
  code: RiskCode;
  severity: 'high';
  /** Structured subject: a host, a "METHOD path" serial, or a scope key. */
  target: string;
  detail?: string;
  /** Stable identifier echoed back when the item is ticked. */
  ack_key: string;
}

export type QuarantineReason =
  | 'instruction_override'
  | 'agent_addressed'
  | 'privilege_escalation'
  | 'exfiltration';

/** Untrusted text that reads as an instruction. Display-only. */
export interface QuarantinedChunk {
  where: string;
  text: string;
  reason: QuarantineReason;
}

export interface ConfirmCardModel {
  kind: ConfirmDecisionKind;
  title: string;
  subtitle?: string;
  origin: UntrustedOrigin;
  operations: ConfirmOperation[];
  /** The read-only probe issued after publish to verify auth + connectivity.
   *  It is egress too, so it is part of what the user approves. */
  verify_probe?: ConfirmOperation;
  scopes: ConfirmScope[];
  diff: ConfirmDiff[];
  risks: ConfirmRisk[];
  advisory?: QuarantinedChunk[];
}

export interface ConfirmDecisionBody {
  decision: 'approve' | 'reject';
  /** Every acknowledged ack_key, in card order. */
  acknowledged: string[];
}
