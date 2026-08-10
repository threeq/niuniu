import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useParams } from '@tanstack/react-router';
import { Plus, Search, Archive, LayoutDashboard, Inbox, PanelLeftClose, Loader2, ListChecks, Trash2 } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { cn } from '@/lib/utils';
import { useWorkspaces, useMarkWorkspaceDone, useWorkspaceContentSearch } from '@/lib/hooks/use-workspaces';
import { useProjectsWithStats } from '@/lib/hooks/use-projects';
import { NewWorkspaceDialog } from '@/components/dialogs/new-workspace-dialog';
import { ArchivedWorkspacesDialog } from '@/components/dialogs/archived-workspaces-dialog';
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
import { useAuthStore } from '@/stores/auth-store';
import { useWorkspacePanelStore } from '@/stores/workspace-panel-store';
import { usePinnedWorkspacesStore } from '@/stores/pinned-workspaces-store';
import type { Workspace, Project } from '@/types/api';
import { PinnedZone } from './workspace-sidebar-pinned-zone';
import { PriorityZone } from './workspace-sidebar-priority-zone';
import { WorkspaceVirtualList } from './workspace-sidebar-virtual-list';
import { buildDescendantMap } from './workspace-tree';

const SCOPE_STORAGE_KEY = 'workspaceSidebarScope';

const MIN_SIDEBAR_WIDTH = 200;
const MAX_SIDEBAR_WIDTH = 480;

