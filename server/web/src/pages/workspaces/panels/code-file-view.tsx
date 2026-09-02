import { useEffect, useMemo, useState } from 'react';
import type { WorkspaceComment } from '@/types/api';
import { CommentThread, type CommentApi } from './diff-comments';
import { CodeSurface, buildFileRows, type CodeLineRenderer } from './code-surface';

// How long the jumped-to line stays highlighted. Long enough to catch the eye
// after the scroll settles, short enough not to look like a selection.
const JUMP_FLASH_MS = 1600;

interface CodeFileViewProps {
  /** Raw file text. */
  content: string;
  /** Repo (worktree) + path, used only for the comment thread's line metadata. */
  repoName: string;
  filePath: string;
  /** Existing review comments for this file (anchored by line_number). */
  comments?: WorkspaceComment[];
  onQueueComment?: (line: number, content: string) => Promise<void>;
  onSendComment?: (line: number, content: string) => Promise<void>;
  /**
   * 1-based line to scroll to and flash on mount — set when the file was opened
   * from a content-search hit. Resolved to a row index and handed to the
   * virtualizer's `scrollToIndex`, so the target does not need to be mounted
   * (or even near the viewport) for the jump to land.
   */
  jumpToLine?: number;
}

/**
 * CodeFileView renders a file's FULL content as plain, line-numbered,
 * syntax-highlighted code (no diff chrome) with the same per-line comment
 * queue/send affordance as the diff viewer.
 *
 * Rendering runs through the shared `CodeSurface`, so only on-screen lines are
 * mounted and tokenized. That removed the former `MAX_HIGHLIGHT_LINES = 5000`
 * bail-out, which degraded backwards: past the limit a file lost its
 * highlighting (worst readability) yet still mounted every line (worst
 * performance).
 */
export function CodeFileView({
  content,
  repoName,
  filePath,
  comments,
  onQueueComment,
  onSendComment,
  jumpToLine,
}: CodeFileViewProps) {
  const [activeLine, setActiveLine] = useState<number | null>(null);
  // The jump flash is derived, not stored: `expiredJump` records which jump has
  // already flashed, and the flash is on whenever the current jump isn't it.
  // Deriving it this way keeps the effect free of a synchronous setState (which
  // would cascade a render on every open).
  const [expiredJump, setExpiredJump] = useState<string | null>(null);

  // Normalize CRLF/CR so highlighting and rendering never carry stray \r.
  const lines = useMemo(() => content.replace(/\r\n?/g, '\n').split('\n'), [content]);
  const rows = useMemo(() => buildFileRows(lines), [lines]);

  const jumpValid = !!jumpToLine && jumpToLine >= 1 && jumpToLine <= lines.length;
  const jumpKey = jumpValid ? `${filePath}:${jumpToLine}` : null;
  const flashLine = jumpKey && expiredJump !== jumpKey ? jumpToLine : null;

  useEffect(() => {
    if (!jumpKey) return;
    const timer = setTimeout(() => setExpiredJump(jumpKey), JUMP_FLASH_MS);
    return () => clearTimeout(timer);
  }, [jumpKey]);

  const byLine = useMemo(() => {
    const m = new Map<number, WorkspaceComment[]>();
    for (const c of comments ?? []) {
      if (c.line_number == null) continue;
      const arr = m.get(c.line_number) ?? [];
      arr.push(c);
      m.set(c.line_number, arr);
    }
    return m;
  }, [comments]);

  const commentApi: CommentApi | null =
    onQueueComment && onSendComment
      ? {
          repoName,
          filePath,
          byLine,
          activeLine,
          setActiveLine,
          onQueue: onQueueComment,
          onSend: onSendComment,
        }
      : null;

  const renderer: CodeLineRenderer = {
    attachment: commentApi
      ? (anchor) => <CommentThread anchor={anchor} api={commentApi} />
      : undefined,
    gutterAction: commentApi
      ? (cell) => (cell.anchor != null ? () => setActiveLine(cell.anchor!) : undefined)
      : undefined,
    rowClassName: (row) =>
      flashLine != null && row.kind === 'line' && row.anchor === flashLine
        ? 'bg-brand-soft transition-colors duration-500'
        : undefined,
  };

  // A plain file is all context: row index is line number − 1, no lookup needed.
  const scrollToRow = jumpValid ? jumpToLine! - 1 : undefined;

  return (
    <CodeSurface
      rows={rows}
      renderer={renderer}
      scrollToRow={scrollToRow}
      scrollToKey={jumpKey}
      showSigns={false}
    />
  );
}
