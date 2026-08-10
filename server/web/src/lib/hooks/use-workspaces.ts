import { useMemo, useRef, useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { api, apiFetch, ApiError, batchDeleteWorkspaces, markWorkspaceDone, unmarkWorkspaceDone } from '@/lib/api';
import { useAppStore } from '@/stores/app';
import type { Workspace, CreateWorkspaceRequest, WorktreeChangeStatus, ArchivedWorkspace, BatchDeleteWorkspacesResult, WorkspaceGitStatus } from '@/types/api';

type UseWorkspacesOptions = {
  /** When set, sent as ?created_by=... — 'me' / 'all' / a numeric user id. */
  createdBy?: 'me' | 'all' | number;
};

// mergeWorkspaceGitStatus folds the lazily-loaded git badges (方案 B) into a
// workspace list by id, and per-worktree by name. Any consumer of GET /workspaces
// that renders changes_count/ahead_count (sidebar, kanban cards) must apply this
// after fetching GET /workspaces/sidebar-git, since the list itself is git-less.
export function mergeWorkspaceGitStatus(
  list: Workspace[] | undefined,
  git: WorkspaceGitStatus[] | undefined,
): Workspace[] | undefined {
  if (!list) return list;
  if (!git || git.length === 0) return list;
  const byId = new Map(git.map((g) => [g.workspace_id, g]));
  return list.map((ws) => {
    const g = byId.get(ws.id);
    if (!g) return ws;
    let worktrees = ws.worktrees;
    if (worktrees?.length && g.worktrees?.length) {
      const gwByName = new Map(g.worktrees.map((w) => [w.name, w]));
      worktrees = worktrees.map((w) => {
        const gw = gwByName.get(w.name);
        return gw ? { ...w, changes_count: gw.changes_count, ahead_count: gw.ahead_count } : w;
      });
    }
    return { ...ws, changes_count: g.changes_count, ahead_count: g.ahead_count, worktrees };
  });
}

/** Query path for the lazy sidebar git badges, matching the list's created_by scope. */
export function sidebarGitPath(createdBy: 'me' | 'all' | number | null): string {
  return createdBy !== null ? `/workspaces/sidebar-git?created_by=${createdBy}` : '/workspaces/sidebar-git';
}

export function useWorkspaces(options: UseWorkspacesOptions = {}) {
  const queryClient = useQueryClient();
  const { removeWorkspaceTab, openWorkspaceTabs } = useAppStore();

  const createdByParam = options.createdBy ?? null;

  const workspacesQuery = useQuery({
    queryKey: ['workspaces', createdByParam],
    queryFn: () => {
      const path = createdByParam !== null
        ? `/workspaces?created_by=${createdByParam}`
        : '/workspaces';
      return api.get<Workspace[]>(path);
    },
    retry: 1,
  });

  // Lazy git badges (方案 B): the list above is a pure DB read and returns
  // instantly; this second (expensive, O(N) git-subprocess) request computes the
  // change/ahead counts and is merged into the list below. Its key is
  // deliberately NOT under the ['workspaces'] prefix so high-frequency workspace
  // events (bg_task progress, which invalidate ['workspaces']) do NOT retrigger a
  // full git recompute. It refreshes only on git_status events and on
  // workspace create/delete (see notification-dispatcher).
  const gitStatusQuery = useQuery({
    queryKey: ['sidebar-git', createdByParam],
    queryFn: () => api.get<WorkspaceGitStatus[]>(sidebarGitPath(createdByParam)),
    enabled: !!workspacesQuery.data,
    retry: 1,
  });

  const mergedWorkspaces = useMemo<Workspace[] | undefined>(
    () => mergeWorkspaceGitStatus(workspacesQuery.data, gitStatusQuery.data),
    [workspacesQuery.data, gitStatusQuery.data],
  );

  const createWorkspaceMutation = useMutation({
    mutationFn: (data: CreateWorkspaceRequest) =>
      api.createWorkspace(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['workspaces'] });
    },
  });

  const cleanupWorkspace = (id: string) => {
    // Cancel in-flight queries first to prevent refetch race after deletion
    queryClient.cancelQueries({ queryKey: ['workspace', id] });
    queryClient.cancelQueries({ queryKey: ['workspace-tree-groups', id] });
    if (openWorkspaceTabs.includes(id)) {
      removeWorkspaceTab(id);
    }
    queryClient.removeQueries({ queryKey: ['workspace', id] });
    queryClient.removeQueries({ queryKey: ['workspace-tree-groups', id] });
  };

  const deleteWorkspaceMutation = useMutation({
    mutationFn: async (id: string): Promise<WorktreeChangeStatus[] | null> => {
      try {
        await apiFetch<void>(`/workspaces/${id}`, { method: 'DELETE', suppressError: false });
        return null;
      } catch (error) {
        if (error instanceof ApiError && error.status === 409) {
          const details = (error.body as { error?: { details?: unknown } })?.error?.details;
          return (details ?? []) as WorktreeChangeStatus[];
        }
        throw error;
      }
    },
    onSuccess: (_data, id) => {
      // Only clean up if deletion succeeded (data is null = deleted)
      if (_data === null) {
        cleanupWorkspace(id);
      }
      queryClient.invalidateQueries({ queryKey: ['workspaces'] });
    },
  });

  const forceDeleteWorkspaceMutation = useMutation({
    mutationFn: (id: string) =>
      apiFetch<void>(`/workspaces/${id}`, { method: 'DELETE', params: { force: true } }),
    onSuccess: (_data, id) => {
      cleanupWorkspace(id);
      queryClient.invalidateQueries({ queryKey: ['workspaces'] });
    },
  });

  const archiveWorkspaceMutation = useMutation({
    mutationFn: (id: string) =>
      apiFetch<void>(`/workspaces/${id}/archive`, { method: 'POST' }),
    onSuccess: (_data, id) => {
      cleanupWorkspace(id);
      queryClient.invalidateQueries({ queryKey: ['workspaces'] });
      queryClient.invalidateQueries({ queryKey: ['workspaces', 'archived'] });
    },
  });

  // batchDeleteWorkspaces is async on the server: accepted ids are marked
  // 'deleting' and cleaned up in the background, emitting a "deleted"
  // notification per id (which the dispatcher uses to drop their caches). Here
  // we only invalidate the list so the 'deleting' markers appear immediately;
  // the open tabs for accepted ids are closed so the user isn't left staring at
  // a workspace mid-deletion.
  const batchDeleteWorkspacesMutation = useMutation({
    mutationFn: ({ ids, force }: { ids: string[]; force: boolean }): Promise<BatchDeleteWorkspacesResult> =>
      batchDeleteWorkspaces(ids, force),
    onSuccess: (result) => {
      for (const id of result.accepted) {
        const sid = String(id);
        if (openWorkspaceTabs.includes(sid)) {
          removeWorkspaceTab(sid);
        }
      }
      queryClient.invalidateQueries({ queryKey: ['workspaces'] });
    },
  });

  return {
    workspaces: mergedWorkspaces,
    isLoading: workspacesQuery.isLoading,
    error: workspacesQuery.error,
    refetch: workspacesQuery.refetch,
    createWorkspace: createWorkspaceMutation.mutate,
    deleteWorkspace: deleteWorkspaceMutation.mutateAsync,
    deleteWorkspaceResult: deleteWorkspaceMutation.data,
    isCreating: createWorkspaceMutation.isPending,
    isDeleting: deleteWorkspaceMutation.isPending || forceDeleteWorkspaceMutation.isPending,
    forceDeleteWorkspace: forceDeleteWorkspaceMutation.mutate,
    archiveWorkspace: archiveWorkspaceMutation.mutateAsync,
    isArchiving: archiveWorkspaceMutation.isPending,
    batchDeleteWorkspaces: batchDeleteWorkspacesMutation.mutateAsync,
    isBatchDeleting: batchDeleteWorkspacesMutation.isPending,
  };
}

