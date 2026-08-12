import { useMemo } from 'react';
import { FolderTree, GitCompareArrows, CircleDot, Archive, Sparkles, Pin, Presentation, Loader2, Library } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import { useWorkspacePanelStore, type PanelId } from '@/stores/workspace-panel-store';
import { Button } from '@/components/ui/button';
import { useExtractMemory, useExtractStatus } from '@/lib/hooks/use-memory';
import { WorkspaceDataButton } from './panels/workspace-data-drawer';
import type { Workspace } from '@/types/api';

interface WorkspaceToolbarProps {
  workspace: Workspace;
}

// Panel view toggles. `chat` is intentionally absent: it is the always-open
// centre column (togglePanel('chat') is a no-op), so a permanently-disabled
// button was pure noise. Each remaining entry is an independent on/off toggle —
// several panels can be open at once — grouped visually into one segmented set.
const allPanelButtons: { id: PanelId; icon: React.ComponentType<{ className?: string }>; i18nKey: string }[] = [
  { id: 'files', icon: FolderTree, i18nKey: 'toolbar.panels.files' },
  { id: 'changes', icon: GitCompareArrows, i18nKey: 'toolbar.panels.changes' },
  { id: 'artifact', icon: Presentation, i18nKey: 'toolbar.panels.artifact' },
  { id: 'issue', icon: CircleDot, i18nKey: 'toolbar.panels.issue' },
  { id: 'pinned', icon: Pin, i18nKey: 'toolbar.panels.pinned' },
  { id: 'kbs', icon: Library, i18nKey: 'toolbar.panels.kbs' },
];

export function WorkspaceToolbar({ workspace }: WorkspaceToolbarProps) {
  const { t } = useTranslation('workspaces');
  const { togglePanel, isPanelOpen } = useWorkspacePanelStore();
  const workspaceId = Number(workspace.id);
  const extractMemory = useExtractMemory();
  const extractStatus = useExtractStatus(workspaceId);
  // Spinner shows while the async extraction is running on the server (survives
  // reloads) or during the brief kickoff request. Gated on isSuccess so an
  // errored status poll can't leave the spinner stuck on.
  const isExtracting =
    (extractStatus.isSuccess && !!extractStatus.data?.data?.running) || extractMemory.isPending;

  const panelButtons = useMemo(
    () => allPanelButtons.filter((btn) => btn.id !== 'issue' || !!workspace.issue_id),
    [workspace.issue_id]
  );

  const toolbarBtn = 'h-7 px-2 gap-1 text-xs rounded-md font-normal';

  return (
    <div
      className={cn(
        'h-10 border-b border-warm-border bg-warm-muted flex items-center justify-between px-3 shrink-0',
        workspace.is_archived === 1 && 'border-l-2 border-l-warning/60',
      )}
    >
      {/* Breadcrumb — `#` always means the issue (business identity), `!` the
          workspace (runtime identity). When the workspace is linked to an issue
          the issue `#<id>` is the primary id (matches what IM/kanban show) and
          the workspace `!<id>` trails as the secondary technical id; an issue-less
          workspace shows only `!<id>`. */}
      <div className="flex items-center gap-1.5 min-w-0">
        {workspace.issue_id ? (
          <span className="flex items-center gap-1 shrink-0 text-xs font-mono tabular-nums">
            <span className="text-warm-text-muted">#{workspace.issue_id}</span>
            <span className="text-warm-text-muted/60">!{workspace.id}</span>
          </span>
        ) : (
          <span className="truncate text-xs font-mono tabular-nums text-warm-text-muted">
            !{workspace.id}
          </span>
        )}
        <span className="text-warm-text-muted/60">/</span>
        <span className="truncate text-sm font-medium text-warm-text">{workspace.name}</span>
        {workspace.is_archived === 1 && (
          <span className="ml-2 inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] bg-warning/15 text-warning border border-warning/30 shrink-0">
            <Archive className="w-3 h-3" aria-hidden="true" />
            <span>{t('toolbar.archived')}</span>
          </span>
        )}
      </div>

      {/* Action groups: a unified panel-toggle set on the left, secondary
          actions on the right — split by their own container edges instead of a
          loose flat row of equal-weight buttons. */}
      <div className="flex items-center gap-2 shrink-0">
        {/* Panel toggles — one bordered group of independent on/off toggles.
            Several can be active at once (openPanels is a set). */}
        <div className="inline-flex items-center gap-0.5 rounded-lg border border-warm-border bg-warm-surface p-0.5">
          {panelButtons.map(({ id, icon: Icon, i18nKey }) => {
            const isOpen = isPanelOpen(id);
            const label = t(i18nKey);
            return (
              <Button
                key={id}
                variant="ghost"
                onClick={() => togglePanel(id)}
                aria-label={label}
                aria-pressed={isOpen}
                title={label}
                className={cn(
                  toolbarBtn,
                  isOpen
                    ? 'bg-brand-soft text-brand hover:bg-brand-soft hover:text-brand'
                    : 'text-warm-text-muted hover:bg-warm-muted hover:text-warm-text',
                )}
              >
                <Icon className="w-3.5 h-3.5" aria-hidden="true" />
                <span className="hidden sm:inline">{label}</span>
              </Button>
            );
          })}
        </div>

        {/* Secondary actions: pinned-charts drawer + extract learnings. */}
        <div className="flex items-center gap-0.5">
          {/* Pinned-charts entry — appears only when this workspace has any. */}
          <WorkspaceDataButton
            workspaceId={Number(workspace.id)}
            className={cn(toolbarBtn, 'text-warm-text-muted')}
          />

          {/* Extract learnings button */}
          <Button
            variant="ghost"
            onClick={() => extractMemory.mutate(workspaceId)}
            disabled={isExtracting}
            aria-label={t('toolbar.extractLearnings')}
            title={t('toolbar.extractLearnings')}
            className={cn(toolbarBtn, 'text-warm-text-muted')}
          >
            {isExtracting ? (
              <Loader2 className="w-3.5 h-3.5 animate-spin" aria-hidden="true" />
            ) : (
              <Sparkles className="w-3.5 h-3.5" aria-hidden="true" />
            )}
            <span className="hidden sm:inline">{t('toolbar.extractLearnings')}</span>
          </Button>
        </div>
      </div>
    </div>
  );
}
