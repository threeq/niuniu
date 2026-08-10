import type { Workspace } from '../api/types';
import { mapWorkspaceStatus } from './workspaceCardHelpers';

export const UNASSIGNED_KEY = '__unassigned__';
export const UNASSIGNED_TITLE = '未关联项目'; // i18n-todo: replace with i18n key when i18n framework is added

export interface ProjectSectionStats {
  running: number;
  needs_review: number;
  attention: number;
}

export interface ProjectSection {
  key: string;
  title: string;
  workspaces: Workspace[];
  stats: ProjectSectionStats;
  latestUpdatedAt: string;
}

export interface GroupWorkspacesOptions {
  /**
   * Optional unfiltered workspace list used to compute per-section status stats.
   * When omitted, stats are aggregated from `items` themselves. Pass the full
   * (pre-filter) workspaces here so badges reflect the project's overall state
   * even when the user has narrowed the visible list with a status filter.
   */
  statsSource?: Workspace[];
}

function zeroStats(): ProjectSectionStats {
  return { running: 0, needs_review: 0, attention: 0 };
}

function computeStatsByKey(items: Workspace[]): Map<string, ProjectSectionStats> {
  const map = new Map<string, ProjectSectionStats>();
  for (const ws of items) {
    const name = ws.project_name?.trim() ?? '';
    const key = name === '' ? UNASSIGNED_KEY : name;
    const stats = map.get(key) ?? zeroStats();
    const s = mapWorkspaceStatus(ws);
    if (s === 'running' || s === 'needs_review' || s === 'attention') {
      stats[s] += 1;
    }
    map.set(key, stats);
  }
  return map;
}

export function groupWorkspaces(
  items: Workspace[],
  options: GroupWorkspacesOptions = {},
): ProjectSection[] {
  if (items.length === 0) return [];

  const buckets = new Map<string, Workspace[]>();
  for (const ws of items) {
    const name = ws.project_name?.trim() ?? '';
    const key = name === '' ? UNASSIGNED_KEY : name;
    const arr = buckets.get(key) ?? [];
    arr.push(ws);
    buckets.set(key, arr);
  }

  const statsSource = options.statsSource ?? items;
  const statsByKey = computeStatsByKey(statsSource);

  const sections: ProjectSection[] = [];
  for (const [key, list] of buckets) {
    const sorted = [...list].sort((a, b) =>
      a.updated_at < b.updated_at ? 1 : a.updated_at > b.updated_at ? -1 : 0,
    );
    sections.push({
      key,
      title: key === UNASSIGNED_KEY ? UNASSIGNED_TITLE : key,
      workspaces: sorted,
      stats: statsByKey.get(key) ?? zeroStats(),
      latestUpdatedAt: sorted[0].updated_at,
    });
  }

  sections.sort((a, b) => {
    if (a.key === UNASSIGNED_KEY) return 1;
    if (b.key === UNASSIGNED_KEY) return -1;
    return a.latestUpdatedAt < b.latestUpdatedAt
      ? 1
      : a.latestUpdatedAt > b.latestUpdatedAt
        ? -1
        : 0;
  });

  return sections;
}
