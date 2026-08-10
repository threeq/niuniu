import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from '@tanstack/react-router';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import {
  ArrowLeft,
  Check,
  ChevronDown,
  Copy,
  ExternalLink,
  FolderInput,
  MoveRight,
  RefreshCw,
  Trash2,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSub,
  DropdownMenuSubTrigger,
  DropdownMenuSubContent,
} from '@/components/ui/dropdown-menu';
import { DataResultBlock } from '@/components/data/data-result-block';
import {
  listPanels,
  deletePanel,
  fetchPanelData,
  listDashboards,
  movePanel,
  copyPanel,
  type DashboardPanel,
  type Dashboard,
} from '@/lib/dashboards-api';
import { confirm } from '@/lib/confirm';

interface Props {
  dashboardId: string;
}

export function DashboardDetail({ dashboardId }: Props) {
  const { t } = useTranslation('dashboards');
  const id = Number(dashboardId);

  // Warm the echarts chunk on mount — panels may render charts (same idea as
  // the chat panel, plan task D4 / E5 step 3).
  useEffect(() => {
    const ric =
      window.requestIdleCallback ??
      ((cb: () => void) => window.setTimeout(cb, 200) as unknown as number);
    const cancel = window.cancelIdleCallback ?? window.clearTimeout;
    const handle = ric(() => {
      void import('@/components/data/echarts-renderer');
    });
    return () => cancel(handle as number);
  }, []);

  const navigate = useNavigate();
  const { data: panels, isLoading } = useQuery({
    queryKey: ['dashboard-panels', id],
    queryFn: () => listPanels(id),
  });
  // All dashboards: drives the header switcher and the per-panel move/copy menu.
  const { data: dashboards } = useQuery({
    queryKey: ['dashboards'],
    queryFn: listDashboards,
  });
  const dashboardList = dashboards ?? [];
  const currentName = dashboardList.find((d) => d.id === id)?.name;

  return (
    // The layout wraps pages in a fixed-height `overflow-hidden` region, so the
    // page itself must scroll when panels overflow the viewport.
    <div className="flex h-full flex-col gap-4 overflow-y-auto p-6">
      <div className="flex items-center gap-3">
        <Link
          to="/dashboards"
          className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground transition-colors"
        >
          <ArrowLeft className="size-4" />
          {t('back')}
        </Link>
        {/* Dashboard switcher — hop between dashboards without going to the list. */}
        {dashboardList.length > 0 && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" size="sm" className="font-normal">
                {currentName ?? t('loading')}
                <ChevronDown className="ml-1 size-4 opacity-50" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              {dashboardList.map((d) => (
                <DropdownMenuItem
                  key={d.id}
                  onClick={() => {
                    if (d.id !== id) {
                      navigate({
                        to: '/dashboards/$id',
                        params: { id: String(d.id) },
                      });
                    }
                  }}
                >
                  <Check
                    className={`mr-2 size-4 ${d.id === id ? 'opacity-100' : 'opacity-0'}`}
                  />
                  {d.name}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>

      {isLoading && (
        <div className="text-sm text-muted-foreground">{t('loading')}</div>
      )}

      {!isLoading && (!panels || panels.length === 0) && (
        <div className="text-sm text-muted-foreground">{t('panelEmpty')}</div>
      )}

      {!isLoading && panels && panels.length > 0 && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {panels.map((p) => (
            <PanelCard
              key={p.id}
              dashboardId={id}
              panel={p}
              dashboards={dashboardList}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function PanelCard({
  dashboardId,
  panel,
  dashboards,
}: {
  dashboardId: number;
  panel: DashboardPanel;
  dashboards: Dashboard[];
}) {
  const { t, i18n } = useTranslation('dashboards');
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [enlarged, setEnlarged] = useState(false);

  // Move/copy this panel to another dashboard. Both refresh the source and
  // target panel lists so the change shows immediately.
  const invalidatePanels = (targetId: number) => {
    queryClient.invalidateQueries({ queryKey: ['dashboard-panels', dashboardId] });
    queryClient.invalidateQueries({ queryKey: ['dashboard-panels', targetId] });
  };
  const moveMut = useMutation({
    mutationFn: (targetId: number) => movePanel(dashboardId, panel.id, targetId),
    onSuccess: (_d, targetId) => {
      invalidatePanels(targetId);
      toast.success(t('moved'));
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : t('panelLoadFailed')),
  });
  const copyMut = useMutation({
    mutationFn: (targetId: number) => copyPanel(dashboardId, panel.id, targetId),
    onSuccess: (_d, targetId) => {
      invalidatePanels(targetId);
      toast.success(t('copied'));
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : t('panelLoadFailed')),
  });
  const otherDashboards = dashboards.filter((d) => d.id !== dashboardId);

  // A live panel re-fetches on its configured cadence so its "data time"
  // (result.queried_at) tracks the latest successful fetch; a static panel
  // (refresh_interval_sec 0) fetches once. Only live panels carry an interval.
  const refetchInterval =
    panel.source_id > 0 && panel.refresh_interval_sec > 0
      ? panel.refresh_interval_sec * 1000
      : false;
  const { data, isLoading, isError, isFetching, refetch } = useQuery({
    queryKey: ['panel-data', dashboardId, panel.id],
    queryFn: () => fetchPanelData(dashboardId, panel.id),
    refetchInterval,
  });

  // Data time shown under the chart, sourced from the server-stamped
  // result.queried_at (when the data was actually obtained from the source) —
  // NOT the panel's pin time. A live panel shows its latest re-query time; a
  // static snapshot carries the time its data was captured.
  const isLive = panel.source_id > 0;
  const dataTime = data?.queried_at
    ? t(isLive ? 'dataTimeLive' : 'dataTimeSnapshot', {
        time: new Date(data.queried_at).toLocaleString(i18n.language),
      })
    : null;

  const remove = useMutation({
    mutationFn: () => deletePanel(dashboardId, panel.id),
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: ['dashboard-panels', dashboardId],
      }),
  });

  const goToWorkspace = () => {
    if (panel.workspace_id == null) return;
    navigate({
      to: '/workspaces/$id',
      params: { id: String(panel.workspace_id) },
    });
  };

  const handleDelete = async () => {
    if (!(await confirm(t('deletePanelConfirm')))) return;
    remove.mutate();
  };

  const block = data
    ? { title: panel.title, result: data, chart: panel.chart_spec }
    : null;

  return (
    <div className="rounded-md border border-border bg-card">
      <div className="flex items-center justify-between gap-2 border-b border-border px-3 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-sm font-medium text-foreground">
            {panel.title}
          </span>
          {/* Distinguish a re-runnable query panel (live, re-fetches on load)
              from a static snapshot panel (fixed inline chart, no data source). */}
          <Badge
            variant={panel.source_id > 0 ? 'secondary' : 'outline'}
            className="shrink-0"
          >
            {panel.source_id > 0 ? t('liveBadge') : t('snapshotBadge')}
          </Badge>
        </div>
        <div className="flex items-center gap-1">
          {/* Live (source-backed) panels can be re-queried on demand for the
              latest data; static snapshots have nothing to refresh. */}
          {isLive && (
            <Button
              size="sm"
              variant="ghost"
              aria-label={isFetching ? t('refreshing') : t('refresh')}
              title={t('refresh')}
              disabled={isFetching}
              onClick={() => void refetch()}
            >
              <RefreshCw
                className={`size-4 ${isFetching ? 'animate-spin' : ''}`}
              />
            </Button>
          )}
          {/* Only this button navigates to the origin workspace; clicking the
              chart itself enlarges it (see below). Hidden when the panel has
              no origin workspace (e.g. a static chart pinned outside a ws). */}
          {panel.workspace_id != null && (
            <Button size="sm" variant="ghost" onClick={goToWorkspace}>
              <ExternalLink className="mr-1 size-4" />
              {t('backToWorkspace')}
            </Button>
          )}
          {/* Move / copy this panel to another dashboard (manual organization). */}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                size="sm"
                variant="ghost"
                aria-label={t('moveOrCopy')}
                title={t('moveOrCopy')}
                disabled={moveMut.isPending || copyMut.isPending}
              >
                <FolderInput className="size-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuSub>
                <DropdownMenuSubTrigger>
                  <MoveRight className="mr-2 size-4" />
                  {t('moveTo')}
                </DropdownMenuSubTrigger>
                <DropdownMenuSubContent>
                  {otherDashboards.length === 0 ? (
                    <DropdownMenuItem disabled>
                      {t('noOtherDashboards')}
                    </DropdownMenuItem>
                  ) : (
                    otherDashboards.map((d) => (
                      <DropdownMenuItem
                        key={d.id}
                        onClick={() => moveMut.mutate(d.id)}
                      >
                        {d.name}
                      </DropdownMenuItem>
                    ))
                  )}
                </DropdownMenuSubContent>
              </DropdownMenuSub>
              <DropdownMenuSub>
                <DropdownMenuSubTrigger>
                  <Copy className="mr-2 size-4" />
                  {t('copyTo')}
                </DropdownMenuSubTrigger>
                <DropdownMenuSubContent>
                  {dashboards.map((d) => (
                    <DropdownMenuItem
                      key={d.id}
                      onClick={() => copyMut.mutate(d.id)}
                    >
                      {d.name}
                      {d.id === dashboardId ? ` (${t('current')})` : ''}
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuSubContent>
              </DropdownMenuSub>
            </DropdownMenuContent>
          </DropdownMenu>
          <Button
            size="sm"
            variant="ghost"
            aria-label={t('deletePanel')}
            disabled={remove.isPending}
            onClick={handleDelete}
          >
            <Trash2 className="size-4" />
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
        {block && (
          // Click the chart to enlarge it in a dialog (not navigate).
          <div
            role="button"
            tabIndex={0}
            aria-label={t('enlarge')}
            onClick={() => setEnlarged(true)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                setEnlarged(true);
              }
            }}
            className="w-full cursor-zoom-in rounded-sm transition-colors hover:bg-muted/40"
          >
            <DataResultBlock bare data={block} />
          </div>
        )}
      </div>

      {block && dataTime && (
        <div className="border-t border-border px-3 py-1.5 text-xs text-muted-foreground">
          {dataTime}
        </div>
      )}

      {/* Truncation warning: a row-limited result drops the TAIL of the rows,
          which for an ascending time series is the NEWEST data — so a "live"
          panel can silently look frozen. Surface it instead of hiding it. */}
      {block && data?.truncated && (
        <div className="border-t border-border px-3 py-1.5 text-xs text-warning">
          {t('dataTruncated')}
        </div>
      )}

      {block && (
        <Dialog open={enlarged} onOpenChange={setEnlarged}>
          <DialogContent className="max-w-4xl">
            <DialogHeader>
              <DialogTitle>{panel.title}</DialogTitle>
            </DialogHeader>
            <div className="max-h-[75vh] overflow-auto">
              <DataResultBlock bare data={block} heightClass="h-[70vh]" />
            </div>
          </DialogContent>
        </Dialog>
      )}
    </div>
  );
}
