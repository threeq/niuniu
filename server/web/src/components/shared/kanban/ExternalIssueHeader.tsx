import { useTranslation } from 'react-i18next';
import { Github, Building2, ExternalLink } from 'lucide-react';

// External tracker header for the issue detail panel.
//
// Renders a thin strip surfacing the upstream link + last-snapshot
// timestamp. The legacy refresh button + writeback-paused toggle were
// removed when the M2/M3 writeback subsystem was deleted: external
// sync now happens via the AI proxy (/mcp/external-proxy/*) on demand,
// not via per-issue HTTP toggles on the issue detail panel.

interface Props {
  externalSource: string;
  externalId: string;
  externalUrl: string;
  externalSnapshotAt: string | null;
}

export function ExternalIssueHeader({
  externalSource,
  externalId,
  externalUrl,
  externalSnapshotAt,
}: Props) {
  const { t } = useTranslation('projects');

  if (!externalSource) return null;
  // github keeps its mark; tapd and any user-created custom provider use a
  // neutral tracker icon (was: custom providers fell through and the whole
  // header was hidden).
  const Icon = externalSource === 'github' ? Github : Building2;

  return (
    <div className="mx-5 mt-3 flex items-center gap-3 rounded border border-warm-border bg-warm-muted px-3 py-2 text-sm">
      <Icon className="size-4 text-warm-text-muted" aria-hidden />
      <a
        href={externalUrl}
        target="_blank"
        rel="noreferrer"
        className="flex items-center gap-1 font-mono text-warm-text hover:underline"
      >
        {externalId}
        <ExternalLink className="size-3" aria-hidden />
      </a>
      <span className="text-xs text-warm-text-muted">
        {externalSnapshotAt
          ? t('externalLink.lastSynced', { relative: formatRelative(externalSnapshotAt) })
          : t('externalLink.neverSynced')}
      </span>
    </div>
  );
}

// Tiny relative-time formatter — avoids dragging in date-fns just for one
// header. Granularity stops at days; older snapshots simply read "<n>d".
function formatRelative(iso: string): string {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return iso;
  const diffSec = Math.max(0, (Date.now() - t) / 1000);
  if (diffSec < 60) return `${Math.floor(diffSec)}s`;
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`;
  return `${Math.floor(diffSec / 86400)}d`;
}
