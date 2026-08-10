import { describe, it, expect, beforeEach, vi } from 'vitest';
import type { AssistantPlan } from '@/lib/assistant-api';

// The store subscribes plans to the SSE store and loads history via api on
// refresh; stub both so the test exercises only the selection logic.
vi.mock('@/lib/assistant-api', () => ({
  listAssistantPlans: vi.fn(),
}));
vi.mock('@/stores/agent-sse-store', () => ({
  useAgentSSEStore: { getState: () => ({ addHandler: () => () => {} }) },
}));
vi.mock('@/lib/api', () => ({
  api: { get: vi.fn().mockResolvedValue({ messages: [] }) },
}));

import { useAssistantStore } from './assistant-store';
import { listAssistantPlans } from '@/lib/assistant-api';

function plan(id: number): AssistantPlan {
  return {
    issue_id: id,
    workspace_id: id,
    project_id: 1,
    column_id: 1,
    title: `task ${id}`,
    status: 'running',
    updated_at: id,
    parent_issue_id: 0,
  };
}

const plans = [plan(10), plan(20)];

beforeEach(() => {
  useAssistantStore.setState({
    plans: [],
    activePlanId: null,
    restoring: false,
    forceNewNext: false,
    _subs: new Map(),
  });
  vi.mocked(listAssistantPlans).mockReset().mockResolvedValue(plans);
});

describe('assistant-store refresh selection (#665)', () => {
  it('keeps a null (new-task) selection across a poll — does not jump to the first plan', async () => {
    // User clicked "new task": composing with no plan selected.
    useAssistantStore.getState().newPlan();
    expect(useAssistantStore.getState().activePlanId).toBeNull();

    await useAssistantStore.getState().refresh();

    // The 5s poll must not yank the user into the first plan mid-typing.
    expect(useAssistantStore.getState().activePlanId).toBeNull();
    expect(useAssistantStore.getState().plans).toHaveLength(2);
  });

  it('preserves an existing selection across a poll', async () => {
    useAssistantStore.setState({ plans: plans.map(toLocal), activePlanId: 20 });

    await useAssistantStore.getState().refresh();

    expect(useAssistantStore.getState().activePlanId).toBe(20);
  });

  it('falls back to the newest plan when the selected plan was deleted elsewhere', async () => {
    // Active plan 99 no longer exists in the refreshed list.
    useAssistantStore.setState({ plans: plans.map(toLocal), activePlanId: 99 });

    await useAssistantStore.getState().refresh();

    expect(useAssistantStore.getState().activePlanId).toBe(10);
  });
});

// Minimal local Plan shape matching what refresh() expects to merge against.
function toLocal(dto: AssistantPlan) {
  return {
    issueId: dto.issue_id,
    workspaceId: dto.workspace_id,
    projectId: dto.project_id,
    parentIssueId: dto.parent_issue_id,
    title: dto.title,
    status: dto.status,
    updatedAt: dto.updated_at,
    messages: [],
    steps: [],
    completed: false,
    loaded: true,
    schedules: [],
  };
}
