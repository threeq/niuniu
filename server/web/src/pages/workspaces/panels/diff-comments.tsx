import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Send,
  Clock,
  Check,
  CircleCheck,
  CircleDot,
  History,
  ChevronDown,
  ChevronRight,
  Minus,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import type { CommentAnchorContext, WorkspaceComment } from '@/types/api';
import { isOutdated } from '@/lib/hooks/use-workspace-comments';

// Shared line-level comment UI, used by both the diff viewer and the plain code
// (full-file) viewer so anchoring/queue/send behaves identically in both. The
// line gutter itself lives in `code-surface/` — it is layout, not comment UI.

/** What a composer is anchoring to: a live new-side line, or a deleted old line. */
export interface ComposeTarget {
  /** The anchor coordinate (new-side line number, or the old line for side=old). */
  line: number;
  side: 'old' | 'new';
  /**
   * Snapshot of the deleted line + its neighbours, required for side=old: the
   * old side is not in the working tree, so only the client that rendered the
   * diff can capture it. Without it the server has no anchor at all and the
   * comment would read as outdated the moment it is created.
   */
  context?: CommentAnchorContext;
}

// Callbacks + state a surface needs to render the line-level "+" and threads.
export interface CommentApi {
  repoName: string;
  filePath: string;
  /** Anchored comments keyed by the line they currently render at. */
  byLine: Map<number, WorkspaceComment[]>;
  /** Which line's composer is open, and on which side. */
  active: ComposeTarget | null;
  setActive: (target: ComposeTarget | null) => void;
  onQueue: (target: ComposeTarget, content: string) => Promise<void>;
  onSend: (target: ComposeTarget, content: string) => Promise<void>;
  /** Set/clear the review verdict. Omitted where the surface is read-only. */
  onSetResolved?: (commentId: number, resolved: boolean) => Promise<void>;
}

/** A small pill; the shared shape for every comment status badge. */
function Badge({
  tone,
  icon: Icon,
  children,
  title,
}: {
  tone: 'warning' | 'success' | 'info' | 'muted' | 'destructive';
  icon: React.ComponentType<{ className?: string }>;
  children: React.ReactNode;
  title?: string;
}) {
  const tones = {
    warning: 'bg-warning/15 text-warning-foreground',
    success: 'bg-success/15 text-success-foreground',
    info: 'bg-info/15 text-info-foreground',
    muted: 'bg-muted text-muted-foreground',
    destructive: 'bg-destructive/15 text-destructive',
  } as const;
  return (
    <span
      title={title}
      className={cn(
        'inline-flex items-center gap-1 rounded-full px-1.5 py-px text-[10px] font-medium',
        tones[tone],
      )}
    >
      <Icon className="h-2.5 w-2.5" />
      {children}
    </span>
  );
}

/**
 * A context snapshot rendered as code: the commented line, emphasised, with its
 * neighbours dimmed. Used for both "what the reviewer saw" and "what stands
 * there now" — showing them in the same shape is what makes the comparison
 * readable at a glance.
 */
function ContextSnapshot({
  context,
  tone,
}: {
  context: CommentAnchorContext;
  tone: 'before' | 'after';
}) {
  const rows: Array<{ text: string; anchored: boolean }> = [
    ...(context.before ?? []).map((text) => ({ text, anchored: false })),
    { text: context.line ?? '', anchored: true },
    ...(context.after ?? []).map((text) => ({ text, anchored: false })),
  ];
  return (
    <div className="overflow-x-auto rounded border border-border bg-background">
      {rows.map((row, i) => (
        <div
          key={i}
          className={cn(
            'whitespace-pre px-2 font-mono text-[11px] leading-4',
            row.anchored
              ? tone === 'before'
                ? 'bg-diff-del text-diff-del-fg'
                : 'bg-diff-add text-diff-add-fg'
              : 'text-muted-foreground/70',
          )}
        >
          {row.text.length > 0 ? row.text : '​'}
        </div>
      ))}
    </div>
  );
}

/**
 * The anchor panel: the evidence a reviewer needs to judge "was this actually
 * addressed?".
 *
 * It is collapsed by default and expanded on demand, EXCEPT that the outdated
 * marker itself is always visible in the header — a comment whose anchor is
 * gone must never look like one that is still attached, which is the failure
 * this whole wave exists to remove.
 */
