import type { GitFileDiff, GitDiffLine } from '@/lib/hooks/use-file-diff';
import type { CodeRow, LineCell } from './types';

/**
 * Row builders: turn a file (plain text or a structured diff) into the flat
 * `CodeRow[]` the windowed `CodeSurface` renders.
 *
 * Flattening happens here, up front, for a reason: the virtualizer addresses
 * rows by index, so every collapse/expand and every unified↔split switch must
 * produce a list whose indices are authoritative. Anchor lookup
 * (`findRowForLine`) reads the same list, which is what keeps a jump correct
 * after a fold has been expanded.
 */

/** Context runs longer than this fold into an "expand N lines" button. */
export const COLLAPSE_THRESHOLD = 10;
/** Lines kept visible on each side of a folded run. */
const COLLAPSE_EDGE = 3;

/**
 * The coordinate comments anchor to: the NEW-file line number. A single
 * coordinate space keeps a stored `line_number` unambiguous — anchoring
 * deletions by old-side numbers would collide with the add/context lines that
 * share the same integer and duplicate threads. Pure deletions are therefore
 * not commentable.
 */
function anchorOf(line: GitDiffLine): number | undefined {
  return line.new_line;
}

/** Walk a hunk's lines, folding long unchanged runs into gap markers. */
function foldedLines(
  lines: GitDiffLine[],
  hunkIndex: number,
  expandedGaps: Set<string>,
  disableCollapse: boolean,
  emitLine: (line: GitDiffLine, key: string) => void,
  emitGap: (key: string, hiddenCount: number) => void,
) {
  let i = 0;
  while (i < lines.length) {
    if (lines[i].type !== 'context') {
      emitLine(lines[i], `${hunkIndex}-${i}`);
      i++;
      continue;
    }
    let j = i;
    while (j < lines.length && lines[j].type === 'context') j++;
    const run = lines.slice(i, j);
    const gapKey = `g${hunkIndex}-${i}`;

    if (disableCollapse || run.length <= COLLAPSE_THRESHOLD || expandedGaps.has(gapKey)) {
      run.forEach((l, k) => emitLine(l, `${hunkIndex}-${i}-${k}`));
    } else {
      // A run at the very start/end of a hunk has no changed line on that side
      // to give context to, so it folds entirely rather than keeping an edge.
      const head = i === 0 ? [] : run.slice(0, COLLAPSE_EDGE);
      const tail = j === lines.length ? [] : run.slice(run.length - COLLAPSE_EDGE);
      head.forEach((l, k) => emitLine(l, `${hunkIndex}-${i}-h${k}`));
      emitGap(gapKey, run.length - head.length - tail.length);
      tail.forEach((l, k) => emitLine(l, `${hunkIndex}-${i}-t${k}`));
    }
    i = j;
  }
}

/** Unified diff rows: hunk headers, folded gaps, and one row per line. */
export function buildUnifiedRows(
  file: GitFileDiff,
  expandedGaps: Set<string>,
  disableCollapse = false,
): CodeRow[] {
  const rows: CodeRow[] = [];

  (file.hunks ?? []).forEach((hunk, hi) => {
    const heading = hunk.header ? ` ${hunk.header}` : '';
    rows.push({
      kind: 'hunk',
      key: `h${hi}`,
      content: `@@ -${hunk.old_start},${hunk.old_count} +${hunk.new_start},${hunk.new_count} @@${heading}`,
    });

    foldedLines(
      hunk.lines ?? [],
      hi,
      expandedGaps,
      disableCollapse,
      (line, key) => {
        const anchor = anchorOf(line);
        rows.push({
          kind: 'line',
          key,
          // Unified: old number, new number, then the code. The new-side gutter
          // comes last, so it is the one that carries the "+" affordance.
          cells: [
            {
              side: 'unified',
              line,
              gutters: [line.old_line, line.new_line],
              anchor,
            },
          ],
          anchor,
        });
      },
      (key, hiddenCount) => rows.push({ kind: 'gap', key, hiddenCount }),
    );
  });

  return rows;
}

const EMPTY_CELL = (side: 'old' | 'new'): LineCell => ({
  side,
  line: null,
  gutters: [undefined],
});

/**
 * Split (side-by-side) rows. Consecutive delete/add runs are zipped into
 * left/right pairs so a modification shows old and new on the same row; the
 * shorter side is padded. Both halves live in ONE row element, which is what
 * guarantees left/right alignment — each side cannot drift to its own height
 * because there is only one height to measure.
 */
export function buildSplitRows(
  file: GitFileDiff,
  expandedGaps: Set<string>,
  disableCollapse = false,
): CodeRow[] {
  const rows: CodeRow[] = [];
  let dels: GitDiffLine[] = [];
  let adds: GitDiffLine[] = [];
  let seq = 0;

  const flushPairs = () => {
    const max = Math.max(dels.length, adds.length);
    for (let k = 0; k < max; k++) {
      const left = dels[k];
      const right = adds[k];
      const anchor = right ? anchorOf(right) : undefined;
      rows.push({
        kind: 'line',
        key: `p${seq++}`,
        cells: [
          left
            ? { side: 'old', line: left, gutters: [left.old_line] }
            : EMPTY_CELL('old'),
          right
            ? { side: 'new', line: right, gutters: [right.new_line], anchor }
            : EMPTY_CELL('new'),
        ],
        anchor,
      });
    }
    dels = [];
    adds = [];
  };

  (file.hunks ?? []).forEach((hunk, hi) => {
    const heading = hunk.header ? ` ${hunk.header}` : '';
    rows.push({
      kind: 'hunk',
      key: `h${hi}`,
      content: `@@ -${hunk.old_start},${hunk.old_count} +${hunk.new_start},${hunk.new_count} @@${heading}`,
    });

    foldedLines(
      hunk.lines ?? [],
      hi,
      expandedGaps,
      disableCollapse,
      (line) => {
        if (line.type === 'delete') {
          dels.push(line);
          return;
        }
        if (line.type === 'add') {
          adds.push(line);
          return;
        }
        // A context line closes any pending modification block and then shows
        // the same text on both sides.
        flushPairs();
        const anchor = anchorOf(line);
        rows.push({
          kind: 'line',
          key: `c${seq++}`,
          cells: [
            { side: 'old', line, gutters: [line.old_line] },
            { side: 'new', line, gutters: [line.new_line], anchor },
          ],
          anchor,
        });
      },
      (key, hiddenCount) => {
        flushPairs();
        rows.push({ kind: 'gap', key, hiddenCount });
      },
    );
    flushPairs();
  });

  return rows;
}

/**
 * Plain full-file rows — every line is `context` and carries the same number on
 * both sides, so the file view and the diff view share one renderer contract.
 */
export function buildFileRows(lines: string[]): CodeRow[] {
  return lines.map((content, i) => {
    const n = i + 1;
    const line = { type: 'context' as const, content, old_line: n, new_line: n };
    return {
      kind: 'line' as const,
      key: `l${n}`,
      cells: [{ side: 'unified' as const, line, gutters: [n], anchor: n }],
      anchor: n,
    };
  });
}

/**
 * Row index for a NEW-side line number, or -1. Anchor jumps go through this so
 * they address the list actually being rendered — a folded line genuinely has
 * no row, and returning -1 (rather than a guessed index) keeps the caller from
 * scrolling somewhere arbitrary.
 */
export function findRowForLine(rows: CodeRow[], lineNumber: number): number {
  return rows.findIndex(
    (r) => r.kind === 'line' && r.cells.some((c) => c.line?.new_line === lineNumber),
  );
}
