import { groupWorkspaces, UNASSIGNED_KEY, UNASSIGNED_TITLE } from '../groupWorkspaces';
import type { Workspace } from '../../api/types';

function ws(overrides: Partial<Workspace>): Workspace {
  return {
    id: '1',
    name: 'ws',
    path: '/tmp',
    status: 'created',
    created_at: '2026-05-01T00:00:00Z',
    updated_at: '2026-05-01T00:00:00Z',
    ...overrides,
  };
}

describe('groupWorkspaces', () => {
  it('returns [] for empty input', () => {
    expect(groupWorkspaces([])).toEqual([]);
  });

  it('groups by project_name and sorts items inside each group by updated_at desc', () => {
    const out = groupWorkspaces([
      ws({ id: '1', project_name: 'P1', updated_at: '2026-05-01T00:00:00Z' }),
      ws({ id: '2', project_name: 'P1', updated_at: '2026-05-03T00:00:00Z' }),
      ws({ id: '3', project_name: 'P1', updated_at: '2026-05-02T00:00:00Z' }),
    ]);
    expect(out).toHaveLength(1);
    expect(out[0].key).toBe('P1');
    expect(out[0].title).toBe('P1');
    expect(out[0].workspaces.map(w => w.id)).toEqual(['2', '3', '1']);
    expect(out[0].latestUpdatedAt).toBe('2026-05-03T00:00:00Z');
  });

  it('sorts groups by latestUpdatedAt desc; pins unassigned to last', () => {
    const out = groupWorkspaces([
      ws({ id: 'a', project_name: 'Old',    updated_at: '2026-04-01T00:00:00Z' }),
      ws({ id: 'b', project_name: 'New',    updated_at: '2026-05-04T00:00:00Z' }),
      ws({ id: 'c', project_name: undefined, updated_at: '2026-05-10T00:00:00Z' }),
      ws({ id: 'd', project_name: 'Mid',    updated_at: '2026-04-20T00:00:00Z' }),
    ]);
    expect(out.map(s => s.key)).toEqual(['New', 'Mid', 'Old', UNASSIGNED_KEY]);
    expect(out[3].title).toBe(UNASSIGNED_TITLE);
  });

  it('treats empty-string and missing project_name the same', () => {
    const out = groupWorkspaces([
      ws({ id: '1', project_name: '' }),
      ws({ id: '2', project_name: undefined }),
    ]);
    expect(out).toHaveLength(1);
    expect(out[0].key).toBe(UNASSIGNED_KEY);
    expect(out[0].workspaces).toHaveLength(2);
  });

  it('aggregates stats by mapped status', () => {
    const out = groupWorkspaces([
      ws({ id: '1', project_name: 'P', status: 'running' }),
      ws({ id: '2', project_name: 'P', status: 'running' }),
      ws({ id: '3', project_name: 'P', status: 'needs_review' }),
      ws({ id: '4', project_name: 'P', status: 'attention' }),
      ws({ id: '5', project_name: 'P', status: 'completed' }),
    ]);
    expect(out[0].stats).toEqual({
      running: 2,
      needs_review: 1,
      attention: 1,
    });
  });

  it('aggregates stats from statsSource when provided (global vs filtered scope)', () => {
    // filtered = only running workspaces
    const filtered = [
      ws({ id: '1', project_name: 'P', status: 'running' }),
    ];
    // unfiltered = all workspaces (same project has 1 running + 2 needs_review)
    const all = [
      ws({ id: '1', project_name: 'P', status: 'running' }),
      ws({ id: '2', project_name: 'P', status: 'needs_review' }),
      ws({ id: '3', project_name: 'P', status: 'needs_review' }),
    ];
    const out = groupWorkspaces(filtered, { statsSource: all });
    expect(out[0].workspaces).toHaveLength(1);          // visible items reflect filter
    expect(out[0].stats).toEqual({                      // stats reflect full project
      running: 1,
      needs_review: 2,
      attention: 0,
    });
  });

  it('trims project_name and merges variants; whitespace-only goes to unassigned', () => {
    const out = groupWorkspaces([
      ws({ id: '1', project_name: 'P1' }),
      ws({ id: '2', project_name: '  P1  ' }),
      ws({ id: '3', project_name: '   ' }),
    ]);
    expect(out).toHaveLength(2);
    expect(out[0].key).toBe('P1');
    expect(out[0].workspaces).toHaveLength(2);
    expect(out[1].key).toBe(UNASSIGNED_KEY);
    expect(out[1].workspaces).toHaveLength(1);
  });
});