function AnchorDetail({ comment }: { comment: WorkspaceComment }) {
  const { t } = useTranslation('workspaces');
  const [open, setOpen] = useState(false);
  const anchor = comment.anchor;
  if (!anchor) return null;

  const hasSnapshot = !!anchor.context && (anchor.context.line ?? '') !== '';
  const hasCurrent = !!anchor.current;
  if (!hasSnapshot && !hasCurrent) return null;

  const Chevron = open ? ChevronDown : ChevronRight;
  return (
    <div className="mt-1">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="inline-flex items-center gap-1 rounded px-1 py-px text-[10.5px] text-muted-foreground hover:bg-accent hover:text-foreground"
      >
        <Chevron className="h-3 w-3" />
        {t(
          hasCurrent
            ? 'panels.changes.comments.anchor.compare'
            : 'panels.changes.comments.anchor.showSnapshot',
        )}
      </button>
      {open && (
        <div className="mt-1 flex flex-col gap-1.5">
          {hasSnapshot && (
            <div>
              <div className="mb-0.5 text-[10px] uppercase tracking-wide text-muted-foreground/70">
                {t('panels.changes.comments.anchor.thenLabel')}
              </div>
              <ContextSnapshot context={anchor.context!} tone="before" />
            </div>
          )}
          {hasCurrent && (
            <div>
              <div className="mb-0.5 text-[10px] uppercase tracking-wide text-muted-foreground/70">
                {t('panels.changes.comments.anchor.nowLabel')}
              </div>
              <ContextSnapshot context={anchor.current!} tone="after" />
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * One persisted comment, with its TWO orthogonal states shown separately:
 *
 *   sent_to_agent — delivery. Did the agent receive this?
 *   resolved      — the review verdict. Did I judge it addressed?
 *
 * These used to be conflated (an injected comment vanished from the pending
 * view whether or not anything was fixed), so they are deliberately rendered as
 * two independent badges rather than one merged status.
 */
export function CommentItem({ comment, api }: { comment: WorkspaceComment; api: CommentApi }) {
  const { t } = useTranslation('workspaces');
  const [busy, setBusy] = useState(false);
  const pending = comment.sent_to_agent !== true;
  const resolved = !!comment.resolved;
  const outdated = isOutdated(comment);
  const status = comment.anchor?.status;
  const oldSide = comment.anchor?.side === 'old' || comment.side === 'old';

  const toggleResolved = async () => {
    if (!api.onSetResolved || busy) return;
    setBusy(true);
    try {
      await api.onSetResolved(comment.id, !resolved);
    } finally {
      setBusy(false);
    }
  };

  // Where the comment sits NOW. An outdated anchor has no honest position, so
  // it is labelled by its original line and explicitly marked, rather than
  // printed as a plain line number that reads as verified.
  const shownLine = outdated
    ? (comment.anchor?.original_line ?? comment.line_number ?? null)
    : (comment.anchor?.effective_line ?? comment.line_number ?? null);

  return (
    <div className={cn('flex gap-2', resolved && 'opacity-60')}>
      <div className="grid h-6 w-6 shrink-0 place-items-center rounded-full bg-brand-soft text-[10px] font-semibold text-brand">
        {t('panels.changes.comments.me')}
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
          <span>
            {t('panels.changes.comments.lineMeta', {
              line: shownLine ?? '',
              repo: api.repoName,
              path: api.filePath,
            })}
          </span>
          {oldSide && (
            <Badge tone="destructive" icon={Minus} title={t('panels.changes.comments.oldSideHint')}>
              {t('panels.changes.comments.oldSide')}
            </Badge>
          )}
          {outdated && (
            <Badge
              tone="warning"
              icon={History}
              title={t('panels.changes.comments.anchor.outdatedHint')}
            >
              {t('panels.changes.comments.anchor.outdated')}
            </Badge>
          )}
          {status === 'relocated' && (
            <Badge
              tone="info"
              icon={History}
              title={t('panels.changes.comments.anchor.relocatedHint', {
                from: comment.anchor?.original_line ?? '',
                to: comment.anchor?.effective_line ?? '',
              })}
            >
              {t('panels.changes.comments.anchor.relocated')}
            </Badge>
          )}
          {/* Delivery */}
          {pending ? (
            <Badge tone="warning" icon={Clock}>
              {t('panels.changes.comments.pending')}
            </Badge>
          ) : (
            <Badge tone="muted" icon={Check}>
              {t('panels.changes.comments.sent')}
            </Badge>
          )}
          {/* Verdict — separate from delivery, and the only one that closes a comment. */}
          {api.onSetResolved ? (
            <button
              type="button"
              onClick={toggleResolved}
              disabled={busy}
              title={t(
                resolved
                  ? 'panels.changes.comments.verdict.reopenHint'
                  : 'panels.changes.comments.verdict.resolveHint',
              )}
              className="rounded-full disabled:opacity-50"
            >
              <Badge tone={resolved ? 'success' : 'muted'} icon={resolved ? CircleCheck : CircleDot}>
                {t(
                  resolved
                    ? 'panels.changes.comments.verdict.resolved'
                    : 'panels.changes.comments.verdict.open',
                )}
              </Badge>
            </button>
          ) : (
            <Badge tone={resolved ? 'success' : 'muted'} icon={resolved ? CircleCheck : CircleDot}>
              {t(
                resolved
                  ? 'panels.changes.comments.verdict.resolved'
                  : 'panels.changes.comments.verdict.open',
              )}
            </Badge>
          )}
        </div>
        <div
          className={cn(
            'mt-0.5 whitespace-pre-wrap break-words text-[12.5px] text-foreground',
            resolved && 'line-through decoration-muted-foreground/50',
          )}
        >
          {comment.content}
        </div>
        <AnchorDetail comment={comment} />
      </div>
    </div>
  );
}

/** The inline comment composer: queue or send-directly, anchored to a line. */
function CommentComposer({ target, api }: { target: ComposeTarget; api: CommentApi }) {
  const { t } = useTranslation('workspaces');
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);

  const run = async (action: 'queue' | 'send') => {
    const content = text.trim();
    if (!content || busy) return;
    setBusy(true);
    try {
      if (action === 'queue') await api.onQueue(target, content);
      else await api.onSend(target, content);
      setText('');
      api.setActive(null);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mt-1.5 rounded-md border border-border bg-background p-1.5">
      {target.side === 'old' && (
        <div className="mb-1 px-1.5 text-[10.5px] text-muted-foreground">
          {t('panels.changes.comments.composingOldSide', { line: target.line })}
        </div>
      )}
      <textarea
        autoFocus
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={t('panels.changes.comments.placeholder')}
        rows={2}
        className="w-full resize-y bg-transparent px-1.5 py-1 text-[12.5px] outline-none placeholder:text-muted-foreground/60"
      />
      <div className="flex flex-wrap items-center justify-start gap-1.5 pt-1">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-7"
          onClick={() => api.setActive(null)}
          disabled={busy}
        >
          {t('panels.changes.comments.cancel')}
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-7"
          onClick={() => run('queue')}
          disabled={busy || !text.trim()}
        >
          {t('panels.changes.comments.queue')}
        </Button>
        <Button
          type="button"
          size="sm"
          className="h-7 gap-1"
          onClick={() => run('send')}
          disabled={busy || !text.trim()}
        >
          <Send className="h-3 w-3" />
          {t('panels.changes.comments.sendNow')}
        </Button>
      </div>
    </div>
  );
}

/**
 * The full-width thread shown beneath a line: existing comments plus the
 * composer (when this line is active). Returns null when there is nothing to
 * show, so callers can render it unconditionally.
 *
 * This is an *attachment* in the CodeSurface sense — it lives INSIDE the line's
 * row element rather than in a row of its own. That placement is what keeps the
 * virtualized list stable: the row is the unit `measureElement` observes, so
 * opening or closing a thread re-measures that one row in place instead of
 * inserting/removing an index and shifting everything below it.
 *
 * Only anchored comments appear here. An outdated one has no line to sit on and
 * is surfaced by the file-level `OutdatedCommentList` instead.
 */
export function CommentThread({
  anchor,
  side = 'new',
  api,
}: {
  anchor: number;
  /**
   * Which coordinate space this thread belongs to. The caller supplies the
   * matching `api.byLine` (new-side or old-side map); `side` is what keeps the
   * COMPOSER from opening on both halves of a modification row at once, since
   * both sides can be active on the same integer.
   */
  side?: 'old' | 'new';
  api: CommentApi;
}) {
  const comments = api.byLine.get(anchor) ?? [];
  const open = api.active?.line === anchor && api.active.side === side;
  if (comments.length === 0 && !open) return null;
  return (
    <div className="border-y border-border bg-muted/30">
      {/* Pin the thread/composer to the left of the horizontal scroll and cap
          its width, so the action buttons stay reachable no matter how far the
          (wide) code is scrolled sideways. */}
      <div className="sticky left-0 w-full max-w-2xl px-3 py-2 pl-12">
        <div className="flex flex-col gap-2">
          {comments.map((c) => (
            <CommentItem key={c.id} comment={c} api={api} />
          ))}
          {open && <CommentComposer target={api.active!} api={api} />}
        </div>
      </div>
    </div>
  );
}

/**
 * Comments whose anchor no longer resolves to a line in this file.
 *
 * They cannot be shown inline — there is no honest line to attach them to — but
 * dropping them from the view would be worse: the reviewer would silently lose
 * feedback they wrote. So they are collected into a banner above the diff, each
 * carrying its own snapshot of what the code looked like when it was written.
 */
export function OutdatedCommentList({
  comments,
  api,
}: {
  comments: WorkspaceComment[];
  api: CommentApi;
}) {
  const { t } = useTranslation('workspaces');
  const [open, setOpen] = useState(false);
  if (comments.length === 0) return null;
  const Chevron = open ? ChevronDown : ChevronRight;
  return (
    <div className="shrink-0 border-b border-border bg-warning/5">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-1.5 px-3 py-1.5 text-left text-[11.5px] text-warning-foreground hover:bg-warning/10"
      >
        <Chevron className="h-3.5 w-3.5 shrink-0" />
        <History className="h-3.5 w-3.5 shrink-0" />
        {t('panels.changes.comments.anchor.outdatedBanner', { count: comments.length })}
      </button>
      {open && (
        <div className="flex max-h-64 flex-col gap-2 overflow-y-auto px-3 pb-2 pl-9">
          {comments.map((c) => (
            <CommentItem key={c.id} comment={c} api={api} />
          ))}
        </div>
      )}
    </div>
  );
}
