import { useState, useEffect } from 'react';
import { Link, useParams, useNavigate } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { Plus, Search, Eye, EyeOff } from 'lucide-react';
import { cn } from '@/lib/utils';
import { useProjectsWithStats } from '@/lib/hooks/use-projects';
import { useOwnerGrouping } from '@/lib/hooks/use-owner-grouping';
import { useAppStore } from '@/stores/app';
import { NewProjectDialog } from '@/components/dialogs/new-project-dialog';
import { OwnerGroupSection } from '@/components/shared/owner-group-section';
import { ProjectDetailPage } from './project-detail-page';
import type { Project } from '@/types/api';

// Workspace statuses to display in sidebar (filtered — hide idle/created/completed)
const visibleWsStatuses = ['attention', 'needs_review', 'running'] as const;

// Use design system semantic tokens (docs/design-system.md §2.3) instead of
// raw Tailwind palette colors. attention=destructive, needs_review=warning,
// running=success — keeps the sidebar in sync with the global token system.
const wsStatusConfigBase: Record<string, { dotClass: string; textClass: string; pulse?: boolean }> = {
  attention: { dotClass: 'bg-destructive', textClass: 'text-destructive' },
  needs_review: { dotClass: 'bg-warning', textClass: 'text-warning' },
  running: { dotClass: 'bg-success', textClass: 'text-success', pulse: true },
};

// Compute completion ratio: Done count (last column) over total.
// Returns null when there are no issues so callers can hide the bar.
function computeCompletion(stats: { column_name: string; count: number }[]): { done: number; total: number; pct: number } | null {
  if (stats.length === 0) return null;
  const total = stats.reduce((sum, s) => sum + s.count, 0);
  if (total === 0) return null;
  // Last column is conventionally "Done"; if only one column exists, nothing is "done"
  const done = stats.length > 1 ? stats[stats.length - 1].count : 0;
  return { done, total, pct: total > 0 ? Math.round((done / total) * 100) : 0 };
}

function ProjectStats({ project }: { project: Project }) {
  const { t } = useTranslation('projects');
  const wsStatusLabels: Record<string, string> = {
    attention: t('detail.stats.wsAttention'),
    needs_review: t('detail.stats.wsNeedsReview'),
    running: t('detail.stats.wsRunning'),
  };
  const issueStats = project.issue_stats;
  const wsStats = project.ws_stats;

  const hasIssues = issueStats && issueStats.length > 0;
  const hasWs = wsStats && wsStats.length > 0;

  if (!hasIssues && !hasWs) return null;

  // Issue completion data — single brand-color bar showing % done
  const completion = hasIssues ? computeCompletion(issueStats) : null;

  // Workspace status — filtered and sorted
  const filteredWs = hasWs
    ? visibleWsStatuses
        .map((status) => {
          const stat = wsStats.find((s) => s.status === status);
          return stat && stat.count > 0 ? stat : null;
        })
        .filter(Boolean) as { status: string; count: number }[]
    : [];

  return (
    <div className="mt-1.5 space-y-1">
      {/* Single-color completion progress bar */}
      {completion && (
        <div
          className="h-1 rounded-sm overflow-hidden bg-warm-muted"
          title={`${completion.done}/${completion.total} (${completion.pct}%)`}
        >
          <div
            className="h-full bg-brand transition-[width] duration-300"
            style={{ width: `${completion.pct}%` }}
          />
        </div>
      )}

      {/* Workspace status dots */}
      {filteredWs.length > 0 && (
        <div className="flex items-center gap-2 text-[10px]">
          {filteredWs.map((stat) => {
            const config = wsStatusConfigBase[stat.status];
            const label = wsStatusLabels[stat.status];
            if (!config) return null;
            return (
              <span
                key={stat.status}
                className={cn('inline-flex items-center gap-1', config.textClass)}
                title={`${label} ${stat.count}`}
              >
                <span
                  className={cn(
                    'w-1.5 h-1.5 rounded-full',
                    config.dotClass,
                    config.pulse && 'animate-pulse'
                  )}
                />
                {label} {stat.count}
              </span>
            );
          })}
        </div>
      )}
    </div>
  );
}

