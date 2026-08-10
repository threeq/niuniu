import { Fragment, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Package, ChevronRight, FileCode2, ChevronsDownUp } from 'lucide-react';
import { cn } from '@/lib/utils';
import { parseUnifiedDiff, type DiffFile, type DiffLine } from '@/lib/hooks/use-diff';
import type { GitFileDiff } from '@/lib/hooks/use-file-diff';
import { highlightCode } from '@/lib/syntax-highlight';
import type { WorkspaceComment } from '@/types/api';
import { Gutter, CommentRow, type CommentApi } from './diff-comments';

const MAX_DIFF_LINES = 2000; // beyond this we degrade to a "render anyway" notice
const COLLAPSE_THRESHOLD = 10; // context runs longer than this collapse
const COLLAPSE_EDGE = 3; // lines kept visible on each side of a collapsed run

export type ViewMode = 'unified' | 'split';

// A diff line with its resolved old/new line numbers.
interface NumberedLine {
  type: DiffLine['type'];
  content: string;
  oldNumber?: number;
  newNumber?: number;
}

// Render items: a hunk header, a collapsed gap, or a numbered line.
type RenderItem =
  | { kind: 'hunk'; key: string; content: string }
  | { kind: 'gap'; key: string; hidden: NumberedLine[] }
  | { kind: 'line'; key: string; line: NumberedLine };

const STATUS_META: Record<string, { letter: string; badge: string }> = {
  added: { letter: 'A', badge: 'bg-success text-success-foreground' },
  untracked: { letter: 'A', badge: 'bg-success text-success-foreground' },
  modified: { letter: 'M', badge: 'bg-warning text-warning-foreground' },
  deleted: { letter: 'D', badge: 'bg-destructive text-destructive-foreground' },
  renamed: { letter: 'R', badge: 'bg-info text-info-foreground' },
};

/**
 * The line number a comment anchors to: the NEW-file line number. We anchor on
 * a single coordinate space (the new side) so a comment's stored line_number is
 * unambiguous — anchoring deletes by old-line numbers would collide with
 * add/context lines that share the same integer, duplicating threads. Pure
 * deletions (no new number) are therefore not commentable.
 */
function anchorOf(line: NumberedLine): number | undefined {
  return line.newNumber;
}

/** Resolve old/new line numbers for every line in a hunk. */
function numberHunkLines(hunk: { oldStart: number; newStart: number; lines: DiffLine[] }): NumberedLine[] {
  let oldNo = hunk.oldStart;
  let newNo = hunk.newStart;
  return hunk.lines.map((line) => {
    if (line.type === 'add') return { type: 'add', content: line.content, newNumber: newNo++ };
    if (line.type === 'delete') return { type: 'delete', content: line.content, oldNumber: oldNo++ };
    return { type: 'context', content: line.content, oldNumber: oldNo++, newNumber: newNo++ };
  });
}

/** Build the flat render-item list for a file, folding long unchanged runs.
 *  `disableCollapse` renders every line (used for full-file views where the
 *  whole file is context and folding it would hide the content). */
function buildRenderItems(
  file: DiffFile,
  expandedGaps: Set<string>,
  disableCollapse = false,
): RenderItem[] {
  const items: RenderItem[] = [];

  file.hunks.forEach((hunk, hi) => {
    items.push({
      kind: 'hunk',
      key: `h${hi}`,
      content: `@@ -${hunk.oldStart},${hunk.oldLines} +${hunk.newStart},${hunk.newLines} @@`,
    });

    const lines = numberHunkLines(hunk);
    let i = 0;
    while (i < lines.length) {
      if (lines[i].type !== 'context') {
        items.push({ kind: 'line', key: `${hi}-${i}`, line: lines[i] });
        i++;
        continue;
      }
      // Gather a maximal run of context lines.
      let j = i;
      while (j < lines.length && lines[j].type === 'context') j++;
      const run = lines.slice(i, j);
      const atStart = i === 0;
      const atEnd = j === lines.length;
      const gapKey = `g${hi}-${i}`;

      if (disableCollapse || run.length <= COLLAPSE_THRESHOLD || expandedGaps.has(gapKey)) {
        run.forEach((l, k) => items.push({ kind: 'line', key: `${hi}-${i}-${k}`, line: l }));
      } else {
        const head = atStart ? [] : run.slice(0, COLLAPSE_EDGE);
        const tail = atEnd ? [] : run.slice(run.length - COLLAPSE_EDGE);
        const hidden = run.slice(head.length, run.length - tail.length);
        head.forEach((l, k) => items.push({ kind: 'line', key: `${hi}-${i}-h${k}`, line: l }));
        items.push({ kind: 'gap', key: gapKey, hidden });
        tail.forEach((l, k) => items.push({ kind: 'line', key: `${hi}-${i}-t${k}`, line: l }));
      }
      i = j;
    }
  });

  return items;
}

