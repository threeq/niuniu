import { useState, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Zap, ChevronDown, ChevronUp, ChevronRight } from 'lucide-react';
import { cn } from '@/lib/utils';
import { useAuthStore } from '@/stores/auth-store';
import { PriorityCard } from './workspace-sidebar-priority-card';
import { getPriorityKind, PRIORITY_KINDS, type PriorityKind } from './workspace-sidebar-helpers';
import type { Workspace, Project } from '@/types/api';

// One user-namespaced bag for all priority-zone UI state, so future additions
// don't keep growing the localStorage key surface. Each user gets their own
// slot — switching accounts in the same browser must not leak preferences.
const PRIORITY_PREFS_KEY = 'sidebarPriority';

const KIND_LABEL_I18N: Record<PriorityKind, string> = {
  not_started: 'sidebar.priority.notStarted',
  needs_review: 'sidebar.priority.needsReview',
  running: 'sidebar.priority.running',
  attention: 'sidebar.priority.attention',
};

// Single accent for the active filter pill — semantic color stays on the
// card-level status chip so this row reads as a control, not a legend.
const ACTIVE_PILL_CLASS = 'bg-brand/15 text-brand border-brand/30';

interface PriorityPrefs {
  expanded: boolean;        // >3-items: show all vs cap to 3
  zoneCollapsed: boolean;   // whole-zone toggle (only header visible)
  enabledKinds: PriorityKind[]; // status filter; empty array treated as "all"
}

function readPrefs(uid: number): PriorityPrefs {
  const defaults: PriorityPrefs = {
    expanded: false,
    zoneCollapsed: false,
    enabledKinds: [...PRIORITY_KINDS],
  };
  try {
    const raw = localStorage.getItem(`${PRIORITY_PREFS_KEY}:u${uid}`);
    if (!raw) return defaults;
    const parsed = JSON.parse(raw) as Partial<PriorityPrefs> | null;
    if (!parsed || typeof parsed !== 'object') return defaults;
    const enabled = Array.isArray(parsed.enabledKinds)
      ? parsed.enabledKinds.filter((k): k is PriorityKind => (PRIORITY_KINDS as readonly string[]).includes(k))
      : [];
    return {
      expanded: typeof parsed.expanded === 'boolean' ? parsed.expanded : defaults.expanded,
      zoneCollapsed: typeof parsed.zoneCollapsed === 'boolean' ? parsed.zoneCollapsed : defaults.zoneCollapsed,
      enabledKinds: enabled.length > 0 ? enabled : defaults.enabledKinds,
    };
  } catch {
    return defaults;
  }
}

function writePrefs(uid: number, prefs: PriorityPrefs): void {
  try {
    localStorage.setItem(`${PRIORITY_PREFS_KEY}:u${uid}`, JSON.stringify(prefs));
  } catch {
    /* ignore */
  }
}

export interface PriorityZoneProps {
  workspaces: Workspace[];                          // 父组件已过滤 completed
  projectsByKey: Map<string, Project>;              // 按 owner-scoped key 查 Project（避免跨 owner 同名碰撞）
  projectKeyForWs: (ws: Workspace) => string;       // 父透传的 key 计算函数（与 byProject map 用同一份逻辑）
  activeWorkspaceId?: string;
  onMarkDone: (id: string | number) => void;
  /** From useMarkWorkspaceDone — workspaces whose mark-done request is
   * currently in flight. Each card looks itself up to disable its
   * complete-button while the network round-trip is pending. */
  pendingIds: Set<string>;
}

