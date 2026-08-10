import { Layers, CornerDownRight } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import type { Workspace } from '@/types/api';

export interface IssueTypeMarkerProps {
  issueType: Workspace['issue_type'];
  parentIssueId?: Workspace['parent_issue_id'];
  className?: string;
}

/**
 * Issue-category markers for the workspace sidebar cards. Icon-only by design:
 * the rows are dense (10px text, tight status badges), so a full text pill
 * would crowd them. The three categories read at a glance:
 *   - Epic (issue_type='epic'):        brand-colored `Layers` (reuses the kanban
 *     Epic visual language so an Epic reads the same everywhere)
 *   - Sub-issue (has parent_issue_id):  muted `CornerDownRight` (nested-under-
 *     parent semantic; gray keeps it visibly secondary to the Epic marker)
 *   - Regular issue (neither):          nothing
 * Epic and sub-issue can co-occur (an Epic that is itself a child); both render.
 */
export function WorkspaceIssueTypeMarker({ issueType, parentIssueId, className }: IssueTypeMarkerProps) {
  const { t } = useTranslation('workspaces');
  const isEpic = issueType === 'epic';
  const isSubIssue = parentIssueId != null;
  if (!isEpic && !isSubIssue) return null;

  const epicLabel = t('sidebar.card.epicMarker');
  const subLabel = t('sidebar.card.subIssueMarker');
  return (
    <>
      {isEpic && (
        <span className="inline-flex shrink-0" title={epicLabel} aria-label={epicLabel} role="img">
          <Layers className={cn('w-3 h-3 text-brand', className)} aria-hidden="true" />
        </span>
      )}
      {isSubIssue && (
        <span className="inline-flex shrink-0" title={subLabel} aria-label={subLabel} role="img">
          <CornerDownRight className={cn('w-3 h-3 text-muted-foreground', className)} aria-hidden="true" />
        </span>
      )}
    </>
  );
}
