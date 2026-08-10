import { api } from './api';
import type { WorkspaceTask } from '@/types/workspace-task';

export const workspaceTaskApi = {
  list: (workspaceId: number) => {
    return api
      .get<{ tasks: WorkspaceTask[] }>(`/workspaces/${workspaceId}/workspace-tasks`)
      .then((r) => r.tasks);
  },
};
