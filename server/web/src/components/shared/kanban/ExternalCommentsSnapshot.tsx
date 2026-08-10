import { useTranslation } from 'react-i18next';
import { Pin, ExternalLink, MessageCircle } from 'lucide-react';
import type { ExternalComment } from '@/types/integration';

interface Props {
  source: 'github' | 'tapd' | '';
  externalUrl: string;
  snapshot: ExternalComment[];
  snapshotAt: string | null | undefined;
}

// Inline relative time — duplicated from ExternalIssueHeader; consolidate
// in v1.2 cleanup with the other two callers.
function formatRelative(iso: string): string {
  const diffSec = (Date.now() - new Date(iso).getTime()) / 1000;
  if (diffSec < 60) return `${Math.floor(diffSec)}s`;
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`;
  return `${Math.floor(diffSec / 86400)}d`;
}

/**
 * v1.1 — Read-only snapshot of external (GitHub / TAPD) comments fetched
 * on the most recent manual refresh of an imported issue. Rendered
 * ABOVE the niuniu comment timeline so users can clearly see "external
 * snapshot" vs "niuniu native" comments.
 *
 * Capped at 50 entries by the backend best-effort path; we display
 * whatever the server has cached at issues.external_comments_snapshot.
 * Never persisted into niuniu.issue_comments; never editable; not part
 * of the writeback path.
 */
export function ExternalCommentsSnapshot({ source, externalUrl, snapshot, snapshotAt }: Props) {
  const { t } = useTranslation('projects');
  if (!source) return null;

  // github/tapd have localized labels; a user-created custom provider falls
  // back to its raw name (so its comment snapshot is shown, not hidden).
  const providerLabel =
    source === 'github'
      ? t('externalLink.snapshotProviderGithub')
      : source === 'tapd'
        ? t('externalLink.snapshotProviderTapd')
        : source;

  return (
    <section className="border border-warm-border rounded-md bg-warm-surface">
      <header className="flex items-center justify-between gap-2 px-3 py-2 border-b border-warm-border">
        <div className="flex items-center gap-2 text-xs text-warm-text-muted">
          <Pin className="size-3" aria-hidden />
          <span title={t('externalLink.snapshotSubtitle')}>
            {t('externalLink.snapshotTitle')} · {providerLabel}
          </span>
          {snapshotAt ? (
            <span>· {t('externalLink.snapshotLastSync', { relative: formatRelative(snapshotAt) })}</span>
          ) : null}
        </div>
        {snapshot.length >= 50 && (
          <a
            href={externalUrl}
            target="_blank"
            rel="noreferrer"
            className="flex items-center gap-1 text-xs text-warm-text hover:underline"
          >
            {t('externalLink.snapshotMore', { provider: providerLabel })}
            <ExternalLink className="size-3" />
          </a>
        )}
      </header>

      {snapshot.length === 0 ? (
        <div className="px-3 py-4 text-xs text-warm-text-muted text-center">
          {snapshotAt ? t('externalLink.snapshotEmpty') : t('externalLink.snapshotNeverSynced')}
        </div>
      ) : (
        <ul className="divide-y divide-warm-border">
          {snapshot.map((c) => (
            <li key={c.id} className="px-3 py-2 hover:bg-warm-muted/40">
              <div className="flex items-center justify-between gap-2 mb-1 text-[11px] text-warm-text-muted">
                <span className="font-medium text-warm-text">
                  <MessageCircle className="inline size-3 mr-1 align-text-bottom" aria-hidden />
                  {t('externalLink.snapshotCommentBy', { author: c.author || '?' })}
                </span>
                <div className="flex items-center gap-2">
                  <span className="tabular-nums">{c.created_at}</span>
                  {c.url && (
                    <a
                      href={c.url}
                      target="_blank"
                      rel="noreferrer"
                      className="hover:text-warm-text"
                      aria-label={t('externalLink.snapshotOpenComment')}
                    >
                      <ExternalLink className="size-3" />
                    </a>
                  )}
                </div>
              </div>
              <pre className="text-xs text-warm-text whitespace-pre-wrap font-sans m-0">
                {c.body || ''}
              </pre>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
