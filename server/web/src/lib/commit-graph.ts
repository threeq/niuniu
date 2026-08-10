// Commit-graph layout engine (SourceTree-like).
//
// Given commits in `git log --topo-order [--all]` order (child-before-parent),
// assign each commit a horizontal lane and emit the edges between a commit and
// its parents, preserving the TRUE DAG topology.
//
// Design notes:
//   - Lanes are assigned purely by first-parent continuity: a commit inherits
//     the lane of whatever pipe already points at it; merge parents get fresh
//     lanes; freed lanes are compacted leftward. There is intentionally NO
//     "anchor the current branch to lane 0" step — that mid-graph lane swap was
//     what made parallel branches look rebased/linearized. SourceTree does not
//     do it; lanes flow naturally from the tips.
//   - This is a pure function (commits -> layout), unit-tested in
//     commit-graph.test.ts, so topology correctness is verifiable without a
//     browser.

import type { GraphCommit } from '@/types/api';

export const BRANCH_COLORS = [
  '#4fc3f7', // light blue
  '#81c784', // green
  '#ffb74d', // orange
  '#e57373', // red
  '#ba68c8', // purple
  '#4dd0e1', // cyan
  '#fff176', // yellow
  '#f06292', // pink
  '#aed581', // light green
  '#7986cb', // indigo
];

type PipeKind = 'STARTS' | 'CONTINUES' | 'TERMINATES';

interface Pipe {
  fromHash: string;
  toHash: string;
  fromPos: number;
  toPos: number;
  kind: PipeKind;
  colorKey: string;
}

export interface GraphNode {
  commit: GraphCommit;
  lane: number;
  color: string;
}

export interface GraphEdge {
  fromRow: number;
  fromLane: number;
  toRow: number;
  toLane: number;
  color: string;
}

export interface GraphLayout {
  nodes: GraphNode[];
  edges: GraphEdge[];
  maxLane: number;
}

