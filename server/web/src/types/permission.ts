// Types for the chat-side permission-prompt UI.
//
// Wire shape mirrors the Go DTOs in server/internal/api/permission.go exactly.
// Niuniu's REST wrapper (`api.ts`) does NOT convert snake_case to camelCase, so
// these types stay in snake_case to match the raw JSON from the server.
//
// Timestamp transport note: per Task 5 + spec §6.5, both the REST list endpoint
// and the SSE stream emit `requested_at` / `expires_at` / `created_at` as unix
// milliseconds (number). Other niuniu types use ISO date strings (e.g. BaseEntity.
// created_at), but the permission DTOs deliberately use UnixMilli for cheap
// expiry math on the client. Keep these as `number` — do not switch to string.

export type PermissionRequestStatus =
  | 'pending'
  | 'allowed'
  | 'denied'
  | 'timeout'
  | 'cancelled';

export interface PermissionRequest {
  id: number;
  workspace_id: number;
  session_id: string;
  tool_name: string;
  tool_input: Record<string, unknown>;
  /** unix milliseconds (number), not ISO string */
  requested_at: number;
  /** unix milliseconds (number), not ISO string */
  expires_at: number;
  status: PermissionRequestStatus;
}

export type MatcherKind = 'any' | 'exact' | 'prefix' | 'glob' | 'domain';

export interface Matcher {
  kind: MatcherKind;
  value: string;
}

export interface PermissionAllowlistEntry {
  id: number;
  workspace_id: number;
  tool_name: string;
  matcher_kind: MatcherKind;
  matcher_value: string;
  /** present only when an authenticated user created the entry */
  created_by?: number;
  /** unix milliseconds (number), not ISO string */
  created_at: number;
}

export interface DecideBody {
  decision: 'allow' | 'deny';
  scope: 'once' | 'always';
  matcher?: Matcher;
  /** matches Go json tag `deny_message` */
  deny_message?: string;
}

/**
 * Tools that require a non-'any' matcher when the user picks "always allow".
 * Mirrors service.IsHighRiskTool in server/internal/service/permission_matcher.go.
 * The SPA uses this to gate the matcher-required UI before submitting.
 */
export const HIGH_RISK_TOOLS: ReadonlySet<string> = new Set([
  'Bash',
  'Edit',
  'Write',
  'WebFetch',
]);
