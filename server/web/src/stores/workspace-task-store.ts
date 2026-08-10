import { create } from 'zustand';
import type { TaskUpdatePayload, WorkspaceTaskStatus } from '@/types/workspace-task';
import { workspaceTaskApi } from '@/lib/workspace-task-api';

interface WorkspaceTaskState {
  tasks: Map<string, TaskUpdatePayload>;

  handleTaskEvent: (event: TaskUpdatePayload) => void;
  loadTasks: (workspaceId: number) => Promise<void>;
  clearTasks: () => void;

  getTasksByPhase: () => Map<string, TaskUpdatePayload[]>;
  getProgress: () => { completed: number; total: number };
  getCurrentTask: () => TaskUpdatePayload | undefined;
  hasVisibleTasks: () => boolean;
  hasInterrupted: () => boolean;
}

const PHASE_ORDER: Record<string, number> = {
  spec: 0,
  plan: 1,
  arch: 2,
  impl: 3,
  test: 4,
};

export const useWorkspaceTaskStore = create<WorkspaceTaskState>((set, get) => ({
  tasks: new Map(),

  handleTaskEvent: (event) => {
    set((state) => {
      const next = new Map(state.tasks);
      next.set(event.agentTaskId, event);
      return { tasks: next };
    });
  },

  loadTasks: async (workspaceId) => {
    try {
      const tasks = await workspaceTaskApi.list(workspaceId);
      const map = new Map<string, TaskUpdatePayload>();
      for (const t of tasks) {
        map.set(t.agent_task_id, {
          id: t.id,
          agentTaskId: t.agent_task_id,
          subject: t.subject,
          description: t.description,
          activeForm: t.active_form,
          status: t.status as WorkspaceTaskStatus,
          phase: t.phase,
          messageId: t.message_id,
          batchId: t.batch_id,
          startedAt: t.started_at ? new Date(t.started_at).getTime() : undefined,
          completedAt: t.completed_at ? new Date(t.completed_at).getTime() : undefined,
        });
      }
      set({ tasks: map });
    } catch {
      // Silently ignore — workspace may not have tasks yet
    }
  },

  clearTasks: () => set({ tasks: new Map() }),

  getTasksByPhase: () => {
    const { tasks } = get();
    const groups = new Map<string, TaskUpdatePayload[]>();
    for (const task of tasks.values()) {
      // Include deleted tasks — TaskCard handles fade-out animation
      const list = groups.get(task.phase) || [];
      list.push(task);
      groups.set(task.phase, list);
    }
    const sorted = new Map(
      [...groups.entries()].sort(([a], [b]) => {
        const oa = PHASE_ORDER[a] ?? 99;
        const ob = PHASE_ORDER[b] ?? 99;
        return oa - ob;
      }),
    );
    return sorted;
  },

  getProgress: () => {
    const { tasks } = get();
    let completed = 0;
    let total = 0;
    for (const task of tasks.values()) {
      if (task.status === 'deleted') continue;
      total++;
      if (task.status === 'completed') completed++;
    }
    return { completed, total };
  },

  getCurrentTask: () => {
    const { tasks } = get();
    for (const task of tasks.values()) {
      if (task.status === 'in_progress') return task;
    }
    return undefined;
  },

  hasVisibleTasks: () => {
    const { tasks } = get();
    for (const task of tasks.values()) {
      if (task.status !== 'deleted') return true;
    }
    return false;
  },

  hasInterrupted: () => {
    const { tasks } = get();
    for (const task of tasks.values()) {
      if (task.status === 'interrupted') return true;
    }
    return false;
  },
}));
