import type { WsStatusStat } from '../api/types';

export interface IssueCounts {
  todo: number;
  inProgress: number;
  done: number;
}

// splitStatsByPosition groups column-level issue counts into workflow buckets
// using column position (the array order from the server is `ORDER BY
// columns.position` per server/internal/store/queries/projects.sql:19), so:
//   first column   = todo
//   middle columns = in progress (sum)
//   last column    = done
// Mirrors server/web/src/pages/projects/project-layout.tsx groupIssueStats.
// Position-based heuristic is used because the server-side ColumnIssueStat
// DTO does not carry lifecycle_mapping.
export function splitStatsByPosition(stats: { count: number }[] | undefined): IssueCounts {
  const arr = stats ?? [];
  if (arr.length === 0) return { todo: 0, inProgress: 0, done: 0 };
  if (arr.length === 1) return { todo: arr[0].count, inProgress: 0, done: 0 };
  const todo = arr[0].count;
  const done = arr[arr.length - 1].count;
  const inProgress = arr.slice(1, -1).reduce((s, col) => s + col.count, 0);
  return { todo, inProgress, done };
}

// Surface the workspace status counts a user is most likely to act on.
// Status string contract: server emits these via WsStatusStat.status from
// the workspaces.status column — see canonical enum at
// server/internal/agentproxy/proxy.go:1443 (needs_review on agent done /
// attention on agent error) and server/internal/service/agent.go:458-474
// (needs_review → attention on review timeout). Hardcoded names match the
// same priority set the dashboard WORKSPACES section uses
// (attention > needs_review > running).
export interface ProjectWsHighlights {
  attention: number;
  needsReview: number;
  running: number;
  hasAny: boolean;
}

export function summarizeWsStats(stats: WsStatusStat[] | undefined): ProjectWsHighlights {
  const arr = stats ?? [];
  const attention = arr.find((s) => s.status === 'attention')?.count ?? 0;
  const needsReview = arr.find((s) => s.status === 'needs_review')?.count ?? 0;
  const running = arr.find((s) => s.status === 'running')?.count ?? 0;
  return {
    attention,
    needsReview,
    running,
    hasAny: attention > 0 || needsReview > 0 || running > 0,
  };
}
