import { useEffect, useMemo, useState } from 'react';
import type { WorkspaceComment } from '@/types/api';
import { useSyntaxHighlight, renderTokens } from '@/lib/syntax';
import { effectiveLine } from '@/lib/hooks/use-workspace-comments';
import {
  CommentThread,
  OutdatedCommentList,
  type CommentApi,
  type ComposeTarget,
} from './diff-comments';
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
  /** Existing review comments for this file (positioned by their anchor). */
  comments?: WorkspaceComment[];
  onQueueComment?: (target: ComposeTarget, content: string) => Promise<void>;
  onSendComment?: (target: ComposeTarget, content: string) => Promise<void>;
  /** Set/clear a comment's review verdict. */
  onSetResolved?: (commentId: number, resolved: boolean) => Promise<void>;
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
  onSetResolved,
  jumpToLine,
}: CodeFileViewProps) {
  const [active, setActive] = useState<ComposeTarget | null>(null);
  // The jump flash is derived, not stored: `expiredJump` records which jump has
  // already flashed, and the flash is on whenever the current jump isn't it.
  // Deriving it this way keeps the effect free of a synchronous setState (which
  // would cascade a render on every open).
  const [expiredJump, setExpiredJump] = useState<string | null>(null);

  // Normalize CRLF/CR so highlighting and rendering never carry stray \r.
  const normalized = useMemo(() => content.replace(/\r\n?/g, '\n'), [content]);
  const lines = useMemo(() => normalized.split('\n'), [normalized]);
  const rows = useMemo(() => buildFileRows(lines), [lines]);

  // A full file is one contiguous document, so the grammar sees exactly what it
  // would in an editor — multi-line strings and block comments are unambiguous
  // here (unlike in a diff, see `useDiffHighlight`).
  const highlight = useSyntaxHighlight({ code: normalized, path: filePath });

  const jumpValid = !!jumpToLine && jumpToLine >= 1 && jumpToLine <= lines.length;
  const jumpKey = jumpValid ? `${filePath}:${jumpToLine}` : null;
  const flashLine = jumpKey && expiredJump !== jumpKey ? jumpToLine : null;

  useEffect(() => {
    if (!jumpKey) return;
    const timer = setTimeout(() => setExpiredJump(jumpKey), JUMP_FLASH_MS);
    return () => clearTimeout(timer);
  }, [jumpKey]);

  // A full-file view shows the CURRENT content, so a comment belongs at its
  // effective (re-resolved) line, not the line it was written against. One whose
  // anchor is gone has no place in the listing at all and goes to the banner.
  const { byLine, outdated } = useMemo(() => {
    const byLine = new Map<number, WorkspaceComment[]>();
    const outdated: WorkspaceComment[] = [];
    for (const c of comments ?? []) {
      // An old-side comment names a line that no longer exists in the file, so
      // the full-file view has nowhere to put it. Otherwise `effectiveLine` is
      // the sole authority — null means no honest position.
      const oldSide = (c.anchor?.side ?? c.side) === 'old';
      const line = oldSide ? null : effectiveLine(c);
      if (line == null) {
        outdated.push(c);
        continue;
      }
      const arr = byLine.get(line) ?? [];
      arr.push(c);
      byLine.set(line, arr);
    }
    return { byLine, outdated };
  }, [comments]);

  const commentApi: CommentApi | null =
    onQueueComment && onSendComment
      ? {
          repoName,
          filePath,
          byLine,
          active,
          setActive,
          onQueue: onQueueComment,
          onSend: onSendComment,
          onSetResolved,
        }
      : null;

  const renderer: CodeLineRenderer = {
    // Row index is line number − 1 in a plain file, which is exactly the index
    // the highlighter keys on.
    tokenize: (line) =>
      renderTokens(
        line.new_line != null ? highlight(line.new_line - 1) : undefined,
        line.content,
      ),
    // A plain file has no deleted lines, so only the new side ever attaches.
    attachment: commentApi
      ? (anchor, side) =>
          side === 'new' ? <CommentThread anchor={anchor} side="new" api={commentApi} /> : null
      : undefined,
    gutterAction: commentApi
      ? (cell) =>
          cell.anchor != null ? () => setActive({ line: cell.anchor!, side: 'new' }) : undefined
      : undefined,
    rowClassName: (row) =>
      flashLine != null && row.kind === 'line' && row.anchor === flashLine
        ? 'bg-brand-soft transition-colors duration-500'
        : undefined,
  };

  // A plain file is all context: row index is line number − 1, no lookup needed.
  const scrollToRow = jumpValid ? jumpToLine! - 1 : undefined;

  return (
    <div className="flex h-full min-h-0 flex-col">
      {commentApi && <OutdatedCommentList comments={outdated} api={commentApi} />}
      <div className="min-h-0 flex-1">
        <CodeSurface
          rows={rows}
          renderer={renderer}
          scrollToRow={scrollToRow}
          scrollToKey={jumpKey}
          showSigns={false}
        />
      </div>
    </div>
  );
}
