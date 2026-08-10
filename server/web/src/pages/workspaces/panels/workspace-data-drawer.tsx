import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { BarChart3, ExternalLink, RefreshCw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet';
import { DataResultBlock } from '@/components/data/data-result-block';
import {
  listWorkspacePanels,
  fetchPanelData,
  type WorkspacePanel,
} from '@/lib/dashboards-api';

/**
 * Toolbar entry + drawer for a workspace's pinned charts. The button renders
 * ONLY when this workspace has pinned panels, so the entry appears just-in-time.
 * The drawer lists those panels inline (live ones can refresh; each links to its
 * dashboard), so charts pinned here can be viewed without leaving the workspace.
 */
export function WorkspaceDataButton({
  workspaceId,
  className,
}: {
  workspaceId: number;
  className?: string;
}) {
  const { t } = useTranslation('workspaces');
  const [open, setOpen] = useState(false);
  const { data } = useQuery({
    queryKey: ['workspace-panels', workspaceId],
    queryFn: () => listWorkspacePanels(workspaceId),
  });
  const panels = data ?? [];
  // Entry only appears when there are pinned charts.
  if (panels.length === 0) return null;

  return (
    <>
      <Button
        variant="ghost"
        onClick={() => setOpen(true)}
        aria-label={t('dataDrawer.open')}
        title={t('dataDrawer.open')}
        className={className}
      >
        <BarChart3 className="w-3.5 h-3.5" aria-hidden="true" />
        <span className="hidden sm:inline">
          {t('toolbar.data')} ({panels.length})
        </span>
      </Button>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent
          side="right"
          className="w-[460px] overflow-y-auto sm:w-[560px]"
        >
          <SheetHeader>
            <SheetTitle>{t('dataDrawer.title')}</SheetTitle>
          </SheetHeader>
          <div className="mt-4 flex flex-col gap-4">
            {panels.map((p) => (
              <DrawerPanel key={p.id} panel={p} />
            ))}
          </div>
        </SheetContent>
      </Sheet>
    </>
  );
}

function DrawerPanel({ panel }: { panel: WorkspacePanel }) {
  const { t } = useTranslation('dashboards');
  const { t: tw } = useTranslation('workspaces');
  const refetchInterval =
    panel.source_id > 0 && panel.refresh_interval_sec > 0
      ? panel.refresh_interval_sec * 1000
      : false;
  const { data, isLoading, isError, isFetching, refetch } = useQuery({
    queryKey: ['panel-data', panel.dashboard_id, panel.id],
    queryFn: () => fetchPanelData(panel.dashboard_id, panel.id),
    refetchInterval,
  });
  const isLive = panel.source_id > 0;
  const block = data
    ? { title: panel.title, result: data, chart: panel.chart_spec }
    : null;

  return (
    <div className="rounded-md border border-border bg-card">
      <div className="flex items-center justify-between gap-2 border-b border-border px-3 py-2">
        <span className="truncate text-sm font-medium text-foreground">
          {panel.title}
        </span>
        <div className="flex shrink-0 items-center gap-1">
          {isLive && (
            <Button
              size="sm"
              variant="ghost"
              aria-label={t('refresh')}
              title={t('refresh')}
              disabled={isFetching}
              onClick={() => void refetch()}
            >
              <RefreshCw className={`size-4 ${isFetching ? 'animate-spin' : ''}`} />
            </Button>
          )}
          <Button
            asChild
            size="sm"
            variant="ghost"
            aria-label={t('openWorkspace')}
            title={panel.dashboard_name}
          >
            <Link
              to="/dashboards/$id"
              params={{ id: String(panel.dashboard_id) }}
            >
              <ExternalLink className="size-4" />
            </Link>
          </Button>
        </div>
      </div>
      <div className="p-3">
        {isLoading && (
          <div className="text-sm text-muted-foreground">{t('loading')}</div>
        )}
        {isError && (
          <div className="text-sm text-destructive">{t('panelLoadFailed')}</div>
        )}
        {block && <DataResultBlock bare data={block} />}
      </div>
      <div className="truncate border-t border-border px-3 py-1.5 text-xs text-muted-foreground">
        {tw('dataDrawer.inDashboard', { name: panel.dashboard_name })}
      </div>
    </div>
  );
}
