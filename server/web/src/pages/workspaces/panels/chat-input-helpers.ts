// Pure helpers for the chat-input usage pill. Extracted from chat-input.tsx
// so the visibility logic can be unit-tested without rendering the component.

export type UsagePillTone = 'critical' | 'warning' | 'info' | 'muted' | null;

export interface RateLimitStatusLike {
  status?: string;
}

// Decides the pill's tone given the current context-window usage and 5h
// rate-limit status. The pill is ALWAYS shown once a context percentage is
// known (so the user can read live occupancy at a glance); the tone only
// escalates colour as thresholds are crossed. Returns null only when there is
// no signal at all (no context data and no rate alert) — e.g. before the first
// status poll on a fresh session.
//
// Hierarchy (highest severity wins):
//   - rate_limits.five_hour.status === 'rejected'        -> critical
//   - ctxPct >= 95                                        -> critical
//   - rate_limits.five_hour.status === 'allowed_warning' -> warning
//   - ctxPct >= 80                                        -> warning
//   - ctxPct >= 75                                        -> info
//   - ctxPct known but below 75                           -> muted (neutral, still shown)
//   - otherwise (no ctxPct, no rate alert)                -> null (hidden)
export function computeUsagePillTone(
  ctxPct: number | null | undefined,
  rateStatus: RateLimitStatusLike | null | undefined,
): UsagePillTone {
  if (rateStatus?.status === 'rejected') return 'critical';
  if (ctxPct != null && ctxPct >= 95) return 'critical';
  if (rateStatus?.status === 'allowed_warning') return 'warning';
  if (ctxPct != null && ctxPct >= 80) return 'warning';
  if (ctxPct != null && ctxPct >= 75) return 'info';
  if (ctxPct != null) return 'muted';
  return null;
}
