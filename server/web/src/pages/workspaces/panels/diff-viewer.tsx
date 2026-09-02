import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Package, ChevronRight, FileCode2 } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { GitFileDiff, GitDiffLine } from '@/lib/hooks/use-file-diff';
import type { CommentAnchorContext, WorkspaceComment } from '@/types/api';
import { useDiffHighlight, renderTokens } from '@/lib/syntax';
import { effectiveLine } from '@/lib/hooks/use-workspace-comments';
import {
  CommentThread,
  OutdatedCommentList,
  type CommentApi,
  type ComposeTarget,
} from './diff-comments';
import {
  CodeSurface,
  buildUnifiedRows,
  buildSplitRows,
  type CodeLineRenderer,
} from './code-surface';

export type ViewMode = 'unified' | 'split';

const STATUS_META: Record<string, { letter: string; badge: string }> = {
  added: { letter: 'A', badge: 'bg-success text-success-foreground' },
  untracked: { letter: 'A', badge: 'bg-success text-success-foreground' },
  modified: { letter: 'M', badge: 'bg-warning text-warning-foreground' },
  deleted: { letter: 'D', badge: 'bg-destructive text-destructive-foreground' },
  renamed: { letter: 'R', badge: 'bg-info text-info-foreground' },
  copied: { letter: 'C', badge: 'bg-info text-info-foreground' },
};

interface DiffViewerProps {
  fileDiff: GitFileDiff;
  repoName: string;
  /** Unified vs split — owned by the changes-panel toolbar. */
  mode: ViewMode;
  /** Optional VS Code fallback for binary diffs. */
  onOpenExternal?: () => void;
  /** Existing review comments for this file (positioned by their anchor). */
  comments?: WorkspaceComment[];
  /** Queue a comment on a line (persist only). */
  onQueueComment?: (target: ComposeTarget, content: string) => Promise<void>;
  /** Send a comment on a line directly to the agent. */
  onSendComment?: (target: ComposeTarget, content: string) => Promise<void>;
  /** Set/clear a comment's review verdict. */
  onSetResolved?: (commentId: number, resolved: boolean) => Promise<void>;
  /** Render every line without folding long unchanged runs (full-file view). */
  disableCollapse?: boolean;
}

/** How many neighbours each side of an old-side snapshot, matching the server. */
const OLD_CONTEXT_RADIUS = 3;

/**
 * Snapshot the OLD side of the diff around a deleted line.
 *
 * The old side does not exist in the working tree, so the server cannot capture
 * this itself — only the client holding the rendered diff can. Without it the
 * comment reaches the backend with no anchor at all and is reported outdated
 * from the moment it is created, which would make deleted-line comments
 * technically possible but useless.
 */
function captureOldContext(file: GitFileDiff, oldLine: number): CommentAnchorContext | undefined {
  // Reconstruct the old side of the file from the hunks: every line that exists
  // before the change (context + delete), keyed by its old line number.
  const byOldLine = new Map<number, string>();
  for (const hunk of file.hunks ?? []) {
    for (const l of hunk.lines ?? []) {
      if (l.old_line != null) byOldLine.set(l.old_line, l.content);
    }
  }
  const line = byOldLine.get(oldLine);
  if (line == null) return undefined;

  const before: string[] = [];
  for (let n = oldLine - OLD_CONTEXT_RADIUS; n < oldLine; n++) {
    const text = byOldLine.get(n);
    // Only contiguous neighbours are real context. A gap (folded run, hunk
    // boundary) means the intervening lines are simply not in the diff, and
    // pretending otherwise would hand the server a window that never existed.
    if (text == null) {
      before.length = 0;
      continue;
    }
    before.push(text);
  }
  const after: string[] = [];
  for (let n = oldLine + 1; n <= oldLine + OLD_CONTEXT_RADIUS; n++) {
    const text = byOldLine.get(n);
    if (text == null) break;
    after.push(text);
  }
  return { before, line, after };
}

/**
 * DiffViewer renders one file's structured diff, unified or side-by-side.
 *
 * Both modes flatten to a `CodeRow[]` and go through the shared windowed
 * `CodeSurface`, which mounts only the rows on screen. That is what retired the
 * old `MAX_DIFF_LINES = 2000` guard — whose only "protection" was a notice with
 * a *Render anyway* button, i.e. the user's options were "don't look at the
 * diff" or "hang the tab".
 *
 * Split mode is one row holding both halves rather than two independently
 * scrolled lists, so the left and right sides cannot drift out of alignment:
 * there is a single measured height per row.
 */
