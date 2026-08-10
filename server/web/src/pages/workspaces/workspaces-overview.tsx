import { useMemo, useState } from 'react';
import { Link } from '@tanstack/react-router';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { AlertTriangle, Activity, Coins, Layers, ExternalLink, FolderKanban, GitMerge, FileEdit, ChevronRight, Trash2, Loader2 } from 'lucide-react';
import { api, batchDeleteWorkspaces } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';
import { OwnerFilter } from '@/components/shared/owner-filter';
import { OwnerBadge } from '@/components/shared/owner-badge';
import { AttentionIssuesPanel } from '@/components/workspaces/attention-issues-panel';
import { OwnerTokenTrend } from '@/components/workspaces/token-usage-chart';
import { CreatorPicker } from '@/components/shared/creator-picker';
import { UserBadge } from '@/components/shared/user-badge';
import { useAuthStore } from '@/stores/auth-store';
import { useOrgStore } from '@/stores/org-store';
import type { OwnerRef } from '@/types/org';
import type { WorkspaceOverviewItem, WorkspaceOverview } from '@/types/api';

// Compact token count: 1234 -> "1.2K", 1_500_000 -> "1.5M".
function formatTokens(n: number): string {
  if (!isFinite(n) || n <= 0) return '0';
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}K`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

// Time-ago in coarse buckets — exact minutes/seconds aren't useful here.
function timeAgo(iso: string | null | undefined, t: (k: string, v?: Record<string, unknown>) => string): string {
  if (!iso) return t('overview.timeAgo.never');
  const then = new Date(iso).getTime();
  if (!isFinite(then)) return t('overview.timeAgo.never');
  const diff = Date.now() - then;
  const m = Math.floor(diff / 60000);
  if (m < 1) return t('overview.timeAgo.justNow');
  if (m < 60) return t('overview.timeAgo.minutes', { count: m });
  const h = Math.floor(m / 60);
  if (h < 24) return t('overview.timeAgo.hours', { count: h });
  const d = Math.floor(h / 24);
  if (d < 30) return t('overview.timeAgo.days', { count: d });
  const mo = Math.floor(d / 30);
  return t('overview.timeAgo.months', { count: mo });
}

function ownerToFilterParam(refs: OwnerRef[] | 'all'): string | undefined {
  if (refs === 'all') return undefined;
  if (refs.length !== 1) return undefined;
  const r = refs[0];
  return r.type === 'user' ? `user:${r.id}` : `org:${r.id}`;
}

// MetricCard — top-row tile. Compact, no decorations the design system bans.
function MetricCard({
  label,
  value,
  hint,
  icon: Icon,
  tone,
}: {
  label: string;
  value: string;
  hint?: string;
  icon: React.ComponentType<{ className?: string }>;
  tone: 'neutral' | 'warning' | 'destructive';
}) {
  const toneClass =
    tone === 'destructive'
      ? 'text-destructive'
      : tone === 'warning'
        ? 'text-warning'
        : 'text-foreground';
  return (
    <div className="rounded-lg border bg-card p-4 flex flex-col gap-2">
      <div className="flex items-center gap-2 text-muted-foreground">
        <Icon className="w-4 h-4" />
        <span className="text-xs font-medium">{label}</span>
      </div>
      <div className={`text-2xl font-semibold tabular-nums ${toneClass}`}>{value}</div>
      {hint ? <div className="text-xs text-muted-foreground">{hint}</div> : null}
    </div>
  );
}

function StatusDot({ status, isStuck }: { status: string; isStuck: boolean }) {
  let cls = 'bg-muted-foreground/40';
  if (isStuck) cls = 'bg-destructive';
  else if (status === 'attention') cls = 'bg-destructive';
  else if (status === 'needs_review') cls = 'bg-warning';
  else if (status === 'running') cls = 'bg-success';
  else if (status === 'completed') cls = 'bg-info';
  return <span className={`inline-block w-2 h-2 rounded-full ${cls}`} aria-hidden />;
}

function WorkspaceRow({
  item,
  selectMode = false,
  selected = false,
  onToggleSelect,
}: {
  item: WorkspaceOverviewItem;
  selectMode?: boolean;
  selected?: boolean;
  onToggleSelect?: (id: number) => void;
}) {
  const { t } = useTranslation('workspaces');
  const hasGit = item.ahead_count > 0 || item.changes_count > 0;
  const isDeleting = item.status === 'deleting';

  const inner = (
    <>
      {selectMode ? (
        <Checkbox
          checked={selected}
          disabled={isDeleting}
          aria-label={t('overview.batch.selectAria', { id: item.workspace_id })}
          className="shrink-0"
        />
      ) : (
        <StatusDot status={item.status} isStuck={item.is_stuck} />
      )}
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold text-muted-foreground tabular-nums shrink-0">
            #{item.workspace_id}
          </span>
          {item.name ? (
            <span className="text-sm font-medium truncate">{item.name}</span>
          ) : null}
          {isDeleting ? (
            <span
              className="inline-flex items-center gap-1 px-1.5 py-0.5 text-[10px] font-medium rounded bg-warning/10 text-warning shrink-0"
              title={t('overview.batch.deletingTooltip')}
            >
              <Loader2 className="w-3 h-3 animate-spin" aria-hidden />
              {t('overview.batch.deleting')}
            </span>
          ) : null}
          {item.is_stuck ? (
            <span
              className="inline-flex items-center gap-1 px-1.5 py-0.5 text-[10px] font-medium rounded bg-destructive/10 text-destructive shrink-0"
              title={t('overview.stuckTooltip')}
            >
              <AlertTriangle className="w-3 h-3" />
              {t('overview.stuck')}
            </span>
          ) : null}
        </div>
        <div className="flex items-center flex-wrap gap-x-2 gap-y-0.5 text-xs text-muted-foreground tabular-nums mt-0.5">
          <span>{t(`status.${item.status}` as 'status.created') || item.status}</span>
          <span aria-hidden>·</span>
          <span>{timeAgo(item.last_activity_at, t)}</span>
          {item.message_count > 0 ? (
            <>
              <span aria-hidden>·</span>
              <span>{t('overview.messages', { count: item.message_count })}</span>
            </>
          ) : null}
          {hasGit ? (
            <>
              <span aria-hidden>·</span>
              {item.ahead_count > 0 ? (
                <span
                  className="flex items-center gap-0.5 text-info"
                  title={t('overview.git.aheadTooltip', { count: item.ahead_count })}
                  aria-label={t('overview.git.aheadTooltip', { count: item.ahead_count })}
                >
                  <GitMerge className="w-3 h-3" aria-hidden />
                  <span>{item.ahead_count}</span>
                </span>
              ) : null}
              {item.changes_count > 0 ? (
                <span
                  className="flex items-center gap-0.5 text-warning"
                  title={t('overview.git.changesTooltip', { count: item.changes_count })}
                  aria-label={t('overview.git.changesTooltip', { count: item.changes_count })}
                >
                  <FileEdit className="w-3 h-3" aria-hidden />
                  <span>{item.changes_count}</span>
                </span>
              ) : null}
            </>
          ) : null}
        </div>
      </div>
      <div className="flex flex-col items-end gap-0.5 shrink-0">
        <OwnerBadge owner={item.owner} />
        {item.creator_owner ? (
          <UserBadge user={item.creator_owner} />
        ) : (
          <span className="text-muted-foreground text-[10px]">
            {t('overview.creator.unknown')}
          </span>
        )}
      </div>
      <div className="text-right shrink-0">
        <div className="text-sm font-medium tabular-nums">
          {t('overview.tokensLabel', { tokens: formatTokens(item.input_tokens + item.output_tokens) })}
        </div>
        <div className="text-xs text-muted-foreground tabular-nums">
          {t('overview.messagesLabel', { user: item.user_message_count, ai: item.ai_message_count })}
        </div>
      </div>
      {selectMode ? null : (
        <ExternalLink className="w-3.5 h-3.5 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity" aria-hidden />
      )}
    </>
  );

  // In select mode the row is a toggle, not a navigation link. Deleting rows
  // stay inert (they're already being removed) so they can't be re-selected.
  if (selectMode) {
    return (
      <button
        type="button"
        disabled={isDeleting}
        onClick={() => onToggleSelect?.(item.workspace_id)}
        className="w-full flex items-center gap-3 px-3 py-2 rounded-md transition-colors text-left hover:bg-accent disabled:opacity-50 disabled:cursor-not-allowed"
      >
        {inner}
      </button>
    );
  }

  return (
    <Link
      to="/workspaces/$id"
      params={{ id: String(item.workspace_id) }}
      className="flex items-center gap-3 px-3 py-2 hover:bg-accent rounded-md transition-colors group"
    >
      {inner}
    </Link>
  );
}

export function WorkspacesOverview() {
  const { t } = useTranslation('workspaces');
  const userId = useAuthStore((s) => s.user?.id ?? 0);
  const myOrgs = useOrgStore((s) => s.myOrgs);
  const isTeamEdition = myOrgs.length > 0;
  const [ownerFilter, setOwnerFilter] = useState<OwnerRef[] | 'all'>('all');
  const ownerParam = useMemo(() => ownerToFilterParam(ownerFilter), [ownerFilter]);

  // Serialized ownerFilter for CreatorPicker and query cache key.
  const ownerFilterParam = ownerParam ?? null;

  const [creatorId, setCreatorId] = useState<number | null>(null);
  const [creatorScopeKey, setCreatorScopeKey] = useState<string | null>(ownerParam ?? null);

  // Reset creator selection when ownerFilter changes so a stale selection
  // from one scope doesn't bleed into another. React-recommended pattern:
  // compare during render + setState (instead of useEffect with setState),
  // see react.dev "Adjusting state when a prop changes".
  const currentScopeKey = ownerParam ?? null;
  if (currentScopeKey !== creatorScopeKey) {
    setCreatorScopeKey(currentScopeKey);
    setCreatorId(null);
  }

  // Picker is irrelevant when scope is personal (only the user themselves).
  const pickerHidden = ownerFilter !== 'all' && ownerFilter.length === 1 && ownerFilter[0].type === 'user';

  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: ['workspaces-overview', ownerFilterParam, creatorId],
    queryFn: () =>
      api.get<WorkspaceOverview>('/workspaces/overview', {
        params: {
          owner: ownerFilterParam ?? undefined,
          created_by: creatorId !== null ? creatorId : undefined,
        },
      }),
    refetchInterval: 60_000,
    staleTime: 30_000,
  });

  const summary = data?.summary;
  const workspaces = useMemo(() => data?.workspaces ?? [], [data]);
  const visibleWorkspaces = useMemo(
    () => workspaces.filter((w) => !w.is_archived),
    [workspaces],
  );

  // Group by project, preserving the server-side sort order: the first
  // workspace encountered with a given project pins that group's position.
  // Workspaces with no linked project fall into a trailing "no project" bucket.
  const projectGroups = useMemo(() => {
    const NO_PROJECT = '__no_project__';
    const map = new Map<string, { key: string; name: string | null; items: WorkspaceOverviewItem[] }>();
    for (const item of visibleWorkspaces) {
      const name = item.project_name?.trim();
      const key = name ? name : NO_PROJECT;
      let g = map.get(key);
      if (!g) {
        g = { key, name: name ?? null, items: [] };
        map.set(key, g);
      }
      g.items.push(item);
    }
    const groups = Array.from(map.values());
    // Force the no-project bucket (if any) to the end regardless of where its
    // first member sat in the sort.
    const noIdx = groups.findIndex((g) => g.key === NO_PROJECT);
    if (noIdx >= 0 && noIdx < groups.length - 1) {
      const [no] = groups.splice(noIdx, 1);
      groups.push(no);
    }
    return groups;
  }, [visibleWorkspaces]);

  // Expanded-group state. Auto-expand the first group on initial load only;
  // after that the user owns expansion state (refetches don't clobber it).
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(() => new Set());
  const [groupInitDone, setGroupInitDone] = useState(false);
  if (!groupInitDone && projectGroups.length > 0) {
    setGroupInitDone(true);
    setExpandedGroups(new Set([projectGroups[0].key]));
  }

  const toggleGroup = (key: string) => {
    setExpandedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  // ====== Batch delete (multi-select) ======
  const qc = useQueryClient();
  const [selectMode, setSelectMode] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(() => new Set());
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [force, setForce] = useState(false);

  const exitSelect = () => {
    setSelectMode(false);
    setSelected(new Set());
    setForce(false);
  };

  const toggleSelect = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const batchDelete = useMutation({
    mutationFn: (vars: { ids: number[]; force: boolean }) =>
      batchDeleteWorkspaces(vars.ids, vars.force),
    onSuccess: (result) => {
      qc.invalidateQueries({ queryKey: ['workspaces-overview'] });
      qc.invalidateQueries({ queryKey: ['workspaces'] });
      if (result.accepted.length > 0) {
        toast.success(t('overview.batch.deleteStarted', { count: result.accepted.length }));
      }
      const changeSkips = result.skipped.filter((s) => s.reason === 'has_changes').length;
      if (changeSkips > 0) {
        toast.warning(t('overview.batch.skippedChanges', { count: changeSkips }));
      }
      const otherSkips = result.skipped.length - changeSkips;
      if (otherSkips > 0) {
        toast.message(t('overview.batch.skippedOther', { count: otherSkips }));
      }
      setConfirmOpen(false);
      exitSelect();
    },
    onError: (e: unknown) => {
      toast.error(
        t('overview.batch.deleteFailed', {
          message: e instanceof Error ? e.message : String(e),
        }),
      );
    },
  });

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-5xl mx-auto p-6 flex flex-col gap-6">
        {/* Header */}
        <div className="flex items-center justify-between gap-3">
          <div>
            <h1 className="text-xl font-semibold text-foreground">{t('overview.title')}</h1>
            <p className="text-sm text-muted-foreground">{t('overview.subtitle')}</p>
          </div>
          <div className="flex items-center gap-2">
            {isTeamEdition ? (
              <OwnerFilter value={ownerFilter} onChange={setOwnerFilter} userId={userId} />
            ) : null}
            <CreatorPicker
              value={creatorId}
              onChange={setCreatorId}
              ownerFilter={ownerFilterParam}
              hidden={pickerHidden}
            />
          </div>
        </div>

        {/* Loading / error */}
        {isLoading ? (
          <div className="flex items-center justify-center py-12 text-sm text-muted-foreground">
            {t('common:actions.loading')}
          </div>
        ) : isError ? (
          <div className="flex flex-col items-center gap-2 py-12 text-sm">
            <span className="text-destructive">{t('overview.loadFailed')}</span>
            <Button variant="outline" size="sm" onClick={() => refetch()}>
              {t('overview.retry')}
            </Button>
          </div>
        ) : (
          <>
            {/* Top metrics */}
            <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
              <MetricCard
                label={t('overview.metrics.active')}
                value={String(summary?.active_count ?? 0)}
                hint={t('overview.metrics.activeHint', { total: summary?.total_count ?? 0 })}
                icon={Activity}
                tone="neutral"
              />
              <MetricCard
                label={t('overview.metrics.stuck')}
                value={String(summary?.stuck_count ?? 0)}
                hint={t('overview.metrics.stuckHint')}
                icon={AlertTriangle}
                tone={(summary?.stuck_count ?? 0) > 0 ? 'destructive' : 'neutral'}
              />
              <MetricCard
                label={t('overview.metrics.inputTokens')}
                value={formatTokens(summary?.input_tokens ?? 0)}
                hint={t('overview.metrics.cacheReadHint', { tokens: formatTokens(summary?.cache_read_tokens ?? 0) })}
                icon={Coins}
                tone="neutral"
              />
              <MetricCard
                label={t('overview.metrics.outputTokens')}
                value={formatTokens(summary?.output_tokens ?? 0)}
                hint={t('overview.metrics.messagesHint', { user: summary?.user_message_count ?? 0, ai: summary?.ai_message_count ?? 0 })}
                icon={Layers}
                tone="neutral"
              />
            </div>

            {/* Owner token usage trend; falls back to the caller's personal
                owner when no single-owner filter is active. */}
            <OwnerTokenTrend ownerParam={ownerFilterParam} fallbackOwner={userId ? `user:${userId}` : null} />

            {/* "需要我处理的" terminal-state issues (spec section 19). Self-hides when empty. */}
            <AttentionIssuesPanel />

            {/* Workspace list */}
            <div className="rounded-lg border bg-card">
              <div className="px-4 py-2 border-b flex items-center justify-between gap-2">
                <span className="text-sm font-medium">{t('overview.workspaceList')}</span>
                <div className="flex items-center gap-2">
                  <span className="text-xs text-muted-foreground tabular-nums">
                    {t('overview.workspaceCount', { count: visibleWorkspaces.length })}
                  </span>
                  {visibleWorkspaces.length > 0 ? (
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-7 px-2 text-xs font-normal text-muted-foreground"
                      onClick={() => (selectMode ? exitSelect() : setSelectMode(true))}
                    >
                      {selectMode ? t('overview.batch.exit') : t('overview.batch.enter')}
                    </Button>
                  ) : null}
                </div>
              </div>
              {/* Batch action bar — only in select mode. */}
              {selectMode ? (
                <div className="px-4 py-2 border-b bg-muted/30 flex items-center justify-between gap-3">
                  <span className="text-xs text-muted-foreground tabular-nums">
                    {t('overview.batch.selectedCount', { count: selected.size })}
                  </span>
                  <div className="flex items-center gap-3">
                    <label className="flex items-center gap-1.5 text-xs text-muted-foreground cursor-pointer select-none">
                      <Checkbox
                        checked={force}
                        onCheckedChange={(v) => setForce(v === true)}
                      />
                      {t('overview.batch.forceLabel')}
                    </label>
                    <Button
                      variant="destructive"
                      size="sm"
                      className="h-7 px-2 text-xs gap-1"
                      disabled={selected.size === 0 || batchDelete.isPending}
                      onClick={() => setConfirmOpen(true)}
                    >
                      <Trash2 className="w-3.5 h-3.5" aria-hidden />
                      {t('overview.batch.deleteSelected', { count: selected.size })}
                    </Button>
                  </div>
                </div>
              ) : null}
              {visibleWorkspaces.length === 0 ? (
                <div className="p-8 text-sm text-muted-foreground text-center">
                  {t('overview.empty')}
                </div>
              ) : (
                <div>
                  {projectGroups.map((group, idx) => {
                    const isExpanded = expandedGroups.has(group.key);
                    const headerLabel = group.name ?? t('overview.noProject');
                    return (
                      <div
                        key={group.key}
                        className={idx > 0 ? 'border-t' : undefined}
                      >
                        <button
                          type="button"
                          onClick={() => toggleGroup(group.key)}
                          aria-expanded={isExpanded}
                          aria-label={t(
                            isExpanded
                              ? 'overview.group.collapseAria'
                              : 'overview.group.expandAria',
                            { name: headerLabel },
                          )}
                          className="w-full flex items-center gap-2 px-4 py-1.5 bg-muted/30 hover:bg-muted/60 text-xs font-medium text-muted-foreground text-left transition-colors"
                        >
                          <ChevronRight
                            className={`w-3.5 h-3.5 shrink-0 transition-transform ${isExpanded ? 'rotate-90' : ''}`}
                            aria-hidden
                          />
                          <FolderKanban className="w-3.5 h-3.5 shrink-0" aria-hidden />
                          <span className="truncate">{headerLabel}</span>
                          <span className="ml-auto tabular-nums shrink-0">
                            {t('overview.workspaceCount', { count: group.items.length })}
                          </span>
                        </button>
                        {isExpanded ? (
                          <div className="p-1">
                            {group.items.map((item) => (
                              <WorkspaceRow
                                key={item.workspace_id}
                                item={item}
                                selectMode={selectMode}
                                selected={selected.has(item.workspace_id)}
                                onToggleSelect={toggleSelect}
                              />
                            ))}
                          </div>
                        ) : null}
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
          </>
        )}
      </div>

      <AlertDialog
        open={confirmOpen}
        onOpenChange={(o) => {
          if (batchDelete.isPending) return;
          setConfirmOpen(o);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t('overview.batch.confirmTitle', { count: selected.size })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {force
                ? t('overview.batch.confirmDescForce')
                : t('overview.batch.confirmDesc')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={batchDelete.isPending}>
              {t('common:actions.cancel')}
            </AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              disabled={batchDelete.isPending}
              onClick={(e) => {
                e.preventDefault();
                batchDelete.mutate({ ids: Array.from(selected), force });
              }}
            >
              {batchDelete.isPending
                ? t('common:actions.deleting')
                : t('overview.batch.confirmAction')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