function getNextPipes(
  prevPipes: Pipe[],
  commit: GraphCommit,
  getColorKey: (commit: GraphCommit, inheritedKey: string | null) => string,
): Pipe[] {
  // Filter out pipes that terminated in the previous row
  const currentPipes = prevPipes.filter((p) => p.kind !== 'TERMINATES');

  // Compute maxPos from active pipes only (exclude TERMINATES)
  let maxPos = 0;
  for (const pipe of currentPipes) {
    if (pipe.toPos > maxPos) maxPos = pipe.toPos;
  }

  // Find commit position: where a pipe is pointing to this commit
  let pos = maxPos + 1;
  let inheritedColorKey: string | null = null;
  for (const pipe of currentPipes) {
    if (pipe.toHash === commit.hash) {
      pos = pipe.toPos;
      inheritedColorKey = pipe.colorKey;
      break;
    }
  }

  const colorKey = getColorKey(commit, inheritedColorKey);
  const newPipes: Pipe[] = [];

  // Occupancy tracking
  const takenSpots = new Set<number>();
  const traversedSpots = new Set<number>();
  const traversedSpotsForContinuing = new Set<number>();

  for (const pipe of currentPipes) {
    if (pipe.toHash !== commit.hash) {
      traversedSpotsForContinuing.add(pipe.toPos);
    }
  }

  // Keep a continuing pipe in its OWN lane so through-branches render as
  // straight vertical lines. Only relocate when that lane is already occupied
  // by another pipe's endpoint (a true collision) — being merely crossed by a
  // terminate/merge route (traversedSpots) is allowed and must NOT trigger a
  // sideways jog. Dropping the old left-compaction here is what removes the
  // cosmetic one-row "kink" on long-running branches.
  const keepLaneOrRelocate = (preferredPos: number): number => {
    if (!takenSpots.has(preferredPos)) return preferredPos;
    let i = 0;
    while (takenSpots.has(i)) i++;
    return i;
  };

  const getNextAvailableForNew = (): number => {
    let i = 0;
    while (takenSpots.has(i) || traversedSpotsForContinuing.has(i)) i++;
    return i;
  };

  const traverse = (from: number, to: number) => {
    const left = Math.min(from, to);
    const right = Math.max(from, to);
    for (let i = left; i <= right; i++) {
      traversedSpots.add(i);
    }
    takenSpots.add(to);
  };

  // Pass A: Primary parent — STARTS pipe (inherits this commit's lane)
  const firstParent = commit.parents?.[0] ?? null;
  if (firstParent) {
    newPipes.push({
      fromPos: pos,
      toPos: pos,
      fromHash: commit.hash,
      toHash: firstParent,
      kind: 'STARTS',
      colorKey,
    });
  }

  // Pass B: Terminating pipes (other branches whose tip is this commit — a merge point)
  for (const pipe of currentPipes) {
    if (pipe.toHash === commit.hash) {
      newPipes.push({
        fromPos: pipe.toPos,
        toPos: pos,
        fromHash: pipe.fromHash,
        toHash: pipe.toHash,
        kind: 'TERMINATES',
        colorKey: pipe.colorKey,
      });
      traverse(pipe.toPos, pos);
    }
  }

  // Pass C: Left continuing pipes (toPos < commit pos), sorted by toPos for
  // stability. Each stays in its own lane (no compaction) → straight line.
  const leftContinuing = currentPipes
    .filter((p) => p.toHash !== commit.hash && p.toPos < pos)
    .sort((a, b) => a.toPos - b.toPos);
  for (const pipe of leftContinuing) {
    const keptPos = keepLaneOrRelocate(pipe.toPos);
    newPipes.push({
      fromPos: pipe.toPos,
      toPos: keptPos,
      fromHash: pipe.fromHash,
      toHash: pipe.toHash,
      kind: 'CONTINUES',
      colorKey: pipe.colorKey,
    });
    traverse(pipe.toPos, keptPos);
  }

  // Pass D: Merge parents (parents[1:]) — each gets a fresh lane
  if (commit.parents && commit.parents.length > 1) {
    for (let i = 1; i < commit.parents.length; i++) {
      const parentHash = commit.parents[i];
      const availablePos = getNextAvailableForNew();
      newPipes.push({
        fromPos: pos,
        toPos: availablePos,
        fromHash: commit.hash,
        toHash: parentHash,
        kind: 'STARTS',
        colorKey: `merge-${parentHash.slice(0, 8)}`,
      });
      takenSpots.add(availablePos);
    }
  }

  // Pass E: Right continuing pipes (toPos > commit pos). Keep each in its own
  // lane (no left compaction) so the line stays straight; only relocate on a
  // true endpoint collision.
  const rightContinuing = currentPipes
    .filter((p) => p.toHash !== commit.hash && p.toPos > pos)
    .sort((a, b) => a.toPos - b.toPos);
  for (const pipe of rightContinuing) {
    const keptPos = keepLaneOrRelocate(pipe.toPos);
    newPipes.push({
      fromPos: pipe.toPos,
      toPos: keptPos,
      fromHash: pipe.fromHash,
      toHash: pipe.toHash,
      kind: 'CONTINUES',
      colorKey: pipe.colorKey,
    });
    traverse(pipe.toPos, keptPos);
  }

  // Sort by toPos, then by kind
  const kindOrder = { TERMINATES: 0, STARTS: 1, CONTINUES: 2 } as const;
  newPipes.sort((a, b) =>
    a.toPos !== b.toPos ? a.toPos - b.toPos : kindOrder[a.kind] - kindOrder[b.kind],
  );

  return newPipes;
}

