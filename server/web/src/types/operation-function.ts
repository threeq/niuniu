// Sandbox operation functions (Epic #726 / #739).
//
// These mirror the server's service.FunctionView / sandbox.Availability /
// FunctionCallRecord shapes. A function is AI-authored code that runs in a
// throwaway OS-fenced process and reaches the outside world only through the
// integration proxy.

export interface FunctionEgressOp {
  operation?: string;
  method?: string;
  path?: string;
  read_only?: boolean;
}

export interface FunctionLimits {
  timeout_ms?: number;
  memory_mb?: number;
  max_output_bytes?: number;
  max_log_bytes?: number;
  max_egress?: number;
  max_request_bytes?: number;
  max_response_bytes?: number;
}

export interface OperationFunction {
  id: number;
  provider_id: number;
  provider_name: string;
  name: string;
  /** The MCP tool name this function projects to: ext__<source>__<function>. */
  tool_name: string;
  description: string;
  runtime: string;
  code: string;
  code_hash: string;
  entry: string;
  limits: FunctionLimits;
  egress: FunctionEgressOp[];
  idempotent: boolean;
  enabled: boolean;
  version: number;
  status: 'observing' | 'active' | 'disabled';
  observation_calls: number;
  observation_limit: number;
  observing: boolean;
  created_at: string;
  updated_at: string;
}

export interface FunctionCallRecord {
  id: number;
  function_id: number;
  function_name: string;
  provider_name: string;
  caller: string;
  status: string;
  error_code: string;
  error_message: string;
  duration_ms: number;
  egress_calls: number;
  egress_ops: string[] | null;
  input_sha256: string;
  output_bytes: number;
  peak_memory_bytes: number;
  idempotency_key: string;
  replayed: boolean;
  observation: boolean;
  fence_backend: string;
  code_hash: string;
  created_at: string;
}

export interface FunctionVersion {
  id: number;
  version: number;
  code_hash: string;
  egress: FunctionEgressOp[];
  idempotent: boolean;
  active: boolean;
  published_by: number;
  created_at: string;
}

export interface FunctionRollbackResult {
  rolled_back: boolean;
  function_id: number;
  version?: number;
  requires_confirmation?: boolean;
  changed_scopes?: { key: string; before: string; after: string }[];
  detail?: string;
}

export interface FunctionInvokeResult {
  ok: boolean;
  output?: unknown;
  error?: string;
  error_code?: string;
  logs?: string[];
  duration_ms: number;
  egress_calls: number;
  egress_ops?: string[];
  replayed?: boolean;
  observation?: boolean;
  fence_backend?: string;
  audit_id?: number;
  retry_after_ms?: number;
  detail?: string;
}

/** One enforcement mechanism the OS fence relies on. */
export interface FenceComponent {
  name: string;
  ok: boolean;
  required: boolean;
  detail?: string;
}

/** The platform fence's self-check verdict. */
export interface FenceAvailability {
  platform: string;
  backend: string;
  available: boolean;
  degraded: boolean;
  reason?: string;
  components: FenceComponent[];
  probed_at: string;
}

/** One attempted escape and whether the OS refused it. */
export interface FenceProbe {
  name: string;
  denied: boolean;
  error?: string;
}

export interface FenceReport {
  backend: string;
  ok: boolean;
  probes: FenceProbe[];
  checked_at: string;
}
