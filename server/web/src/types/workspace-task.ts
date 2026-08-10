export type WorkspaceTaskStatus = 'pending' | 'in_progress' | 'completed' | 'deleted' | 'interrupted';

export interface WorkspaceTask {
  id: number;
  workspace_id: number;

  agent_task_id: string;
  subject: string;
  description: string;
  active_form: string;
  status: WorkspaceTaskStatus;
  phase: string;
  message_id: string;
  batch_id: string;
  started_at: string | null;
  completed_at: string | null;
  created_at: string;
  updated_at: string;
}

export interface TaskUpdatePayload {
  id: number;
  agentTaskId: string;
  subject: string;
  description: string;
  activeForm: string;
  status: WorkspaceTaskStatus;
  phase: string;
  messageId: string;
  batchId: string;
  startedAt?: number;
  completedAt?: number;
}