export function useArchivedWorkspaces() {
  const queryClient = useQueryClient();

  const archivedQuery = useQuery({
    queryKey: ['workspaces', 'archived'],
    queryFn: () => api.get<ArchivedWorkspace[]>('/workspaces/archived'),
    retry: 1,
  });

  const deleteArchivedMutation = useMutation({
    mutationFn: (id: string) =>
      apiFetch<void>(`/workspaces/${id}`, { method: 'DELETE', params: { force: true } }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['workspaces', 'archived'] });
    },
  });

  return {
    archivedWorkspaces: archivedQuery.data,
    isLoading: archivedQuery.isLoading,
    deleteArchived: deleteArchivedMutation.mutateAsync,
    isDeleting: deleteArchivedMutation.isPending,
  };
}

// Below this length the backend conversation-content lookup is skipped and the
// sidebar relies on its instant client-side name/id filter alone. Short strings
// match too much and make the LIKE scan over message bodies expensive for no
// real selectivity. Counted in code points (Array.from) so 4 CJK characters
// count as 4, not as their UTF-16 unit length.
export const MIN_CONTENT_SEARCH_CHARS = 4;

// useWorkspaceContentSearch — resolves the set of workspace IDs whose
// user-authored chat messages match `query`. The sidebar merges these with its
// instant name/id filter so a workspace surfaces when the keyword appears in
// the conversation the user typed, even if the title/id don't match.
//
// Pass an already-debounced query; the hook only fires once the keyword reaches
// MIN_CONTENT_SEARCH_CHARS. IDs are returned as a Set<string> because
// Workspace.id is a string while the endpoint returns numeric ids — stringifying
// both sides keeps the membership check exact.
export function useWorkspaceContentSearch(query: string) {
  const q = query.trim();
  const enabled = Array.from(q).length >= MIN_CONTENT_SEARCH_CHARS;
  const contentQuery = useQuery({
    queryKey: ['workspaces', 'content-search', q],
    queryFn: () =>
      api.get<{ workspace_ids: number[] }>(
        `/workspaces/search?q=${encodeURIComponent(q)}`
      ),
    enabled,
    staleTime: 30_000,
    retry: 1,
  });

  const contentMatchIds = useMemo(
    () => new Set((contentQuery.data?.workspace_ids ?? []).map(String)),
    [contentQuery.data]
  );

  return {
    contentMatchIds,
    // Only "searching" once the threshold is crossed; below it no request fires.
    isSearching: enabled && contentQuery.isFetching,
  };
}

