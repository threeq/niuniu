// Conversational office-assistant API (#388). Wraps the backend
// POST /api/assistant/quick-create endpoint, which transparently provisions an
// issue + a no-repo workspace + an autohost goal_condition from one sentence.
// The SPA then subscribes to the returned workspace via agent-sse-store and
// sends the description as the first turn.
import { api } from './api';

export interface AssistantQuickCreateRequest {
  description: string;
  title?: string;
  owner?: { type: string; id: number };
}

export interface AssistantQuickCreateResponse {
  issue_id: number;
  workspace_id: number;
  project_id: number;
  column_id: number;
  issue_title: string;
}

/** Provision the backing issue + no-repo workspace for a one-sentence task. */
export async function quickCreateAssistantTask(
  body: AssistantQuickCreateRequest,
): Promise<AssistantQuickCreateResponse> {
  return api.post<AssistantQuickCreateResponse>('/assistant/quick-create', body);
}

// An uploaded attachment, in the shape the /messages endpoint expects.
export interface AssistantAttachment {
  path: string;
  name: string;
  type: string; // 'image' | 'file'
  mimeType?: string;
  size?: number;
}

/** Send a chat turn (optionally with attachments) to the workspace agent. */
export async function sendAssistantMessage(
  workspaceId: number | string,
  content: string,
  attachments?: AssistantAttachment[],
): Promise<void> {
  await api.post(`/workspaces/${workspaceId}/messages`, { content, attachments });
}

// --- Multi-plan dispatcher (#396) ---

// AssistantPlan is one plan (a task + its working area) the assistant manages.
export interface AssistantSchedule {
  id: number;
  name: string;
  cron_expr: string;
  enabled: boolean;
}

export interface AssistantPlan {
  issue_id: number;
  workspace_id: number;
  project_id: number;
  column_id: number;
  title: string;
  status: string;
  updated_at: number;
  // The top-level conversation this plan belongs to (0 = a top-level task).
  parent_issue_id: number;
  // Cron schedules bound to this plan's workspace (omitted when none).
  schedules?: AssistantSchedule[];
}

export interface AssistantDispatchResponse {
  plan: AssistantPlan;
  is_new: boolean;
}

/**
 * Route one message: the backend decides whether it continues an existing plan
 * or starts a new one, then forwards it into that plan's agent. The caller does
 * NOT also send the message — dispatch already delivered it.
 */
export async function dispatchAssistant(body: {
  description: string;
  active_plan_id?: number;
  force_new?: boolean;
}): Promise<AssistantDispatchResponse> {
  return api.post<AssistantDispatchResponse>('/assistant/dispatch', body);
}

/** List the owner's plans, newest-activity first. */
export async function listAssistantPlans(): Promise<AssistantPlan[]> {
  const res = await api.get<{ plans: AssistantPlan[] }>('/assistant/plans');
  return res.plans ?? [];
}

/** Permanently delete a plan (its working files + the task). */
export async function deleteAssistantPlan(issueId: number): Promise<void> {
  await api.delete(`/assistant/plans/${issueId}`);
}
