import { useMemo } from 'react';
import type { GitFileDiff, GitDiffLine } from '@/lib/hooks/use-file-diff';
import { useSyntaxHighlight, type HighlightMap } from './use-syntax-highlight';
import type { SyntaxLine } from './tokenizer';

/**
 * Syntax highlighting for a diff.
 *
 * THE TENSION
 * -----------
 * A TextMate grammar is stateful — line 2 of a template literal is only a
 * string because line 1 opened one — but a diff is not a document. Its lines
 * are interleaved from two different files and separated by unseen gaps
 * between hunks. Highlighting each diff line in isolation is what the retired
 * regex highlighter did, and it is why a multi-line string's continuation
 * lines came out looking like code.
 *
 * THE RESOLUTION
 * --------------
 * Reconstruct the two sides as documents and tokenize each as a whole:
 *
 *   old side = context + delete lines, in order
 *   new side = context + add lines, in order
 *
 * Each reconstruction is a genuine, syntactically coherent excerpt of a real
 * file, so multi-line strings, block comments and JSX blocks tokenize with
 * their true state. Tokens are then mapped back to diff lines by object
 * identity, which survives folding and the unified↔split switch (both reuse
 * the same `GitDiffLine` objects).
 *
 * KNOWN LIMIT, AND WHY HUNKS RESET
 * ---------------------------------
 * A diff omits the unchanged lines between hunks, so the reconstruction is not
 * a real file: hunk N+1 is spliced directly onto hunk N with arbitrary text
 * missing in between. A construct left open at the end of a hunk (a `/*` whose
 * closer lives in the skipped gap) would therefore swallow every later hunk,
 * painting real code as comment — worse than the regex highlighter this
 * replaced, whose `\/\*[\s\S]*?\*&#47;` simply failed to match an unterminated
 * comment and left the following code alone.
 *
 * So each hunk restarts the grammar (`resets` below). State still flows freely
 * *within* a hunk — which is where multi-line strings and block comments
 * actually need it, and where reviewers read — but a mistake can never escape
 * the hunk that caused it. The residual limit is that a construct opened
 * before a hunk starts is not seen, so its first lines may be under-colored.
 * Under-coloring is the safe direction: code stays legible as code.
 *
 * Fixing even that needs the full file text, which the diff endpoint does not
 * return.
 */

/** Which reconstructed side a diff line belongs to, and its index there. */
interface Position {
  side: 'old' | 'new';
  index: number;
}

interface DiffSources {
  oldText: string;
  newText: string;
  /** Line index of each hunk's start, per side — the grammar restarts there. */
  oldResets: number[];
  newResets: number[];
  positions: WeakMap<GitDiffLine, Position>;
}

function buildSources(file: GitFileDiff): DiffSources {
  const oldLines: string[] = [];
  const newLines: string[] = [];
  const oldResets: number[] = [];
  const newResets: number[] = [];
  const positions = new WeakMap<GitDiffLine, Position>();

  for (const hunk of file.hunks ?? []) {
    // Each hunk begins a new segment on both sides: the text preceding it in
    // the real file is not present here, so carried grammar state would be a
    // guess rather than a fact.
    oldResets.push(oldLines.length);
    newResets.push(newLines.length);

    for (const line of hunk.lines ?? []) {
      // A context line exists on both sides but only needs one set of tokens;
      // the new side is authoritative because that is the coordinate space
      // anchors and the split view's right column already use.
      if (line.type !== 'delete') {
        positions.set(line, { side: 'new', index: newLines.length });
        newLines.push(line.content);
      } else {
        positions.set(line, { side: 'old', index: oldLines.length });
        oldLines.push(line.content);
      }
      // Deletes also occupy a slot on the old side even when recorded above as
      // context, so the old reconstruction stays contiguous.
      if (line.type === 'context') oldLines.push(line.content);
    }
  }

  return {
    oldText: oldLines.join('\n'),
    newText: newLines.join('\n'),
    oldResets,
    newResets,
    positions,
  };
}

/**
 * Tokens for one diff line, or `undefined` while a chunk is still in flight.
 * Callers render the raw content in that case.
 */
export type DiffHighlightMap = (line: GitDiffLine) => SyntaxLine | undefined;

const NONE: DiffHighlightMap = () => undefined;

/**
 * Highlight both sides of a file diff.
 *
 * Two highlight passes run concurrently (one per side); each is chunked and
 * off-thread like any other file, so a large diff colors progressively instead
 * of blocking.
 */
export function useDiffHighlight(file: GitFileDiff, enabled = true): DiffHighlightMap {
  const sources = useMemo(() => buildSources(file), [file]);

  const oldMap = useSyntaxHighlight({
    code: sources.oldText,
    path: file.old_path || file.path,
    enabled,
    resets: sources.oldResets,
  });
  const newMap = useSyntaxHighlight({
    code: sources.newText,
    path: file.path,
    enabled,
    resets: sources.newResets,
  });

  return useMemo(() => {
    if (!enabled) return NONE;
    return (line: GitDiffLine) => {
      const pos = sources.positions.get(line);
      if (!pos) return undefined;
      const map: HighlightMap = pos.side === 'old' ? oldMap : newMap;
      return map(pos.index);
    };
  }, [enabled, sources, oldMap, newMap]);
}