export type { Workspace, CreateWorkspaceRequest, ArchivedWorkspace };

// useMarkWorkspaceDone — sidebar "mark done" mutation. Fires the POST
// /workspaces/:id/mark-done IMMEDIATELY on click so a page refresh can never
// lose the commit; the undo button on the success toast calls a separate
// /workspaces/:id/unmark-done endpoint that restores the snapshot status.
//
// History: the previous design held the API call behind a 4.5s setTimeout to
// allow client-side undo. A browser refresh inside that window dropped the
// commit silently — the user saw a success toast but the workspace stayed in
// its old state after reload. The server-side undo endpoint replaces the
// delay so the source of truth is committed before the user can leave.
//
// Cache key contract: `useWorkspaces` stores the list under
// `['workspaces', createdByParam]` where createdByParam ∈ 'me' | 'all' |
// <numeric user id> | null. The sidebar only ever shows the 'me' or 'all'
// list, so this hook accepts `scope: 'mine' | 'all'` and maps 'mine' → 'me'
// to land the optimistic patch on the SAME cache entry the sidebar reads
// from. Writing under the wrong key silently no-ops the optimistic flip.
function workspacesQueryKey(scope: 'mine' | 'all') {
  return ['workspaces', scope === 'mine' ? 'me' : 'all'] as const;
}