export function DiffViewer({
  fileDiff,
  repoName,
  mode,
  onOpenExternal,
  comments,
  onQueueComment,
  onSendComment,
  onSetResolved,
  disableCollapse = false,
}: DiffViewerProps) {
  const { t } = useTranslation('workspaces');
  const [expandedGaps, setExpandedGaps] = useState<Set<string>>(() => new Set());
  const [active, setActive] = useState<ComposeTarget | null>(null);

  const rows = useMemo(
    () =>
      mode === 'split'
        ? buildSplitRows(fileDiff, expandedGaps, disableCollapse)
        : buildUnifiedRows(fileDiff, expandedGaps, disableCollapse),
    [fileDiff, mode, expandedGaps, disableCollapse],
  );

  // Both sides are reconstructed into documents and tokenized whole, so
  // multi-line constructs inside a hunk are scoped correctly — see
  // `useDiffHighlight` for the hunk-boundary caveat. Binary diffs render a
  // placeholder instead of code, so there is nothing to highlight.
  const highlight = useDiffHighlight(fileDiff, !fileDiff.is_binary);

  // Comments split three ways by where they can honestly be shown.
  //
  //   byLine    — new-side, anchor still resolves: rendered inline at the
  //               EFFECTIVE line, which is not necessarily where it was written.
  //   byOldLine — anchored to a line this diff deletes.
  //   outdated  — no honest position left; surfaced in the banner with its
  //               snapshot instead of being pinned to unrelated code.
  const { byLine, byOldLine, outdated } = useMemo(() => {
    const byLine = new Map<number, WorkspaceComment[]>();
    const byOldLine = new Map<number, WorkspaceComment[]>();
    const outdated: WorkspaceComment[] = [];
    const push = (m: Map<number, WorkspaceComment[]>, k: number, c: WorkspaceComment) => {
      const arr = m.get(k) ?? [];
      arr.push(c);
      m.set(k, arr);
    };
    for (const c of comments ?? []) {
      // `effectiveLine` is the single authority on where a comment may render;
      // it returns null precisely when there is no honest position (outdated
      // anchor, or none recorded). Duplicating that test here would let the two
      // copies drift apart, which is how silent drift got in the first time.
      const line = effectiveLine(c);
      if (line == null) {
        // Collected rather than dropped: written feedback must never vanish
        // just because the code moved out from under it.
        outdated.push(c);
        continue;
      }
      const side = c.anchor?.side ?? c.side ?? 'new';
      push(side === 'old' ? byOldLine : byLine, line, c);
    }
    return { byLine, byOldLine, outdated };
  }, [comments]);

  const commentApi: CommentApi | null =
    onQueueComment && onSendComment
      ? {
          repoName,
          filePath: fileDiff.path,
          byLine,
          active,
          setActive,
          onQueue: onQueueComment,
          onSend: onSendComment,
          onSetResolved,
        }
      : null;

  // The old-side thread reads the same CommentApi shape but a different map, so
  // a deleted line's comments cannot leak into the new-side thread that happens
  // to share its integer.
  const oldCommentApi: CommentApi | null = commentApi ? { ...commentApi, byLine: byOldLine } : null;

  const renderer: CodeLineRenderer = {
    // `build-rows` puts the very `GitDiffLine` objects from `fileDiff` into the
    // cells, so the highlighter can key on object identity — which is what
    // keeps tokens attached to the right line across folding and the
    // unified↔split switch, both of which reorder rows but reuse the objects.
    tokenize: (line) => renderTokens(highlight(line as GitDiffLine), line.content),
    attachment: commentApi
      ? (anchor, side) =>
          side === 'old' ? (
            <CommentThread anchor={anchor} side="old" api={oldCommentApi!} />
          ) : (
            <CommentThread anchor={anchor} side="new" api={commentApi} />
          )
      : undefined,
    gutterAction: commentApi
      ? (cell) => {
          if (cell.anchor != null) {
            return () => setActive({ line: cell.anchor!, side: 'new' });
          }
          if (cell.oldAnchor != null) {
            const oldLine = cell.oldAnchor;
            return () =>
              setActive({
                line: oldLine,
                side: 'old',
                context: captureOldContext(fileDiff, oldLine),
              });
          }
          return undefined;
        }
      : undefined,
  };

  const status = fileDiff.status || 'modified';
  const meta = STATUS_META[status] ?? STATUS_META.modified;
  const dir = fileDiff.path.includes('/')
    ? fileDiff.path.slice(0, fileDiff.path.lastIndexOf('/') + 1)
    : '';
  const name = dir ? fileDiff.path.slice(dir.length) : fileDiff.path;

  // A file is "binary" only when git said so ("Binary files ... differ") — a
  // flag the backend parser now carries, so this no longer depends on the
  // client re-detecting it. A zero-hunk diff is otherwise a text file whose
  // change carried no content (a mode flip, a pure rename, an empty file) and
  // must NOT be shown as an un-previewable binary.
  const isBinary = !!fileDiff.is_binary;
  const isNoContentChange = !isBinary && (fileDiff.hunks?.length ?? 0) === 0;

  const header = (
    <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border bg-card px-3 py-2">
      <span
        className={cn(
          'flex h-5 w-5 shrink-0 items-center justify-center rounded-md text-[11px] font-bold',
          meta.badge,
        )}
      >
        {meta.letter}
      </span>
      <span className="flex items-center gap-1 rounded-md bg-muted px-2 py-0.5 text-[11px] text-muted-foreground">
        <Package className="h-3 w-3" />
        {repoName}
      </span>
      <ChevronRight className="h-3 w-3 text-muted-foreground/60" />
      <span className="truncate font-mono text-xs">
        <span className="text-muted-foreground">{dir}</span>
        <span className="font-semibold text-foreground">{name}</span>
      </span>
      <span className="ml-auto flex items-center gap-2 font-mono text-xs">
        {fileDiff.additions > 0 && <span className="text-diff-add-fg">+{fileDiff.additions}</span>}
        {fileDiff.deletions > 0 && <span className="text-diff-del-fg">−{fileDiff.deletions}</span>}
      </span>
    </div>
  );

  let body: React.ReactNode;
  if (isBinary) {
    body = (
      <Degraded
        message={t('panels.changes.diff.binary')}
        actionLabel={onOpenExternal ? t('panels.changes.diff.openInVSCode') : undefined}
        onAction={onOpenExternal}
      />
    );
  } else if (isNoContentChange) {
    // Text file with no content hunks: describe the actual change (mode/rename)
    // rather than the misleading "binary" message.
    const modeMsg =
      fileDiff.old_mode && fileDiff.new_mode && fileDiff.old_mode !== fileDiff.new_mode
        ? t('panels.changes.diff.modeChange', { old: fileDiff.old_mode, new: fileDiff.new_mode })
        : status === 'renamed' || status === 'copied'
          ? t('panels.changes.diff.renameOnly')
          : t('panels.changes.diff.noContentChange');
    body = (
      <Degraded
        message={modeMsg}
        actionLabel={onOpenExternal ? t('panels.changes.diff.openInVSCode') : undefined}
        onAction={onOpenExternal}
      />
    );
  } else {
    body = (
      <CodeSurface
        rows={rows}
        renderer={renderer}
        onExpandGap={(k) => setExpandedGaps((s) => new Set(s).add(k))}
        gapLabel={(count) => t('panels.changes.diff.expandLines', { count })}
      />
    );
  }

  // Fill the parent and scroll the body on BOTH axes in a single fixed-height
  // container, so the horizontal scrollbar stays pinned to the bottom of the
  // pane (instead of the bottom of a tall diff). The header is a static top bar.
  return (
    <div className="flex h-full min-h-0 flex-col bg-card">
      {header}
      {/* Comments whose anchor no longer resolves sit ABOVE the diff, not inside
          it — there is no line left to attach them to, and hiding them would
          silently discard review feedback. */}
      {commentApi && <OutdatedCommentList comments={outdated} api={commentApi} />}
      <div className="min-h-0 flex-1">{body}</div>
    </div>
  );
}

function Degraded({
  message,
  actionLabel,
  onAction,
}: {
  message: string;
  actionLabel?: string;
  onAction?: () => void;
}) {
  return (
    <div className="flex flex-col items-center gap-2 px-4 py-8 text-center">
      <FileCode2 className="h-6 w-6 text-muted-foreground/50" />
      <p className="text-xs text-muted-foreground">{message}</p>
      {actionLabel && onAction && (
        <button
          onClick={onAction}
          className="rounded-md border border-border px-2.5 py-1 text-xs text-foreground hover:bg-accent"
        >
          {actionLabel}
        </button>
      )}
    </div>
  );
}