export function computeGraphLayout(commits: GraphCommit[]): GraphLayout {
  if (commits.length === 0) {
    return { nodes: [], edges: [], maxLane: 0 };
  }

  const hashToRow = new Map<string, number>();
  commits.forEach((c, i) => hashToRow.set(c.hash, i));

  // Color management
  const branchColorMap = new Map<string, string>();
  let colorIdx = 0;
  const getColor = (key: string): string => {
    if (!branchColorMap.has(key)) {
      branchColorMap.set(key, BRANCH_COLORS[colorIdx % BRANCH_COLORS.length]);
      colorIdx++;
    }
    return branchColorMap.get(key)!;
  };

  // Sort refs by priority: local > remote (so a branch keeps a stable color key)
  const getColorKey = (commit: GraphCommit, inheritedKey: string | null): string => {
    if (commit.refs && commit.refs.length > 0) {
      if (commit.refs.length > 1) {
        const sorted = [...commit.refs].sort((a, b) => {
          const aIsRemote = a.startsWith('origin/') ? 1 : 0;
          const bIsRemote = b.startsWith('origin/') ? 1 : 0;
          return aIsRemote - bIsRemote;
        });
        return sorted[0];
      }
      return commit.refs[0];
    }
    if (inheritedKey && inheritedKey !== '__start__') return inheritedKey;
    return `orphan-${commit.hash.slice(0, 8)}`;
  };

  // Generate pipe sets for each row. Lanes flow naturally from the first tip;
  // NO current-branch anchoring (that swap distorted true topology).
  let pipes: Pipe[] = [
    { fromPos: 0, toPos: 0, fromHash: '__START__', toHash: commits[0].hash, kind: 'STARTS', colorKey: '__start__' },
  ];
  const pipeSets: Pipe[][] = [];

  for (const commit of commits) {
    pipes = getNextPipes(pipes, commit, getColorKey);
    pipeSets.push(pipes.map((p) => ({ ...p })));
  }

  // === Two-pass conversion to GraphNode[] + GraphEdge[] ===

  // Pass 1: Build nodes and commitLaneAtRow map
  const nodes: GraphNode[] = [];
  const commitLaneAtRow = new Map<string, number>();
  let maxLane = 0;

  for (let rowIdx = 0; rowIdx < pipeSets.length; rowIdx++) {
    const pipeSet = pipeSets[rowIdx];
    const commit = commits[rowIdx];

    let lane = 0;
    let nodeColorKey = '';
    for (const pipe of pipeSet) {
      if (pipe.kind === 'STARTS' && pipe.fromHash === commit.hash) {
        lane = pipe.fromPos;
        nodeColorKey = pipe.colorKey;
        break;
      }
    }
    if (!nodeColorKey) {
      for (const pipe of pipeSet) {
        if (pipe.kind === 'TERMINATES' && pipe.toHash === commit.hash) {
          lane = pipe.toPos;
          nodeColorKey = pipe.colorKey;
          break;
        }
      }
    }

    commitLaneAtRow.set(commit.hash, lane);
    nodes.push({ commit, lane, color: getColor(nodeColorKey || `fallback-${rowIdx}`) });

    for (const pipe of pipeSet) {
      const right = Math.max(pipe.fromPos, pipe.toPos);
      if (right > maxLane) maxLane = right;
    }
  }

  // Pass 2: Generate edges using actual target lanes
  const edges: GraphEdge[] = [];

  for (let rowIdx = 0; rowIdx < pipeSets.length; rowIdx++) {
    const pipeSet = pipeSets[rowIdx];

    for (const pipe of pipeSet) {
      if (pipe.kind === 'TERMINATES') continue;

      if (pipe.kind === 'STARTS') {
        const toRow = hashToRow.get(pipe.toHash);
        const toLane =
          toRow !== undefined ? commitLaneAtRow.get(pipe.toHash) ?? pipe.toPos : pipe.toPos;
        edges.push({
          fromRow: rowIdx,
          fromLane: pipe.fromPos,
          toRow: toRow !== undefined ? toRow : commits.length,
          toLane,
          color: getColor(pipe.colorKey),
        });
      } else if (pipe.kind === 'CONTINUES' && pipe.fromPos !== pipe.toPos) {
        edges.push({
          fromRow: rowIdx,
          fromLane: pipe.fromPos,
          toRow: rowIdx + 1,
          toLane: pipe.toPos,
          color: getColor(pipe.colorKey),
        });
      }
    }
  }

  return { nodes, edges, maxLane };
}
