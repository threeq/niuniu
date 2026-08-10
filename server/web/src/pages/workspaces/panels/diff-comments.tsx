import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Send, Clock, Check } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import type { WorkspaceComment } from '@/types/api';

// Shared line-level comment UI, used by both the diff viewer and the plain code
// (full-file) viewer so anchoring/queue/send behaves identically in both.

export function Gutter({
  value,
  tone,
  onAdd,
}: {
  value?: number;
  tone?: 'add' | 'del';
  /** When set, hovering the line reveals a "+" to add a comment on it. */
  onAdd?: () => void;
}) {
  return (
    <td
      className={cn(
        'relative w-11 min-w-11 select-none border-r border-border px-2 text-right align-top font-mono text-[11px] leading-5 text-muted-foreground/70',
        tone === 'add' && 'bg-diff-add text-diff-add-fg',
        tone === 'del' && 'bg-diff-del text-diff-del-fg',
      )}
    >
      {onAdd ? (
        <>
          <span className="group-hover/line:opacity-0">{value ?? ''}</span>
          <button
            type="button"
            onClick={onAdd}
            className="absolute inset-0 hidden items-center justify-center text-brand hover:bg-brand-soft group-hover/line:flex"
          >
            <Plus className="h-3 w-3" />
          </button>
        </>
      ) : (
        value ?? ''
      )}
    </td>
  );
}

// Callbacks + state a table needs to render the line-level "+" and threads.
export interface CommentApi {
  repoName: string;
  filePath: string;
  byLine: Map<number, WorkspaceComment[]>;
  activeLine: number | null;
  setActiveLine: (line: number | null) => void;
  onQueue: (line: number, content: string) => Promise<void>;
  onSend: (line: number, content: string) => Promise<void>;
}

/** One persisted comment in the inline thread, with its pending/sent status. */
function CommentItem({ comment, api }: { comment: WorkspaceComment; api: CommentApi }) {
  const { t } = useTranslation('workspaces');
  const pending = comment.sent_to_agent !== true;
  return (
    <div className="flex gap-2">
      <div className="grid h-6 w-6 shrink-0 place-items-center rounded-full bg-brand-soft text-[10px] font-semibold text-brand">
        {t('panels.changes.comments.me')}
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
          <span>
            {t('panels.changes.comments.lineMeta', {
              line: comment.line_number ?? '',
              repo: api.repoName,
              path: api.filePath,
            })}
          </span>
          {pending ? (
            <span className="inline-flex items-center gap-1 rounded-full bg-warning/15 px-1.5 py-px text-[10px] font-medium text-warning-foreground">
              <Clock className="h-2.5 w-2.5" />
              {t('panels.changes.comments.pending')}
            </span>
          ) : (
            <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-1.5 py-px text-[10px] font-medium text-success-foreground">
              <Check className="h-2.5 w-2.5" />
              {t('panels.changes.comments.sent')}
            </span>
          )}
        </div>
        <div className="mt-0.5 whitespace-pre-wrap break-words text-[12.5px] text-foreground">
          {comment.content}
        </div>
      </div>
    </div>
  );
}

/** The inline comment composer: queue or send-directly, anchored to a line. */
function CommentComposer({ line, api }: { line: number; api: CommentApi }) {
  const { t } = useTranslation('workspaces');
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);

  const run = async (action: 'queue' | 'send') => {
    const content = text.trim();
    if (!content || busy) return;
    setBusy(true);
    try {
      if (action === 'queue') await api.onQueue(line, content);
      else await api.onSend(line, content);
      setText('');
      api.setActiveLine(null);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mt-1.5 rounded-md border border-border bg-background p-1.5">
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
          onClick={() => api.setActiveLine(null)}
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
 * The full-width thread row shown beneath a line: existing comments plus the
 * composer (when this line is active). Returns null when there is nothing to
 * show, so callers can render it unconditionally.
 */
export function CommentRow({
  anchor,
  colSpan,
  api,
}: {
  anchor: number;
  colSpan: number;
  api: CommentApi;
}) {
  const comments = api.byLine.get(anchor) ?? [];
  const open = api.activeLine === anchor;
  if (comments.length === 0 && !open) return null;
  return (
    <tr>
      <td colSpan={colSpan} className="border-y border-border bg-muted/30 p-0">
        {/* Pin the thread/composer to the left of the horizontal scroll and cap
            its width, so the action buttons stay reachable no matter how far the
            (wide) code is scrolled sideways. */}
        <div className="sticky left-0 w-full max-w-2xl px-3 py-2 pl-12">
          <div className="flex flex-col gap-2">
            {comments.map((c) => (
              <CommentItem key={c.id} comment={c} api={api} />
            ))}
            {open && <CommentComposer line={anchor} api={api} />}
          </div>
        </div>
      </td>
    </tr>
  );
}