export function PriorityZone({ workspaces, projectsByKey, projectKeyForWs, activeWorkspaceId, onMarkDone, pendingIds }: PriorityZoneProps) {
  const { t } = useTranslation('workspaces');
  // 0 covers the anonymous/unauthenticated branch; the user-namespaced key
  // still works (`...u0`) so storage state is consistent across mount paths.
  const currentUserId = useAuthStore((s) => s.user?.id ?? 0);

  // Pre-compute kind once per workspace so downstream `counts` / `filtered`
  // never need to re-run getPriorityKind.
  const sortedWithKind = useMemo(
    () =>
      workspaces
        .map((ws) => ({ ws, kind: getPriorityKind(ws) }))
        .filter((x): x is { ws: Workspace; kind: PriorityKind } => x.kind !== null)
        .sort((a, b) => Number(b.ws.id) - Number(a.ws.id)),
    [workspaces],
  );

  // Unified user-namespaced prefs (replaces three legacy boolean/array keys).
  // We hold the full object in one useState so any single mutation can persist
  // atomically without round-tripping the other two values out of localStorage.
  const [prefs, setPrefs] = useState<PriorityPrefs>(() => readPrefs(currentUserId));
  const updatePrefs = (partial: Partial<PriorityPrefs>) => {
    setPrefs((prev) => {
      const next = { ...prev, ...partial };
      writePrefs(currentUserId, next);
      return next;
    });
  };

  const selectedKinds = useMemo(() => new Set(prefs.enabledKinds), [prefs.enabledKinds]);

  const toggleKind = (kind: PriorityKind) => {
    // Click the only-active filter to clear the filter (re-enable all).
    if (selectedKinds.size === 1 && selectedKinds.has(kind)) {
      updatePrefs({ enabledKinds: [...PRIORITY_KINDS] });
      return;
    }
    const next = new Set(selectedKinds);
    if (next.has(kind)) next.delete(kind);
    else next.add(kind);
    updatePrefs({ enabledKinds: next.size === 0 ? [...PRIORITY_KINDS] : Array.from(next) });
  };

  const counts = useMemo(() => {
    const c: Record<PriorityKind, number> = { attention: 0, needs_review: 0, running: 0, not_started: 0 };
    for (const { kind } of sortedWithKind) c[kind] += 1;
    return c;
  }, [sortedWithKind]);

  const filtered = useMemo(
    () => sortedWithKind.filter(({ kind }) => selectedKinds.has(kind)).map(({ ws }) => ws),
    [sortedWithKind, selectedKinds],
  );

  if (sortedWithKind.length === 0) return null;

  const visible = prefs.expanded ? filtered : filtered.slice(0, 3);
  const showToggle = filtered.length > 3;

  return (
    <div
      className="px-3.5 py-2.5 border-y border-warm-border
                 bg-gradient-to-b from-destructive/[0.04] to-transparent"
    >
      <div className={cn('flex items-center justify-between', !prefs.zoneCollapsed && 'mb-2')}>
        <button
          type="button"
          onClick={() => updatePrefs({ zoneCollapsed: !prefs.zoneCollapsed })}
          className="inline-flex items-center gap-1.5 text-[11px] font-semibold
                     uppercase tracking-wider text-foreground hover:text-foreground/80 transition-colors"
          aria-expanded={!prefs.zoneCollapsed}
        >
          {prefs.zoneCollapsed
            ? <ChevronRight className="w-3 h-3 text-muted-foreground" />
            : <ChevronDown className="w-3 h-3 text-muted-foreground" />}
          <Zap className="w-3 h-3 text-warning" />
          {t('sidebar.priority.zoneTitle')}
          <span className="px-1.5 py-0.5 rounded-full bg-warning/15 text-warning
                           text-[10px] font-semibold">
            {sortedWithKind.length}
          </span>
        </button>
        {!prefs.zoneCollapsed && (showToggle ? (
          <button
            type="button"
            onClick={() => updatePrefs({ expanded: !prefs.expanded })}
            className="inline-flex items-center gap-0.5 text-[10px] text-muted-foreground hover:text-foreground transition-colors"
          >
            {prefs.expanded
              ? <><ChevronUp className="w-3 h-3" />{t('sidebar.priority.collapse')}</>
              : <><ChevronDown className="w-3 h-3" />{t('sidebar.priority.expandAll')}</>}
          </button>
        ) : (
          <span className="text-[10px] text-muted-foreground">{t('sidebar.priority.sortHint')}</span>
        ))}
      </div>
      {!prefs.zoneCollapsed && (
        <>
          <div
            className="mb-2 flex items-center gap-1 flex-wrap"
            role="group"
            aria-label={t('sidebar.priority.filterLabel')}
          >
            {PRIORITY_KINDS.map((kind) => {
              const active = selectedKinds.has(kind);
              const count = counts[kind];
              // A 0-count pill has no toggle effect (filtering it in/out shows
              // the same set of cards), so treat as non-interactive — prevents
              // the "I clicked but nothing happened" perception.
              const interactable = count > 0;
              return (
                <button
                  key={kind}
                  type="button"
                  onClick={() => { if (interactable) toggleKind(kind); }}
                  aria-pressed={active}
                  aria-disabled={!interactable}
                  tabIndex={interactable ? 0 : -1}
                  className={cn(
                    'inline-flex items-center gap-1 px-1.5 py-0.5 rounded-md',
                    'text-[10px] font-medium border transition-colors',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40',
                    active && interactable
                      ? ACTIVE_PILL_CLASS
                      : 'border-warm-border bg-transparent text-muted-foreground hover:bg-accent',
                    !interactable && 'opacity-50 cursor-not-allowed hover:bg-transparent',
                  )}
                >
                  <span>{t(KIND_LABEL_I18N[kind])}</span>
                  <span className="tabular-nums">{count}</span>
                </button>
              );
            })}
          </div>
          {filtered.length === 0 ? (
            <div
              className="text-[10px] text-muted-foreground italic px-1 py-1"
              role="status"
              aria-live="polite"
            >
              {t('sidebar.priority.filteredEmpty')}
            </div>
          ) : (
            <div className="space-y-1.5">
              {visible.map((ws) => (
                <PriorityCard
                  key={ws.id}
                  ws={ws}
                  project={projectsByKey.get(projectKeyForWs(ws))}
                  isActive={String(activeWorkspaceId) === String(ws.id)}
                  onMarkDone={onMarkDone}
                  isPending={pendingIds.has(String(ws.id))}
                />
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}