export function useMarkWorkspaceDone(scope: 'mine' | 'all') {
  const qc = useQueryClient();
  const { t } = useTranslation('workspaces');
  // Dedupe in-flight mark-done calls per workspace id. With an immediate API
  // call this is mostly a guard against rapid double-clicks; the set drains
  // as soon as the network round-trip resolves.
  const inflight = useRef<Set<string>>(new Set());
  const cacheKey = useMemo(() => workspacesQueryKey(scope), [scope]);
  // pendingIds remains in the public contract so PriorityCard can dim its
  // ✓ button during the brief in-flight window.
  const [pendingIds, setPendingIds] = useState<Set<string>>(() => new Set());

  const setPending = (id: string, on: boolean) => {
    setPendingIds((prev) => {
      const has = prev.has(id);
      if (on === has) return prev;
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const patchStatus = (id: string, status: string) => {
    qc.setQueryData<Workspace[]>(cacheKey, (old) =>
      old?.map((ws) => (String(ws.id) === id ? { ...ws, status } : ws))
    );
  };

  const undo = async (id: string, previousStatus: string) => {
    // Optimistic restore first so the card reappears immediately. If the
    // unmark API fails we re-patch to 'completed' and toast an error.
    patchStatus(id, previousStatus);
    try {
      await unmarkWorkspaceDone(id, previousStatus);
      qc.invalidateQueries({ queryKey: ['workspaces'] });
    } catch (err) {
      // Roll the optimistic restore back so the UI matches server truth.
      patchStatus(id, 'completed');
      if (err instanceof ApiError && err.status === 409) {
        // Workspace already moved off 'completed' on the server (e.g. another
        // tab acted on it). Don't keep faking a restored state.
        toast.error(t('sidebar.markDone.undoStale'));
        qc.invalidateQueries({ queryKey: ['workspaces'] });
      } else {
        console.error('unmark workspace done failed', err);
        toast.error(t('sidebar.markDone.undoError'));
      }
    }
  };

  const trigger = async (id: string | number) => {
    const key = String(id);
    if (inflight.current.has(key)) return; // dedupe rapid double-clicks

    const list = qc.getQueryData<Workspace[]>(cacheKey) ?? [];
    const target = list.find((ws) => String(ws.id) === key);
    if (!target) return; // not in cache — skip; the card is gone
    const previousStatus = target.status;

    inflight.current.add(key);
    setPending(key, true);
    // Optimistic: flip to completed so the card disappears from the priority
    // panel before the network call returns. If the call errors we roll
    // back below.
    patchStatus(key, 'completed');

    try {
      const result = await markWorkspaceDone(key);
      qc.invalidateQueries({ queryKey: ['workspaces'] });
      toast.success(t('sidebar.markDone.success'), {
        action: {
          label: t('sidebar.markDone.undo'),
          onClick: () => { void undo(key, previousStatus); },
        },
        duration: 5000,
      });
      if (result?.warnings?.length) {
        // Partial success — workspace flipped, but a downstream side-effect
        // (issue lifecycle) failed. Surface as a non-blocking follow-up.
        toast.warning(t('sidebar.markDone.partialSuccess'));
      }
    } catch (err) {
      // Roll the optimistic patch back to the snapshot. cancelQueries kills
      // any in-flight read so it can't briefly redraw the optimistic value.
      await qc.cancelQueries({ queryKey: cacheKey });
      patchStatus(key, previousStatus);
      if (err instanceof ApiError && err.status === 409) {
        toast.error(t('sidebar.markDone.runningError'));
      } else {
        console.error('mark workspace done failed', err);
        toast.error(t('sidebar.markDone.error'));
      }
    } finally {
      inflight.current.delete(key);
      setPending(key, false);
    }
  };

  return { trigger, pendingIds };
}
