import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Package, ChevronRight, FileCode2 } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { GitFileDiff } from '@/lib/hooks/use-file-diff';
import type { WorkspaceComment } from '@/types/api';
import { CommentThread, type CommentApi } from './diff-comments';
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
  /** Existing review comments for this file (anchored by line_number). */
  comments?: WorkspaceComment[];
  /** Queue a comment on a line (persist only). */
  onQueueComment?: (line: number, content: string) => Promise<void>;
  /** Send a comment on a line directly to the agent. */
  onSendComment?: (line: number, content: string) => Promise<void>;
  /** Render every line without folding long unchanged runs (full-file view). */
  disableCollapse?: boolean;
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
  disableCollapse = false,
}: DiffViewerProps) {
  const { t } = useTranslation('workspaces');
  const [expandedGaps, setExpandedGaps] = useState<Set<string>>(() => new Set());
  const [activeLine, setActiveLine] = useState<number | null>(null);

  const rows = useMemo(
    () =>
      mode === 'split'
        ? buildSplitRows(fileDiff, expandedGaps, disableCollapse)
        : buildUnifiedRows(fileDiff, expandedGaps, disableCollapse),
    [fileDiff, mode, expandedGaps, disableCollapse],
  );

  // Comments grouped by their anchored line number for inline rendering.
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
          filePath: fileDiff.path,
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