export function ProjectLayout() {
  const { t } = useTranslation('projects');
  const [showHidden, setShowHidden] = useState(false);
  const statusFilter = showHidden ? 'active,hidden' : 'active';
  const { projects, isLoading } = useProjectsWithStats(statusFilter);
  const { lastOpenedProject, setLastOpenedProject } = useAppStore();
  const [search, setSearch] = useState('');
  const [newProjectOpen, setNewProjectOpen] = useState(false);
  const navigate = useNavigate();

  // Filter by search, then order alphabetically by name (dictionary order).
  // Sorting the filtered list here drives both the flat list and the grouped
  // view below (useOwnerGrouping preserves item order within each group).
  const filtered = (projects ?? [])
    .filter((p) => p.name.toLowerCase().includes(search.toLowerCase()))
    .sort((a, b) => a.name.localeCompare(b.name));

  const grouping = useOwnerGrouping(filtered);

  const params = useParams({ strict: false });
  const selectedProjectId = (params as Record<string, string | undefined>).id;

  // Record last opened project
  useEffect(() => {
    if (selectedProjectId) {
      setLastOpenedProject(selectedProjectId);
    }
  }, [selectedProjectId, setLastOpenedProject]);

  // Redirect when no project selected
  useEffect(() => {
    if (selectedProjectId || isLoading || !projects) return;

    // Try last opened
    if (lastOpenedProject && projects.some((p) => String(p.id) === lastOpenedProject)) {
      navigate({ to: '/projects/$id', params: { id: lastOpenedProject }, replace: true });
      return;
    }

    // Try first item
    if (projects.length > 0) {
      navigate({ to: '/projects/$id', params: { id: String(projects[0].id) }, replace: true });
      return;
    }
  }, [selectedProjectId, isLoading, projects, lastOpenedProject, navigate]);

  const renderProjectItem = (project: Project) => {
    const isActive = selectedProjectId === String(project.id);
    const isHidden = project.status === 'hidden';
    const issueStats = project.issue_stats;
    const hasIssues = issueStats && issueStats.length > 0;
    const totalIssues = hasIssues ? issueStats.reduce((sum, s) => sum + s.count, 0) : 0;
    const doneCount = hasIssues && issueStats.length > 1
      ? issueStats[issueStats.length - 1].count
      : 0;

    // Color hierarchy (see docs/design-system.md):
    //   sidebar bg          warm-muted          (group container)
    //   project default     warm-surface        (white card, lifts above sidebar)
    //   project hover       warm-canvas         (subtle off-white for affordance)
    //   project selected    brand-soft + 3px brand left rail
    // Owner identity is conveyed by the OwnerGroupSection header (chevron +
    // icon + label color), not by tinting every project row.
    return (
      <div key={project.id} className={cn('group relative mx-1 mb-0.5', isHidden && 'opacity-50')}>
        <Link
          to="/projects/$id"
          params={{ id: String(project.id) }}
          className={cn(
            'block border-l-[3px] px-2.5 py-2 rounded-r-md transition-colors duration-150',
            isActive
              ? 'border-l-brand bg-brand-soft'
              : 'border-l-transparent bg-warm-surface hover:bg-warm-canvas hover:border-l-warm-text-muted/30'
          )}
        >
          {/* Row 1: Name + completion */}
          <div className="flex items-center justify-between gap-2 min-w-0">
            <span className={cn(
              'text-sm font-medium truncate',
              isActive ? 'text-warm-text' : 'text-warm-text'
            )}>
              {project.name}
            </span>
            <div className="flex items-center gap-1 shrink-0">
              {totalIssues > 0 && (
                <span className={cn(
                  'text-[11px] tabular-nums',
                  isActive ? 'text-warm-text-muted' : 'text-warm-text-muted/70'
                )}>
                  {t('detail.completion', { done: doneCount, total: totalIssues })}
                </span>
              )}
            </div>
          </div>

          {/* Row 2: Description */}
          {project.description && project.description.trim() && (
            <div className="text-[11px] text-warm-text-muted/70 truncate mt-0.5">
              {project.description}
            </div>
          )}

          {/* Row 3-4: Stats (progress bar + ws status) */}
          <ProjectStats project={project} />
        </Link>
      </div>
    );
  };

  return (
    <div className="flex h-full">
      {/* Left sidebar - Project list */}
      <aside className="w-64 border-r border-warm-border bg-warm-muted flex flex-col h-full">
        {/* Header — fixed 40px so it lines up exactly with the right-hand tab
            bar (project-detail-page) and the workspace header; both are h-10. */}
        <div className="flex h-10 shrink-0 items-center justify-between px-3 border-b border-warm-border">
          <span className="text-sm font-semibold text-warm-text">{t('title')}</span>
          <div className="flex items-center gap-0.5">
            <button
              className={cn(
                'p-0.5 rounded transition-colors',
                showHidden
                  ? 'text-brand hover:bg-brand-soft'
                  : 'text-warm-text-muted hover:bg-warm-border/50 hover:text-warm-text'
              )}
              aria-label={showHidden ? t('detail.hideHidden') : t('detail.showHidden')}
              title={showHidden ? t('detail.hideHidden') : t('detail.showHidden')}
              onClick={() => setShowHidden(!showHidden)}
            >
              {showHidden ? <Eye className="w-3.5 h-3.5" /> : <EyeOff className="w-3.5 h-3.5" />}
            </button>
            <button
              className="p-0.5 rounded hover:bg-warm-border/50 text-warm-text-muted hover:text-warm-text transition-colors"
              aria-label={t('detail.newProject')}
              title={t('detail.newProject')}
              onClick={() => setNewProjectOpen(true)}
            >
              <Plus className="w-4 h-4" />
            </button>
          </div>
        </div>

        {/* Search */}
        <div className="px-2 py-1.5 border-b border-warm-border">
          <div className="flex items-center gap-1.5 bg-warm-surface border border-warm-border rounded-md px-2 py-1">
            <Search className="w-3.5 h-3.5 text-warm-text-muted shrink-0" />
            <input
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder={t('list.searchPlaceholder')}
              className="flex-1 text-xs outline-none bg-transparent text-warm-text placeholder:text-warm-text-muted"
            />
          </div>
        </div>

        {/* Project list */}
        <div className="flex-1 overflow-y-auto py-1.5">
          {isLoading ? (
            <div className="px-3 py-4 text-xs text-warm-text-muted text-center">{t('common:actions.loading')}</div>
          ) : filtered.length === 0 ? (
            <div className="px-3 py-4 text-xs text-warm-text-muted text-center">
              {search ? t('common:status.noMatchingResults') : t('list.empty')}
            </div>
          ) : (
            grouping.mode === 'flat' ? (
              filtered.map(renderProjectItem)
            ) : (
              grouping.groups.map((g) => (
                <OwnerGroupSection
                  key={g.key}
                  ownerKey={g.key}
                  label={g.label}
                  icon={g.icon}
                  count={g.items.length}
                  storageKey={`projects:${g.key}`}
                >
                  {g.items.map(renderProjectItem)}
                </OwnerGroupSection>
              ))
            )
          )}
        </div>
      </aside>

      {/* Right content - Project detail */}
      <div className="flex-1 min-w-0 flex flex-col">
        {selectedProjectId ? (
          <ProjectDetailPage projectId={selectedProjectId} />
        ) : isLoading ? (
          <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
            {t('common:actions.loading')}
          </div>
        ) : (projects ?? []).length === 0 ? (
          <div className="flex flex-col items-center justify-center h-full gap-3 text-muted-foreground">
            <p className="text-sm">{t('list.empty')}</p>
            <button
              onClick={() => setNewProjectOpen(true)}
              className="inline-flex items-center gap-1.5 px-3 py-1.5 text-sm bg-info text-white rounded-md hover:bg-info/90 transition-colors"
            >
              <Plus className="w-4 h-4" />
              {t('list.createProject')}
            </button>
          </div>
        ) : null}
      </div>

      <NewProjectDialog open={newProjectOpen} onOpenChange={setNewProjectOpen} />
    </div>
  );
}
