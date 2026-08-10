import { useTranslation } from 'react-i18next';
import { Github, Building2 } from 'lucide-react';
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip';

interface Props {
  /** Provider name from `issues.external_source` (e.g. "github"). */
  provider: string;
  /** Numeric external issue number for display (e.g. 42). Falls back to
   * `external_id` if no parsed number is available. */
  number?: number;
  externalId?: string;
  /** Permalink to the upstream issue. */
  url?: string;
}

// Compact corner badge rendered on top of a kanban IssueCard when the issue
// originated from an external tracker. Click → open upstream in a new tab.
export function ExternalIssueBadge({
  provider,
  number,
  externalId,
  url,
}: Props) {
  const { t } = useTranslation('projects');
  const display = number != null ? `#${number}` : externalId ? `#${externalId}` : '';
  const Icon = provider === 'github' ? Github : Building2;

  const inner = (
    <a
      href={url}
      target="_blank"
      rel="noreferrer"
      onClick={(e) => e.stopPropagation()}
      className="inline-flex items-center gap-0.5 rounded bg-warm-muted px-1 py-0.5 text-[10px] text-warm-text-muted hover:text-warm-text hover:bg-warm-border/60 transition-colors tabular-nums"
    >
      <Icon className="h-3 w-3" aria-hidden="true" />
      <span>{display}</span>
    </a>
  );

  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger asChild>{inner}</TooltipTrigger>
        <TooltipContent>
          {t('externalIssues.badge.openExternal', { provider })}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
