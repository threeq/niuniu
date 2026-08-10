import type { Workspace } from '@/types/api';

// Source of truth for which workspace statuses qualify as "needs my attention".
// Order is the lifecycle progression: not started → review → running → urgent.
// Anything emitted by getPriorityKind below MUST appear here, and vice-versa —
// the type is derived from this tuple so TS rejects drift.
export const PRIORITY_KINDS = ['not_started', 'needs_review', 'running', 'attention'] as const;
export type PriorityKind = (typeof PRIORITY_KINDS)[number];

export function getStatusDotClass(status: string): string {
  switch (status) {
    case 'running': return 'bg-success';
    case 'attention': return 'bg-destructive';
    case 'needs_review':
    case 'created': return 'bg-warning';
    case 'deleting': return 'bg-warning';
    default: return 'bg-muted-foreground/50';
  }
}

/**
 * Tailwind classes for a per-card status badge — colored chip showing the
 * workspace's runtime status (e.g. "运行中", "需审阅"). Mirrors the dot
 * color but as a filled chip so the status is readable at a glance even
 * for users who don't pick up on the 6px dot.
 */
export function getStatusBadgeClass(status: string): string {
  switch (status) {
    case 'running': return 'bg-success/15 text-success';
    case 'attention': return 'bg-destructive/15 text-destructive';
    case 'needs_review': return 'bg-warning/15 text-warning';
    case 'created': return 'bg-info/15 text-info';
    case 'completed': return 'bg-muted text-muted-foreground';
    case 'deleting': return 'bg-warning/15 text-warning';
    default: return 'bg-muted text-muted-foreground';
  }
}

export function isStatusPulsing(status: string): boolean {
  return status === 'running';
}

export function formatTimeAgo(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffSec = Math.floor(diffMs / 1000);
  const diffMin = Math.floor(diffSec / 60);
  const diffHour = Math.floor(diffMin / 60);
  const diffDay = Math.floor(diffHour / 24);

  if (diffDay > 0) return `${diffDay}d ago`;
  if (diffHour > 0) return `${diffHour}h ago`;
  if (diffMin > 0) return `${diffMin}m ago`;
  return 'just now';
}

// Short absolute date — "MM-DD" within the current calendar year, full
// "YYYY-MM-DD" otherwise. Used in cards where space prevents a full
// timestamp; the full ISO is exposed via the wrapping span's title tooltip.
export function formatShortDate(dateStr: string): string {
  const d = new Date(dateStr);
  const now = new Date();
  const mm = String(d.getMonth() + 1).padStart(2, '0');
  const dd = String(d.getDate()).padStart(2, '0');
  if (d.getFullYear() === now.getFullYear()) return `${mm}-${dd}`;
  return `${d.getFullYear()}-${mm}-${dd}`;
}

// Duration between two timestamps, rendered as a single largest non-zero
// unit ("3d" / "5h" / "12m"). Matches formatTimeAgo's one-unit style so a
// card row reads uniformly. Returns "<1m" for sub-minute spans and an
// empty string when start/end are missing or end precedes start.
export function formatDuration(startStr: string | undefined, endStr: string | undefined): string {
  if (!startStr || !endStr) return '';
  const start = new Date(startStr).getTime();
  const end = new Date(endStr).getTime();
  if (!Number.isFinite(start) || !Number.isFinite(end)) return '';
  const ms = end - start;
  if (ms <= 0) return '<1m';
  const min = Math.floor(ms / 60000);
  const hr = Math.floor(min / 60);
  const day = Math.floor(hr / 24);
  if (day > 0) return `${day}d`;
  if (hr > 0) return `${hr}h`;
  if (min > 0) return `${min}m`;
  return '<1m';
}

export function formatCompactCount(count: number): string {
  if (!Number.isFinite(count) || count <= 0) return '0';
  if (count > 999) return '999+';
  return String(count);
}

export function getPriorityKind(ws: Workspace): PriorityKind | null {
  switch (ws.status) {
    case 'attention': return 'attention';
    case 'needs_review': return 'needs_review';
    case 'running': return 'running';
    case 'created': return 'not_started';
    default: return null;
  }
}
