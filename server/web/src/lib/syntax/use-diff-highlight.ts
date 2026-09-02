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
 * KNOWN LIMIT
 * -----------
 * A diff omits the unchanged lines between hunks, so a construct that *opens*
 * in skipped text is not seen. The reconstruction therefore starts each side
 * from the top of the first hunk rather than the top of the file, and a string
 * opened before a hunk begins can still be mis-scoped inside it. Fixing that
 * needs the full file content, which the diff endpoint does not return.
 *
 * This is strictly better than what it replaces: previously *every* multi-line
 * construct broke, now only those straddling a hunk boundary can. Within a
 * hunk — where reviewers actually read — highlighting is exact.
 */

/** Which reconstructed side a diff line belongs to, and its index there. */
interface Position {
  side: 'old' | 'new';
  index: number;
}

interface DiffSources {
  oldText: string;
  newText: string;
  positions: WeakMap<GitDiffLine, Position>;
}

function buildSources(file: GitFileDiff): DiffSources {
  const oldLines: string[] = [];
  const newLines: string[] = [];
  const positions = new WeakMap<GitDiffLine, Position>();

  for (const hunk of file.hunks ?? []) {
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

  return { oldText: oldLines.join('\n'), newText: newLines.join('\n'), positions };
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
  });
  const newMap = useSyntaxHighlight({ code: sources.newText, path: file.path, enabled });

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
