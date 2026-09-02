import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import { highlightCode } from '@/lib/syntax-highlight';
import { cn } from '@/lib/utils';
import type { WorkspaceComment } from '@/types/api';
import { Gutter, CommentRow, type CommentApi } from './diff-comments';

// Beyond this, skip per-line tokenization to keep opening large files snappy.
const MAX_HIGHLIGHT_LINES = 5000;
// Keeps a blank line's table row at full height.
const ZERO_WIDTH_SPACE = String.fromCharCode(0x200b);
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
   * from a content-search hit.
   *
   * This is the pre-virtualization anchor approach (#685): every line has a DOM
   * node, so `scrollIntoView` on its row is enough. Once #687 virtualizes the
   * list this becomes a `scrollToIndex` call, and the row ref goes away.
   */
  jumpToLine?: number;
}

/**
 * CodeFileView renders a file's FULL content as plain, line-numbered,
 * syntax-highlighted code (no diff chrome) with the same per-line comment
 * queue/send affordance as the diff viewer. It fills its parent and scrolls on
 * both axes in a single container, so the horizontal scrollbar stays pinned to
 * the pane bottom.
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
  const jumpRowRef = useRef<HTMLTableRowElement | null>(null);

  // Normalize CRLF/CR so highlighting and rendering never carry stray \r.
  const lines = useMemo(() => content.replace(/\r\n?/g, '\n').split('\n'), [content]);
  const highlight = lines.length <= MAX_HIGHLIGHT_LINES;

  const jumpValid = !!jumpToLine && jumpToLine >= 1 && jumpToLine <= lines.length;
  const jumpKey = jumpValid ? `${filePath}:${jumpToLine}` : null;
  const flashLine = jumpKey && expiredJump !== jumpKey ? jumpToLine : null;

  // Scroll the requested line into view once the rows exist, then let the flash
  // expire. Keyed on the file too, so a second hit in a DIFFERENT file re-jumps.
  useEffect(() => {
    if (!jumpKey) return;
    // rAF lets the browser lay the table out before we measure and scroll.
    const raf = requestAnimationFrame(() => {
      jumpRowRef.current?.scrollIntoView({ block: 'center', behavior: 'auto' });
    });
    const timer = setTimeout(() => setExpiredJump(jumpKey), JUMP_FLASH_MS);
    return () => {
      cancelAnimationFrame(raf);
      clearTimeout(timer);
    };
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
      ? { repoName, filePath, byLine, activeLine, setActiveLine, onQueue: onQueueComment, onSend: onSendComment }
      : null;

  return (
    <div className="h-full overflow-auto bg-card">
      <table className="w-full border-collapse">
        <tbody>
          {lines.map((line, i) => {
            const n = i + 1;
            const nodes = highlight
              ? line.length > 0
                ? highlightCode(line)
                : ZERO_WIDTH_SPACE
              : line || ZERO_WIDTH_SPACE;
            const addOn = commentApi ? () => setActiveLine(n) : undefined;
            const isJumpTarget = jumpValid && jumpToLine === n;
            return (
              <Fragment key={i}>
                <tr
                  ref={isJumpTarget ? jumpRowRef : undefined}
                  className={cn(
                    'group/line',
                    flashLine === n && 'bg-brand-soft transition-colors duration-500',
                  )}
                >
                  <Gutter value={n} onAdd={addOn} />
                  <td className="whitespace-pre px-3 align-top font-mono text-[12.5px] leading-5 text-foreground">
                    {nodes}
                  </td>
                </tr>
                {commentApi && <CommentRow anchor={n} colSpan={2} api={commentApi} />}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