function CodeCell({
  line,
  className,
}: {
  line: NumberedLine;
  className?: string;
}) {
  const sign = line.type === 'add' ? '+' : line.type === 'delete' ? '−' : ' ';
  return (
    <td
      className={cn(
        'whitespace-pre px-3 align-top font-mono text-[12.5px] leading-5 text-foreground',
        line.type === 'add' && 'bg-diff-add border-l-2 border-diff-add-fg',
        line.type === 'delete' && 'bg-diff-del border-l-2 border-diff-del-fg',
        line.type === 'context' && 'border-l-2 border-transparent',
        className,
      )}
    >
      <span
        className={cn(
          'mr-2 select-none font-semibold',
          line.type === 'add' && 'text-diff-add-fg',
          line.type === 'delete' && 'text-diff-del-fg',
          line.type === 'context' && 'text-muted-foreground/40',
        )}
      >
        {sign}
      </span>
      {highlightCode(line.content)}
    </td>
  );
}

function GapRow({
  colSpan,
  onExpand,
  label,
}: {
  colSpan: number;
  onExpand: () => void;
  label: string;
}) {
  return (
    <tr>
      <td colSpan={colSpan} className="border-y border-border bg-muted/40 p-0">
        <button
          onClick={onExpand}
          className="flex w-full items-center gap-2 px-3 py-1 text-left text-[11px] text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          <ChevronsDownUp className="h-3 w-3 shrink-0" />
          {label}
        </button>
      </td>
    </tr>
  );
}

interface DiffViewerProps {
  fileDiff: GitFileDiff;
  repoName: string;
  /** Unified vs split — owned by the changes-panel toolbar. */
  mode: ViewMode;
  /** Optional VS Code fallback for binary / oversized diffs. */
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
  const [forceRender, setForceRender] = useState(false);
  const [expandedGaps, setExpandedGaps] = useState<Set<string>>(() => new Set());
  const [activeLine, setActiveLine] = useState<number | null>(null);

  const parsed: DiffFile | null = useMemo(() => {
    const files = parseUnifiedDiff(fileDiff.raw_patch || '');
    return files[0] ?? null;
  }, [fileDiff.raw_patch]);

  const totalLines = useMemo(
    () => (parsed ? parsed.hunks.reduce((n, h) => n + h.lines.length, 0) : 0),
    [parsed],
  );

