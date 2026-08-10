import { describe, it, expect } from 'vitest';
import { computeGraphLayout } from './commit-graph';
import type { GraphCommit } from '@/types/api';

// Build a GraphCommit in `git log --topo-order` shape (child before parent).
function c(hash: string, parents: string[], extra: Partial<GraphCommit> = {}): GraphCommit {
  return {
    hash,
    short_hash: hash.slice(0, 7),
    author: 'a',
    date: '2026-01-01T00:00:00Z',
    message: hash,
    parents,
    ...extra,
  };
}

const laneOf = (nodes: { commit: GraphCommit; lane: number }[], hash: string) =>
  nodes.find((n) => n.commit.hash === hash)!.lane;

describe('computeGraphLayout', () => {
  it('returns empty layout for no commits', () => {
    expect(computeGraphLayout([])).toEqual({ nodes: [], edges: [], maxLane: 0 });
  });

  it('keeps a linear history in a single lane', () => {
    const commits = [c('A', ['B']), c('B', ['C']), c('C', [])];
    const { nodes, maxLane } = computeGraphLayout(commits);
    expect(nodes.map((n) => n.lane)).toEqual([0, 0, 0]);
    expect(maxLane).toBe(0);
  });

  it('puts a side branch + merge in its own lane (true topology)', () => {
    //   M  (merge of A, B)
    //   A  (first parent, main line)
    //   B  (side branch)
    //   C  (common base)
    const commits = [c('M', ['A', 'B']), c('A', ['C']), c('B', ['C']), c('C', [])];
    const { nodes, maxLane } = computeGraphLayout(commits);
    expect(laneOf(nodes, 'M')).toBe(0); // merge commit on the main lane
    expect(laneOf(nodes, 'A')).toBe(0); // first parent inherits the merge's lane
    expect(laneOf(nodes, 'B')).toBe(1); // second (merge) parent gets its own lane
    expect(laneOf(nodes, 'C')).toBe(0); // branches converge back to lane 0
    expect(maxLane).toBeGreaterThanOrEqual(1);
  });

  it('does NOT anchor the current branch to lane 0 (no rebase-looking distortion)', () => {
    // Two independent tips sharing a base. The FIRST row (feature) takes lane 0;
    // the current branch (main) is the second tip and naturally lands in lane 1.
    // The old engine force-swapped the current branch to lane 0 mid-graph, which
    // distorted parallel branches into a rebased look. With anchoring removed it
    // must keep its natural lane.
    const commits = [
      c('T1', ['base'], { refs: ['feature'] }),
      c('T2', ['base'], { refs: ['main'], is_current: true }),
      c('base', []),
    ];
    const { nodes } = computeGraphLayout(commits);
    expect(laneOf(nodes, 'T1')).toBe(0);
    expect(laneOf(nodes, 'T2')).toBe(1); // current branch NOT yanked to lane 0
    expect(laneOf(nodes, 'base')).toBe(0);
  });

  it('keeps a through-branch in a constant lane (no compaction jog)', () => {
    // Three tips share nothing; they take lanes 0,1,2 in row order. The middle
    // branch (T1) ends early at `mid`, freeing lane 1. The right branch (T2)
    // keeps flowing past that gap. The old left-compaction would slide T2 from
    // lane 2 into the freed lane 1 mid-graph — a cosmetic one-row "kink". With
    // compaction removed, T2 must stay in lane 2 for its whole span, so its tip
    // and its parent commit share the same lane (a straight vertical line).
    const commits = [
      c('T0', ['a0']),
      c('T1', ['mid']),
      c('T2', ['b0']),
      c('mid', []),
      c('a0', ['base']),
      c('b0', []),
      c('base', []),
    ];
    const { nodes } = computeGraphLayout(commits);
    expect(laneOf(nodes, 'T2')).toBe(2);
    expect(laneOf(nodes, 'b0')).toBe(2); // not pulled left into the freed lane 1
  });

  it('emits a parent edge for every non-root commit', () => {
    const commits = [c('A', ['B']), c('B', ['C']), c('C', [])];
    const { edges } = computeGraphLayout(commits);
    // A->B and B->C (root C has no parent edge).
    const rowPairs = edges.map((e) => `${e.fromRow}->${e.toRow}`).sort();
    expect(rowPairs).toContain('0->1');
    expect(rowPairs).toContain('1->2');
  });
});