export function WorkspaceSidebar() {
  const { t } = useTranslation('workspaces');
  const sidebarWidth = useWorkspacePanelStore((s) => s.sidebarWidth);
  const setSidebarWidth = useWorkspacePanelStore((s) => s.setSidebarWidth);
  const toggleSidebar = useWorkspacePanelStore((s) => s.toggleSidebar);

  // Drag-to-resize. Track in a ref so a single mousedown captures a closure
  // over the starting state instead of re-reading store on every mousemove.
  const dragStateRef = useRef<{ startX: number; startWidth: number } | null>(null);
  const [isDragging, setIsDragging] = useState(false);
  const handleResizeMouseDown = useCallback(
    (e: React.MouseEvent<HTMLDivElement>) => {
      e.preventDefault();
      dragStateRef.current = { startX: e.clientX, startWidth: sidebarWidth };
      setIsDragging(true);
    },
    [sidebarWidth]
  );
  useEffect(() => {
    if (!isDragging) return;
    const onMove = (e: MouseEvent) => {
      const s = dragStateRef.current;
      if (!s) return;
      setSidebarWidth(s.startWidth + (e.clientX - s.startX));
    };
    const onUp = () => {
      dragStateRef.current = null;
      setIsDragging(false);
    };
    document.addEventListener('mousemove', onMove);
    document.addEventListener('mouseup', onUp);
    // Disable text selection / native cursor flicker while dragging.
    const prevCursor = document.body.style.cursor;
    const prevSelect = document.body.style.userSelect;
    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';
    return () => {
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);
      document.body.style.cursor = prevCursor;
      document.body.style.userSelect = prevSelect;
    };
  }, [isDragging, setSidebarWidth]);

  // ====== 1. State: scope, search ======
  const [scope, setScope] = useState<'mine' | 'all'>(() => {
    try {
      const saved = localStorage.getItem(SCOPE_STORAGE_KEY);
      return saved === 'all' || saved === 'mine' ? saved : 'mine';
    } catch {
      return 'mine';
    }
  });
  const setScopePersist = useCallback((v: 'mine' | 'all') => {
    setScope(v);
    try {
      localStorage.setItem(SCOPE_STORAGE_KEY, v);
    } catch {
      /* private mode / quota — in-memory state still works */
    }
  }, []);

  const [search, setSearch] = useState('');
  // Debounced copy of `search` — drives the backend conversation-content lookup
  // so we don't fire a query on every keystroke. Name/id filtering stays
  // instant off `search`; only content matching waits for the debounce.
  const [debouncedSearch, setDebouncedSearch] = useState('');
  useEffect(() => {
    const id = setTimeout(() => setDebouncedSearch(search), 250);
    return () => clearTimeout(id);
  }, [search]);
  const { contentMatchIds, isSearching } = useWorkspaceContentSearch(debouncedSearch);
  const [newWsOpen, setNewWsOpen] = useState(false);
  const [archivedOpen, setArchivedOpen] = useState(false);

  // ====== 2. Data hooks ======
  const { workspaces, isLoading, batchDeleteWorkspaces, isBatchDeleting } = useWorkspaces({
    createdBy: scope === 'mine' ? 'me' : 'all',
  });
  const { projects } = useProjectsWithStats('active,hidden');
  const currentUserId = useAuthStore((s) => s.user?.id ?? 0);
  // Load this user's pinned-workspace ids (per-user localStorage) so the pinned
  // zone and every card's pin toggle read from a shared, user-scoped source.
  const loadPinnedForUser = usePinnedWorkspacesStore((s) => s.loadForUser);
  useEffect(() => {
    loadPinnedForUser(currentUserId);
  }, [currentUserId, loadPinnedForUser]);
  // Mutation hook lifted to root → single pending/prevByKey Map shared across all cards.
  // pendingIds: workspaces currently in the 4.5s undo window — passed down to
  // PriorityZone → PriorityCard so the complete button shows disabled+dim state.
  const { trigger: handleMarkDone, pendingIds } = useMarkWorkspaceDone(scope);

  // Router params — note: route is /workspaces/$id, so the key is "id".
  const params = useParams({ strict: false });
  const activeId = (params as Record<string, string | undefined>).id;

  // ====== 3. Filtered list (search only — server-side scope filter already applied) ======
  // A workspace matches when the keyword is in its name/id (instant, client-side)
  // OR it appears in the conversation content the user typed (contentMatchIds,
  // resolved by the debounced backend lookup).
  const filtered = useMemo(() => {
    const wss = workspaces ?? [];
    if (!search.trim()) return wss;
    const q = search.toLowerCase();
    return wss.filter(
      (ws) =>
        ws.name.toLowerCase().includes(q) ||
        String(ws.id).includes(q) ||
        contentMatchIds.has(String(ws.id))
    );
  }, [workspaces, search, contentMatchIds]);

  // Shared scroll container ref — powers the workspace list's virtualization
  // (only windows when this has a real measured height and the list is large).
  const scrollRef = useRef<HTMLDivElement>(null);

  // ====== 3b. Batch select / delete ======
  const [selectMode, setSelectMode] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(() => new Set());
  const [force, setForce] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);

  // parent ws id -> its direct child ws ids, so selecting a parent cascades to
  // its children (and deselecting a parent clears them too).
  const descendantMap = useMemo(() => buildDescendantMap(filtered), [filtered]);

  const exitSelect = useCallback(() => {
    setSelectMode(false);
    setSelected(new Set());
    setForce(false);
  }, []);

  const toggleSelect = useCallback(
    (id: string) => {
      const n = Number(id);
      setSelected((prev) => {
        const next = new Set(prev);
        const willSelect = !prev.has(n);
        const group = [n, ...(descendantMap.get(id) ?? []).map(Number)];
        for (const g of group) {
          if (willSelect) next.add(g);
          else next.delete(g);
        }
        return next;
      });
    },
    [descendantMap]
  );

  const handleBatchDelete = useCallback(async () => {
    try {
      const result = await batchDeleteWorkspaces({
        ids: Array.from(selected).map(String),
        force,
      });
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
    } catch (e) {
      toast.error(
        t('overview.batch.deleteFailed', {
          message: e instanceof Error ? e.message : String(e),
        })
      );
    }
  }, [batchDeleteWorkspaces, selected, force, t, exitSelect]);

  // ====== 4. Owner-scoped project keys ======
  // Key = `${owner_type}:${owner_id}:${name}` — prevents cross-owner same-name collisions
  // (e.g. two orgs each owning a "Backend" project; without owner scoping their
  // workspaces and colors would mix).
  const projectKeyForWs = useCallback((ws: Workspace): string => {
    // Orphan workspace (project deleted / LEFT JOIN data missing): collapse all
    // orphans into a single "(unknown)" group — they share no real project
    // identity, so showing one bucket is more legible than fragmenting per id.
    if (!ws.project_name) return `orphan:unknown`;
    const ot = ws.project_owner_type ?? 'user';
    const oid = ws.project_owner_id ?? 0;
    return `${ot}:${oid}:${ws.project_name}`;
  }, []);
  const projectKeyForProj = useCallback((p: Project): string => {
    const ot = p.owner?.type ?? 'user';
    const oid = p.owner?.id ?? 0;
    return `${ot}:${oid}:${p.name}`;
  }, []);

  // ====== 5. Group workspaces by project key + project lookup map ======
  const byProject = useMemo(() => {
    const m = new Map<string, Workspace[]>();
    filtered.forEach((ws) => {
      const key = projectKeyForWs(ws);
      if (!m.has(key)) m.set(key, []);
      m.get(key)!.push(ws);
    });
    return m;
  }, [filtered, projectKeyForWs]);

  const projectsByKey = useMemo(() => {
    const m = new Map<string, Project>();
    (projects ?? []).forEach((p) => m.set(projectKeyForProj(p), p));
    return m;
  }, [projects, projectKeyForProj]);

  // Resolve a Project for a group key, falling back to a minimal shape rebuilt
  // from the first workspace when the projects fetch lags or the project is
  // hidden/deleted while its workspaces linger. id:-1 is a "do not mutate"
  // sentinel — guard any /projects/${id} click-through with `id >= 0`.
  const getProjectForKey = useCallback(
    (projKey: string, wss: Workspace[]): Project => {
      const project = projectsByKey.get(projKey);
      if (project) return project;
      const firstWs = wss[0];
      return {
        id: -1,
        name: firstWs?.project_name ?? '(unknown)',
        description: null,
        status: 'active',
        color: null,
        owner: firstWs
          ? {
              type: firstWs.project_owner_type ?? 'user',
              id: firstWs.project_owner_id ?? 0,
              name: firstWs.project_owner_name ?? '',
              slug: '',
            }
          : undefined,
        created_at: '',
        updated_at: '',
      };
    },
    [projectsByKey],
  );

  // Ordered project key list: prefer projects fetch order; append orphans (workspaces
  // whose project is not in `projects` — e.g. project hidden/deleted but workspace lingers).
  // Order the project groups alphabetically by group (project) name, in
  // dictionary order. Group display name comes from the project fetch, falling
  // back to the first workspace's project_name for orphan groups.
  const orderedProjectKeys = useMemo(() => {
    const groupName = (k: string): string => {
      const p = projectsByKey.get(k);
      if (p) return p.name;
      return byProject.get(k)?.[0]?.project_name ?? k;
    };
    return Array.from(byProject.keys()).sort((a, b) =>
      groupName(a).localeCompare(groupName(b)),
    );
  }, [byProject, projectsByKey]);

  // ====== 6. Project expansion state (user-namespaced localStorage) ======
  const storageNs = `u${currentUserId}`;
  const [projectExpansion, setProjectExpansion] = useState<Record<string, boolean>>(() => {
    try {
      const raw = localStorage.getItem(`sidebarProjectCollapse:${storageNs}`);
      return raw ? JSON.parse(raw) : {};
    } catch {
      return {};
    }
  });

  // Active workspace's project key — used as smart-default expansion.
  const activeProjectKey = useMemo(() => {
    const active = (workspaces ?? []).find((ws) => String(ws.id) === String(activeId));
    if (!active) return undefined;
    return projectKeyForWs(active);
  }, [workspaces, activeId, projectKeyForWs]);

  // Parent-child collapse — persisted per user, keyed by parent ws id. Default
  // is expanded (children shown); a truthy entry means the parent is collapsed.
  const [parentCollapse, setParentCollapse] = useState<Record<string, boolean>>(() => {
    try {
      const raw = localStorage.getItem(`sidebarParentCollapse:${storageNs}`);
      return raw ? JSON.parse(raw) : {};
    } catch {
      return {};
    }
  });
  const isParentCollapsed = useCallback(
    (parentId: string) => !!parentCollapse[parentId],
    [parentCollapse]
  );
  const toggleParentCollapse = useCallback(
    (parentId: string) => {
      setParentCollapse((prev) => {
        const next = { ...prev, [parentId]: !prev[parentId] };
        try {
          localStorage.setItem(`sidebarParentCollapse:${storageNs}`, JSON.stringify(next));
        } catch {
          /* ignore */
        }
        return next;
      });
    },
    [storageNs]
  );

  const isProjectExpanded = useCallback(
    (projKey: string) => {
      // Select mode → expand every project so any workspace can be selected.
      if (selectMode) return true;
      // Search active → auto-expand any project with matches.
      if (search.trim()) {
        return (byProject.get(projKey) ?? []).length > 0;
      }
      // User-toggled state wins.
      if (projKey in projectExpansion) return projectExpansion[projKey];
      // Default: only the active workspace's project is expanded.
      return activeProjectKey === projKey;
    },
    [selectMode, search, byProject, projectExpansion, activeProjectKey]
  );

  const toggleProject = useCallback(
    (projKey: string) => {
      setProjectExpansion((prev) => {
        const curr = prev[projKey] ?? activeProjectKey === projKey;
        const next = { ...prev, [projKey]: !curr };
        try {
          localStorage.setItem(`sidebarProjectCollapse:${storageNs}`, JSON.stringify(next));
        } catch {
          /* ignore */
        }
        return next;
      });
    },
    [storageNs, activeProjectKey]
  );

  // ====== 7. Render ======
  return (
    <aside
      className="relative border-r border-warm-border bg-warm-muted flex flex-col h-full shrink-0"
      style={{ width: sidebarWidth }}
    >
      {/* [1] Header — fixed 40px to match the workspace toolbar (h-10) on the
          right, so the sidebar title and the content header share one baseline. */}
      <div className="flex h-10 shrink-0 items-center justify-between px-3 border-b">
        <span className="text-sm font-semibold text-foreground">{t('sidebar.title')}</span>
        <div className="flex items-center gap-1 text-muted-foreground">
          <Link
            to="/workspaces/overview"
            className="p-0.5 rounded hover:bg-accent hover:text-foreground transition-colors"
            aria-label={t('sidebar.kanbanOverview')}
            title={t('sidebar.kanbanOverview')}
            activeProps={{ className: 'p-0.5 rounded bg-accent text-foreground' }}
          >
            <LayoutDashboard className="w-4 h-4" />
          </Link>
          <button
            type="button"
            onClick={() => (selectMode ? exitSelect() : setSelectMode(true))}
            className={cn(
              'p-0.5 rounded transition-colors',
              selectMode
                ? 'bg-accent text-foreground'
                : 'hover:bg-accent hover:text-foreground'
            )}
            aria-pressed={selectMode}
            aria-label={t(selectMode ? 'overview.batch.exit' : 'overview.batch.enter')}
            title={t(selectMode ? 'overview.batch.exit' : 'overview.batch.enter')}
          >
            <ListChecks className="w-4 h-4" />
          </button>
          <button
            type="button"
            onClick={() => setArchivedOpen(true)}
            className="p-0.5 rounded hover:bg-accent hover:text-foreground transition-colors"
            aria-label={t('sidebar.archived')}
            title={t('sidebar.archived')}
          >
            <Archive className="w-4 h-4" />
          </button>
          <button
            type="button"
            onClick={() => setNewWsOpen(true)}
            className="p-0.5 rounded hover:bg-accent hover:text-foreground transition-colors"
            aria-label={t('sidebar.newWorkspace')}
            title={t('sidebar.newWorkspace')}
          >
            <Plus className="w-4 h-4" />
          </button>
          <button
            type="button"
            onClick={toggleSidebar}
            className="p-0.5 rounded hover:bg-accent hover:text-foreground transition-colors"
            aria-label={t('sidebar.collapse.fold')}
            title={t('sidebar.collapse.fold')}
          >
            <PanelLeftClose className="w-4 h-4" />
          </button>
        </div>
      </div>

      {/* [2] Toolbar: scope segmented control + search */}
      <div className="px-3 py-2 flex items-center gap-2 border-b">
        <div className="inline-flex bg-background border rounded-md p-0.5 text-[11px]">
          <button
            type="button"
            onClick={() => setScopePersist('mine')}
            className={cn(
              'px-3 py-1 rounded transition-colors',
              scope === 'mine' ? 'bg-accent text-foreground' : 'text-muted-foreground'
            )}
          >
            {t('sidebar.scope.mine')}
          </button>
          <button
            type="button"
            onClick={() => setScopePersist('all')}
            className={cn(
              'px-3 py-1 rounded transition-colors',
              scope === 'all' ? 'bg-accent text-foreground' : 'text-muted-foreground'
            )}
          >
            {t('sidebar.scope.all')}
          </button>
        </div>
        <div className="flex-1 relative">
          <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted-foreground" />
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('sidebar.searchPlaceholder')}
            className="w-full bg-background border rounded-md text-xs pl-7 pr-7 py-1
                       placeholder:text-muted-foreground outline-none"
          />
          {/* Non-blocking content-search indicator. Sits inside the input on the
              right so it never disables typing or covers other controls; only
              shown while the (debounced, >=4 char) backend lookup is in flight. */}
          {isSearching && (
            <Loader2
              className="absolute right-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5
                         text-muted-foreground animate-spin"
              aria-label={t('sidebar.search.searching')}
            />
          )}
        </div>
      </div>

      {/* [2b] Batch action bar — only in select mode. */}
      {selectMode && (
        <div className="px-3 py-2 border-b bg-muted/30 flex items-center justify-between gap-2">
          <span className="text-xs text-muted-foreground tabular-nums shrink-0">
            {t('overview.batch.selectedCount', { count: selected.size })}
          </span>
          <div className="flex items-center gap-2 min-w-0">
            <label className="flex items-center gap-1.5 text-[11px] text-muted-foreground cursor-pointer select-none">
              <Checkbox checked={force} onCheckedChange={(v) => setForce(v === true)} />
              {t('overview.batch.forceLabel')}
            </label>
            <Button
              variant="destructive"
              size="sm"
              className="h-7 px-2 text-xs gap-1 shrink-0"
              disabled={selected.size === 0 || isBatchDeleting}
              onClick={() => setConfirmOpen(true)}
            >
              <Trash2 className="w-3.5 h-3.5" aria-hidden />
              {t('overview.batch.deleteSelected', { count: selected.size })}
            </Button>
          </div>
        </div>
      )}

      {/* [3] Scroll region — priority zone + project sections share a single
          scrollbar so "需要我关注" scrolls away with the list instead of
          permanently squeezing it above a separate, shorter scroll area. */}
      <div ref={scrollRef} className="flex-1 min-h-0 overflow-y-auto">
        {/* Pinned zone — user-pinned workspaces float to the very top, above the
            priority zone. Shown in both scopes (pins are personal); hidden in
            select mode so batch management works over the flat project list. */}
        {!selectMode && (
          <PinnedZone
            workspaces={filtered}
            projectsByKey={projectsByKey}
            projectKeyForWs={projectKeyForWs}
            activeWorkspaceId={activeId}
          />
        )}

        {/* Priority zone — only in "mine" scope, exclude completed. Hidden in
            select mode so batch management works over the flat project list. */}
        {scope === 'mine' && !selectMode && (
          <PriorityZone
            workspaces={filtered.filter((ws) => ws.status !== 'completed')}
            projectsByKey={projectsByKey}
            projectKeyForWs={projectKeyForWs}
            activeWorkspaceId={activeId}
            onMarkDone={handleMarkDone}
            pendingIds={pendingIds}
          />
        )}

        {/* Project sections */}
        {isLoading ? (
          <div className="px-3 py-4 text-xs text-muted-foreground text-center">
            {t('common:actions.loading')}
          </div>
        ) : orderedProjectKeys.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-10 text-muted-foreground">
            <Inbox className="w-8 h-8 mb-2" />
            <span className="text-xs">
              {isSearching
                ? t('sidebar.search.searching')
                : search
                  ? t('sidebar.search.noMatch')
                  : t('sidebar.empty.noProjects')}
            </span>
          </div>
        ) : (
          <WorkspaceVirtualList
            scrollRef={scrollRef}
            orderedProjectKeys={orderedProjectKeys}
            byProject={byProject}
            getProject={getProjectForKey}
            activeWorkspaceId={activeId}
            selectMode={selectMode}
            selectedIds={selected}
            onToggleSelect={toggleSelect}
            isProjectExpanded={isProjectExpanded}
            onToggleProjectExpand={toggleProject}
            isParentCollapsed={isParentCollapsed}
            onToggleParentCollapse={toggleParentCollapse}
          />
        )}
      </div>

      <NewWorkspaceDialog open={newWsOpen} onOpenChange={setNewWsOpen} />
      <ArchivedWorkspacesDialog open={archivedOpen} onOpenChange={setArchivedOpen} />

      {/* Batch-delete confirmation */}
      <AlertDialog
        open={confirmOpen}
        onOpenChange={(o) => {
          if (isBatchDeleting) return;
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
            <AlertDialogCancel disabled={isBatchDeleting}>
              {t('common:actions.cancel')}
            </AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              disabled={isBatchDeleting}
              onClick={(e) => {
                e.preventDefault();
                void handleBatchDelete();
              }}
            >
              {isBatchDeleting
                ? t('common:actions.deleting')
                : t('overview.batch.confirmAction')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Drag-to-resize handle. Wide 6px hit area, thin 1px visible line. */}
      <div
        role="separator"
        aria-orientation="vertical"
        aria-label={t('sidebar.resize.handle')}
        aria-valuemin={MIN_SIDEBAR_WIDTH}
        aria-valuemax={MAX_SIDEBAR_WIDTH}
        aria-valuenow={sidebarWidth}
        onMouseDown={handleResizeMouseDown}
        className={cn(
          'absolute top-0 right-0 h-full w-1.5 -mr-0.5 cursor-col-resize',
          'hover:bg-info/30 active:bg-info/40 transition-colors',
          isDragging && 'bg-info/40'
        )}
      />
    </aside>
  );
}