  const items = useMemo(
    () => (parsed ? buildRenderItems(parsed, expandedGaps, disableCollapse) : []),
    [parsed, expandedGaps, disableCollapse],
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

  const status = fileDiff.status || 'modified';
  const meta = STATUS_META[status] ?? STATUS_META.modified;
  const dir = fileDiff.path.includes('/')
    ? fileDiff.path.slice(0, fileDiff.path.lastIndexOf('/') + 1)
    : '';
  const name = dir ? fileDiff.path.slice(dir.length) : fileDiff.path;

  // A file is "binary" only when git said so ("Binary files ... differ"). A
  // zero-hunk diff is otherwise a text file whose change carried no content —
  // a mode flip (chmod +x on a .sh) or a pure rename — and must NOT be shown
  // as an un-previewable binary.
  const isBinary = !!parsed?.isBinary;
  const isNoContentChange = !!parsed && !isBinary && parsed.hunks.length === 0;
  const isLarge = totalLines > MAX_DIFF_LINES;

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
  if (!parsed || isBinary) {
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
      parsed.oldMode && parsed.newMode
        ? t('panels.changes.diff.modeChange', { old: parsed.oldMode, new: parsed.newMode })
        : status === 'renamed'
          ? t('panels.changes.diff.renameOnly')
          : t('panels.changes.diff.noContentChange');
    body = (
      <Degraded
        message={modeMsg}
        actionLabel={onOpenExternal ? t('panels.changes.diff.openInVSCode') : undefined}
        onAction={onOpenExternal}
      />
    );
  } else if (isLarge && !forceRender) {
    body = (
      <Degraded
        message={t('panels.changes.diff.largeFile', { count: totalLines })}
        actionLabel={t('panels.changes.diff.renderAnyway')}
        onAction={() => setForceRender(true)}
      />
    );
  } else if (mode === 'unified') {
    body = (
      <UnifiedTable
        items={items}
        onExpand={(k) => setExpandedGaps((s) => new Set(s).add(k))}
        t={t}
        comments={commentApi}
      />
    );
  } else {
    body = (
      <SplitTable
        items={items}
        onExpand={(k) => setExpandedGaps((s) => new Set(s).add(k))}
        t={t}
        comments={commentApi}
      />
    );
  }

  // Fill the parent and scroll the body on BOTH axes in a single fixed-height
  // container, so the horizontal scrollbar stays pinned to the bottom of the
  // pane (instead of the bottom of a tall diff). The header is a static top bar.
  return (
    <div className="flex h-full min-h-0 flex-col bg-card">
      {header}
      <div className="min-h-0 flex-1 overflow-auto">{body}</div>
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

type TFn = ReturnType<typeof useTranslation>['t'];

function UnifiedTable({
  items,
  onExpand,
  t,
  comments,
}: {
  items: RenderItem[];
  onExpand: (key: string) => void;
  t: TFn;
  comments: CommentApi | null;
}) {
  return (
    <table className="w-full border-collapse">
      <tbody>
        {items.map((item) => {
          if (item.kind === 'hunk') {
            return (
              <tr key={item.key}>
                <td
                  colSpan={3}
                  className="border-y border-border bg-muted/60 px-3 py-1 font-mono text-[11px] text-muted-foreground"
                >
                  {item.content}
                </td>
              </tr>
            );
          }
          if (item.kind === 'gap') {
            return (
              <GapRow
                key={item.key}
                colSpan={3}
                onExpand={() => onExpand(item.key)}
                label={t('panels.changes.diff.expandLines', { count: item.hidden.length })}
              />
            );
          }
          const { line } = item;
          const tone = line.type === 'add' ? 'add' : line.type === 'delete' ? 'del' : undefined;
          const anchor = comments ? anchorOf(line) : undefined;
          const addOn = comments && anchor != null ? () => comments.setActiveLine(anchor) : undefined;
          return (
            <Fragment key={item.key}>
              <tr className="group/line">
                <Gutter value={line.oldNumber} tone={tone} />
                <Gutter value={line.newNumber} tone={tone} onAdd={addOn} />
                <CodeCell line={line} />
              </tr>
              {comments && anchor != null && (
                <CommentRow anchor={anchor} colSpan={3} api={comments} />
              )}
            </Fragment>
          );
        })}
      </tbody>
    </table>
  );
}

const EMPTY_LINE: NumberedLine = { type: 'context', content: '' };

function SplitTable({
  items,
  onExpand,
  t,
  comments,
}: {
  items: RenderItem[];
  onExpand: (key: string) => void;
  t: TFn;
  comments: CommentApi | null;
}) {
  // Pair consecutive lines into left (old) / right (new) columns.
  const rows: React.ReactNode[] = [];
  let buffer: NumberedLine[] = [];

  // After a row, emit inline comment threads for the lines it covered.
  const pushComments = (anchors: Array<number | undefined>) => {
    if (!comments) return;
    const seen = new Set<number>();
    for (const a of anchors) {
      if (a == null || seen.has(a)) continue;
      seen.add(a);
      rows.push(<CommentRow key={`cm${a}-${rows.length}`} anchor={a} colSpan={4} api={comments} />);
    }
  };

  const flush = () => {
    if (buffer.length === 0) return;
    const dels: NumberedLine[] = [];
    const adds: NumberedLine[] = [];
    const emitPaired = () => {
      const max = Math.max(dels.length, adds.length);
      for (let k = 0; k < max; k++) {
        const left = dels[k] ?? EMPTY_LINE;
        const right = adds[k] ?? EMPTY_LINE;
        // Comments anchor on the new side only (see anchorOf): the right gutter.
        const rightAdd =
          comments && right.newNumber != null
            ? () => comments.setActiveLine(right.newNumber!)
            : undefined;
        rows.push(
          <tr key={`p${rows.length}`} className="group/line">
            <Gutter
              value={left.oldNumber}
              tone={left.content || left.oldNumber ? 'del' : undefined}
            />
            <CodeCell line={left} className="border-r border-border" />
            <Gutter
              value={right.newNumber}
              tone={right.content || right.newNumber ? 'add' : undefined}
              onAdd={rightAdd}
            />
            <CodeCell line={right} />
          </tr>,
        );
        pushComments([right.newNumber]);
      }
      dels.length = 0;
      adds.length = 0;
    };
    for (const l of buffer) {
      if (l.type === 'delete') dels.push(l);
      else if (l.type === 'add') adds.push(l);
      else {
        emitPaired();
        const ctxAdd =
          comments && l.newNumber != null ? () => comments.setActiveLine(l.newNumber!) : undefined;
        rows.push(
          <tr key={`c${rows.length}`} className="group/line">
            <Gutter value={l.oldNumber} />
            <CodeCell line={l} className="border-r border-border" />
            <Gutter value={l.newNumber} onAdd={ctxAdd} />
            <CodeCell line={l} />
          </tr>,
        );
        pushComments([l.newNumber]);
      }
    }
    emitPaired();
    buffer = [];
  };

  items.forEach((item) => {
    if (item.kind === 'line') {
      buffer.push(item.line);
      return;
    }
    flush();
    if (item.kind === 'hunk') {
      rows.push(
        <tr key={item.key}>
          <td
            colSpan={4}
            className="border-y border-border bg-muted/60 px-3 py-1 font-mono text-[11px] text-muted-foreground"
          >
            {item.content}
          </td>
        </tr>,
      );
    } else {
      rows.push(
        <GapRow
          key={item.key}
          colSpan={4}
          onExpand={() => onExpand(item.key)}
          label={t('panels.changes.diff.expandLines', { count: item.hidden.length })}
        />,
      );
    }
  });
  flush();

  return (
    <table className="w-full border-collapse">
      <tbody>{rows}</tbody>
    </table>
  );
}
