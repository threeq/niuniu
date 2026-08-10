import { Fragment, useMemo, useState } from 'react';
import { highlightCode } from '@/lib/syntax-highlight';
import type { WorkspaceComment } from '@/types/api';
import { Gutter, CommentRow, type CommentApi } from './diff-comments';

// Beyond this, skip per-line tokenization to keep opening large files snappy.
const MAX_HIGHLIGHT_LINES = 5000;
// Keeps a blank line's table row at full height.
const ZERO_WIDTH_SPACE = String.fromCharCode(0x200b);

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
}: CodeFileViewProps) {
  const [activeLine, setActiveLine] = useState<number | null>(null);

  // Normalize CRLF/CR so highlighting and rendering never carry stray \r.
  const lines = useMemo(() => content.replace(/\r\n?/g, '\n').split('\n'), [content]);
  const highlight = lines.length <= MAX_HIGHLIGHT_LINES;

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
            return (
              <Fragment key={i}>
                <tr className="group/line">
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
