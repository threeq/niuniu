/**
 * Extension contract — workspace-targeted toast events:
 *
 *   When adding a new workspace event that pops a toast or plays a sound,
 *   the emitter MUST attach `should_alert_user_ids: number[]` to `extra`
 *   (the deduped union of creator + linked-issue assignees, computed
 *   server-side via WorkspaceService.AlertableUserIDs). The single gate
 *   in handleToast() below uses that array to honor the user's
 *   alertScope setting. Events lacking the field default to NO alert
 *   under alertScope='mine' — this is the safe default and steers
 *   future contributors toward following this contract.
 */
import { createElement, type ReactNode } from 'react';
import type { QueryClient } from '@tanstack/react-query';
import type { NavigateOptions } from '@tanstack/react-router';
import { toast } from 'sonner';
import i18n from '@/i18n';
import { router } from '@/router';
import { playNotificationSound } from '@/lib/notification-sound';
import { useNotificationSettings } from '@/stores/notification-settings-store';
import { useAuthStore } from '@/stores/auth-store';

export interface Notification {
  topic: string;
  action: string;
  id?: number;
  extra?: Record<string, unknown>;
}

type QueryKeyMapper = (n: Notification) => (string | number | undefined)[][];

const INVALIDATION_MAP: Record<string, QueryKeyMapper> = {
  workspace: (n) => {
    const base = [['workspaces'], ['workspace', String(n.id)]];
    if (n.action === 'created' || n.action === 'deleted') {
      base.push(['workspace-tree-groups']);
      // A new/removed workspace changes the set the lazy git badges cover, so
      // refresh sidebar-git too (it is not under the ['workspaces'] prefix).
      base.push(['sidebar-git']);
    }
    if (n.action === 'archived' || n.action === 'deleted') {
      base.push(['workspaces', 'archived']);
      base.push(['workspace', 'by-issue']);
    }
    return base;
  },
  diff: (n) => [['workspace', String(n.id), 'diff']],
  git_status: (n) => [
    ['workspace-changes-summary', String(n.id)],
    ['workspace-worktree-status', String(n.id)],
    // Refresh the sidebar's lazy git badges (方案 B). Separate key prefix so the
    // whole workspace list is not refetched, and so bg_task events (which touch
    // ['workspaces']) don't trigger this O(N) recompute.
    ['sidebar-git'],
  ],
  project: (n) => {
    if (n.action === 'learnings_updated' && n.id) {
      return [['project-learnings', n.id]]
    }
    return [['projects']]
  },
  kanban: () => [['issues'], ['all-issues']],
  schedule: (n) => [['schedules'], ['workspace-schedules', String(n.id)]],
  queue: (n) => [['workspace', String(n.id), 'queue']],
  pinned: (n) => [['pinned-messages', String(n.id)]],
  workspace_bg_task: () => [['workspaces']],
};

export type ToastTarget =
  | { type: 'workspace'; id: number }
  | { type: 'project'; id: number };

export function targetRoute(t: ToastTarget): NavigateOptions {
  switch (t.type) {
    case 'workspace':
      return { to: '/workspaces/$id', params: { id: String(t.id) } };
    case 'project':
      return { to: '/projects/$id', params: { id: String(t.id) } };
    default: {
      const _exhaustive: never = t;
      return _exhaustive;
    }
  }
}

function navigateToTarget(t: ToastTarget) {
  router.navigate(targetRoute(t));
}

function workspaceTarget(id: number | undefined): ToastTarget | undefined {
  return id != null ? { type: 'workspace', id } : undefined;
}

type GroupKey = string;
const groupKey = (t: ToastTarget): GroupKey => `${t.type}:${t.id}`;

const groupToToastIds = new Map<GroupKey, Set<string>>();
let toastSeq = 0;

function dismissGroup(key: GroupKey) {
  const set = groupToToastIds.get(key);
  if (!set) return;
  for (const id of set) toast.dismiss(id);
  groupToToastIds.delete(key);
}

// Vite dev：HMR 重载时 module 被重新执行，groupToToastIds 重置为空 Map，
// 但已弹出的 sonner toast 仍存活在 DOM/sonner internal state 里。
// dispose 钩子在旧 module 卸载前主动 dismiss 残留 toast，避免开发期出现
// "点击 toast 不能批量关闭，因为 group set 已经丢"的诡异现象。
//
// Best-effort：本钩子只关掉 HMR 边界**之前**已弹出的 toast；如果用户在
// HMR 后才点击某条旧 toast，该 toast 的 group 映射已不在新 module 的 Map 里，
// 此时点击只会关掉单条（退化为现状），不会批量关闭。生产环境无 HMR，无影响。
//
// vitest 测试环境下 import.meta.hot 为 undefined，整段被跳过。
if (import.meta.hot) {
  import.meta.hot.dispose(() => {
    for (const set of groupToToastIds.values()) {
      for (const id of set) toast.dismiss(id);
    }
    groupToToastIds.clear();
  });
}

