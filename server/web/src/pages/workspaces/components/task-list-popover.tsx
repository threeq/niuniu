import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { useWorkspaceTaskStore } from '@/stores/workspace-task-store';
import { TaskCard } from './task-card';

interface TaskListPopoverProps {
  onClickTask?: (messageId: string) => void;
}

const PHASE_ORDER: Record<string, number> = {
  spec: 0,
  plan: 1,
  arch: 2,
  impl: 3,
  test: 4,
};

export function TaskListPopover({ onClickTask }: TaskListPopoverProps) {
  const { t } = useTranslation('workspaces');
  // Subscribe to tasks directly so component re-renders on task changes
  const tasks = useWorkspaceTaskStore((s) => s.tasks);

  const groups = useMemo(() => {
    const map = new Map<string, typeof tasksArray>();
    const tasksArray = [...tasks.values()];
    for (const task of tasksArray) {
      if (task.status === 'deleted') continue;
      const list = map.get(task.phase) || [];
      list.push(task);
      map.set(task.phase, list);
    }
    return new Map(
      [...map.entries()].sort(([a], [b]) => {
        const oa = PHASE_ORDER[a] ?? 99;
        const ob = PHASE_ORDER[b] ?? 99;
        return oa - ob;
      }),
    );
  }, [tasks]);

  // Track manually collapsed groups; auto-expand groups with in_progress tasks
  const [manualCollapsed, setManualCollapsed] = useState<Set<string>>(new Set());

  const isGroupCollapsed = (phase: string, phaseTasks: typeof tasks extends Map<string, infer V> ? V[] : never[]) => {
    if (manualCollapsed.has(phase)) {
      // Even if manually collapsed, auto-expand if it has an active task
      return !phaseTasks.some((t) => t.status === 'in_progress');
    }
    return false;
  };

  const toggleGroup = (phase: string) => {
    setManualCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(phase)) next.delete(phase);
      else next.add(phase);
      return next;
    });
  };

  const isSingleGroup = groups.size <= 1;

  return (
    <div className="max-h-[60vh] overflow-y-auto py-1">
      {[...groups.entries()].map(([phase, phaseTasks]) => {
        const completedCount = phaseTasks.filter((t) => t.status === 'completed').length;
        const collapsed = !isSingleGroup && isGroupCollapsed(phase, phaseTasks);

        return (
          <div key={phase}>
            {!isSingleGroup && (
              <button
                onClick={() => toggleGroup(phase)}
                className="flex items-center gap-1.5 w-full px-3 py-1.5 text-xs font-medium text-muted-foreground hover:bg-muted"
              >
                {collapsed ? (
                  <ChevronRight className="w-3 h-3" />
                ) : (
                  <ChevronDown className="w-3 h-3" />
                )}
                <span>{t(`panels.taskList.phase.${phase}`, { defaultValue: phase })}</span>
                <span className="text-muted-foreground/70 ml-auto">
                  {completedCount}/{phaseTasks.length}
                </span>
              </button>
            )}

            {!collapsed &&
              phaseTasks.map((task) => (
                <TaskCard
                  key={task.agentTaskId}
                  task={task}
                  onClickTask={onClickTask}
                />
              ))}
          </div>
        );
      })}

      {groups.size === 0 && (
        <div className="px-3 py-4 text-sm text-muted-foreground/70 text-center">{t('panels.taskList.noTasks')}</div>
      )}
    </div>
  );
}