const TOAST_STATUS_SET = new Set(['needs_review', 'attention', 'failed']);

function notifySound() {
  const { soundEnabled, soundName, volume } = useNotificationSettings.getState();
  if (soundEnabled) {
    playNotificationSound(soundName, volume);
  }
}

function notifyToast(
  level: 'info' | 'success' | 'error',
  message: string,
  duration: number,
  target?: ToastTarget,
) {
  const { toastEnabled } = useNotificationSettings.getState();
  if (!toastEnabled) return;

  if (!target) {
    toast[level](message, { duration });
    return;
  }

  const key = groupKey(target);
  const toastId = `toast-${++toastSeq}`;
  let set = groupToToastIds.get(key);
  if (!set) {
    set = new Set();
    groupToToastIds.set(key, set);
  }
  set.add(toastId);

  const handleClick = () => {
    navigateToTarget(target);
    dismissGroup(key);
  };

  const cleanup = () => {
    const s = groupToToastIds.get(key);
    if (!s) return;
    s.delete(toastId);
    if (s.size === 0) groupToToastIds.delete(key);
  };

  const node: ReactNode = createElement(
    'span',
    { onClick: handleClick, className: 'block w-full cursor-pointer' },
    message,
  );

  toast[level](node, {
    id: toastId,
    duration,
    onDismiss: cleanup,
    onAutoClose: cleanup,
  });
}

function shouldAlert(n: Notification): boolean {
  const { alertScope } = useNotificationSettings.getState();
  if (alertScope === 'none') return false;
  if (alertScope === 'all')  return true;

  // 'mine': trust the event-embedded ID list.
  const me = useAuthStore.getState().user?.id;
  const ids = (n.extra as Record<string, unknown> | undefined)?.should_alert_user_ids;
  if (!Array.isArray(ids)) {
    if (import.meta.env.DEV) {
      console.warn(
        `[notify] ${n.topic}.${n.action} (id=${n.id ?? '?'}) missing extra.should_alert_user_ids; ` +
        `silently muted under alertScope='mine'. Server-side emitter must enrich via WorkspaceService.AlertableUserIDs.`,
      );
    }
    return false;
  }
  if (me == null) return false;
  return (ids as number[]).includes(me);
}

function handleToast(n: Notification) {
  if (n.topic !== 'workspace') return;
  if (!shouldAlert(n)) return;     // ← single gate; covers all workspace events

  const status = (n.extra as Record<string, unknown>)?.status as string | undefined;
  const target = workspaceTarget(n.id);
  if (!target) return;

  if (n.action === 'status_changed' && status && TOAST_STATUS_SET.has(status)) {
    notifyToast('info', `Workspace !${n.id}: ${status}`, 5000, target);
    notifySound();
    return;
  }
  if (n.action === 'agent_done') {
    if (status === 'attention') {
      notifyToast('error', i18n.t('common:notifications.agentAttention', { id: n.id }), 5000, target);
    } else {
      notifyToast('success', i18n.t('common:notifications.agentDone', { id: n.id }), 5000, target);
    }
    notifySound();
  }
}

/** Internal: reset module state for tests. Do not call from app code. */
export function __resetForTests() {
  if (import.meta.env.MODE !== 'test') {
    throw new Error('__resetForTests is for vitest only');
  }
  groupToToastIds.clear();
  toastSeq = 0;
}

export function dispatchNotification(queryClient: QueryClient, n: Notification) {
  if (n.topic === 'ping') return;

  // When a workspace is deleted, remove its queries from cache instead of
  // invalidating (which would trigger a refetch of a resource that no longer
  // exists, causing 404 errors).
  if (n.topic === 'workspace' && (n.action === 'deleted' || n.action === 'archived') && n.id != null) {
    const id = String(n.id);
    queryClient.cancelQueries({ queryKey: ['workspace', id] });
    queryClient.cancelQueries({ queryKey: ['workspace-tree-groups', id] });
    queryClient.removeQueries({ queryKey: ['workspace', id] });
    queryClient.removeQueries({ queryKey: ['workspace-tree-groups', id] });
    queryClient.invalidateQueries({ queryKey: ['workspaces'] });
    queryClient.invalidateQueries({ queryKey: ['workspaces', 'archived'] });
    queryClient.invalidateQueries({ queryKey: ['workspace', 'by-issue'] });
    queryClient.invalidateQueries({ queryKey: ['workspace-tree-groups'] });
    handleToast(n);
    return;
  }

  const mapper = INVALIDATION_MAP[n.topic];
  if (mapper) {
    const keys = mapper(n);
    for (const key of keys) {
      queryClient.invalidateQueries({ queryKey: key });
    }
  }

  handleToast(n);
}
