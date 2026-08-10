import { create } from 'zustand';
import i18n from '@/i18n';
import { useAgentSSEStore } from '@/stores/agent-sse-store';
import { api } from '@/lib/api';
import {
  dispatchAssistant,
  sendAssistantMessage,
  listAssistantPlans,
  deleteAssistantPlan,
  type AssistantPlan,
  type AssistantAttachment,
  type AssistantSchedule,
} from '@/lib/assistant-api';
import type { AgentMessage, AgentMessageRecord } from '@/types/api';

export interface ChatMsg {
  key: string;
  role: 'user' | 'assistant';
  text: string;
}

export interface PlanStep {
  key: string;
  // Raw tool name; the view falls back to t('plan.step') when empty.
  label: string;
  status: 'running' | 'done';
}

// Plan is one task the assistant manages: a backing issue + its working area,
// plus the locally-tracked conversation/progress for it.
export interface Plan {
  issueId: number;
  workspaceId: number;
  projectId: number;
  // Top-level conversation this plan belongs to (0 = a top-level task). Drives
  // the rail's epic/subtask two-level grouping.
  parentIssueId: number;
  title: string;
  status: string;
  updatedAt: number;
  messages: ChatMsg[];
  steps: PlanStep[];
  completed: boolean;
  loaded: boolean; // history fetched from backend
  // Cron schedules bound to this task (drives the composer's 定时任务 indicator).
  schedules: AssistantSchedule[];
}

// Persist only the active selection; the plan list itself is the backend's
// truth (re-fetched on restore), so a reload can't desync it.
const ACTIVE_KEY = 'niuniu:assistant:activePlan';

function loadActiveId(): number | null {
  try {
    const raw = localStorage.getItem(ACTIVE_KEY);
    return raw ? Number(raw) || null : null;
  } catch {
    return null;
  }
}

function persistActiveId(id: number | null) {
  try {
    if (id) localStorage.setItem(ACTIVE_KEY, String(id));
    else localStorage.removeItem(ACTIVE_KEY);
  } catch {
    /* ignore */
  }
}

// Autohost completion sentinel: the agent emits it in its reply text when the
// goal is met. We use it to flip a plan to "completed" and strip it from what
// the user sees (it's an internal marker, not prose).
const AUTOHOST_SENTINEL = '[AUTOHOST_DONE]';

function stripSentinel(text: string): string {
  if (!text.includes(AUTOHOST_SENTINEL)) return text;
  return text.split(AUTOHOST_SENTINEL).join('').replace(/\n{3,}/g, '\n\n').trimEnd();
}

function planFromDTO(dto: AssistantPlan): Plan {
  return {
    issueId: dto.issue_id,
    workspaceId: dto.workspace_id,
    projectId: dto.project_id,
    parentIssueId: dto.parent_issue_id ?? 0,
    title: dto.title,
    status: dto.status,
    updatedAt: dto.updated_at,
    messages: [],
    steps: [],
    completed: false,
    loaded: false,
    schedules: dto.schedules ?? [],
  };
}

interface AssistantState {
  plans: Plan[];
  activePlanId: number | null; // issueId
  busy: boolean;
  error: boolean;
  restoring: boolean;
  // When true the next submit starts a fresh plan regardless of routing.
  forceNewNext: boolean;
  _idCounter: number;
  _subs: Map<number, () => void>; // workspaceId -> unsubscribe

  restore: () => Promise<void>;
  refresh: () => Promise<void>;
  submit: (text: string, files?: File[], knowledgePaths?: string[]) => Promise<boolean>;
  selectPlan: (issueId: number) => void;
  newPlan: () => void;
  deletePlan: (issueId: number) => Promise<void>;
}

// useAssistantStore drives the conversational office assistant as a multi-plan
// dispatcher: the user types, the backend routes the message to an existing or
// new plan, and each plan keeps its own conversation + live stream. State lives
// outside React so navigation/reload don't lose it (#396).
export const useAssistantStore = create<AssistantState>((set, get) => {
  // Apply one streamed event to the plan owning msg.workspaceId.
  const handleMessage = (msg: AgentMessage) => {
    const wsId = Number(msg.workspaceId);
    const mutate = (fn: (p: Plan) => Plan) =>
      set((s) => ({ plans: s.plans.map((p) => (p.workspaceId === wsId ? fn(p) : p)) }));

    switch (msg.type) {
      case 'text': {
        if (msg.role !== 'assistant' || !msg.content) return;
        const key = msg.messageId || msg.id || `a${msg.ts}`;
        mutate((p) => {
          const idx = p.messages.findIndex((m) => m.key === key);
          const raw = idx >= 0 ? p.messages[idx].text + msg.content : msg.content;
          const clean = stripSentinel(raw);
          // The sentinel arrives in the reply text (not the done/idle event), so
          // detect completion here.
          const completed = p.completed || clean !== raw;
          if (idx >= 0) {
            const messages = [...p.messages];
            messages[idx] = { ...messages[idx], text: clean };
            return { ...p, messages, completed };
          }
          return { ...p, completed, messages: [...p.messages, { key, role: 'assistant', text: clean }] };
        });
        break;
      }
      case 'tool_use': {
        mutate((p) => {
          const steps: PlanStep[] = p.steps.map((st) => ({ ...st, status: 'done' as const }));
          steps.push({ key: msg.toolUseId || `s${p.steps.length + 1}`, label: msg.toolName || '', status: 'running' });
          return { ...p, steps };
        });
        break;
      }
      case 'done':
      case 'idle': {
        const done = !!(msg.content && msg.content.includes('[AUTOHOST_DONE]'));
        mutate((p) => ({
          ...p,
          steps: p.steps.map((st) => ({ ...st, status: 'done' as const })),
          completed: p.completed || done,
        }));
        break;
      }
      default:
        break;
    }
  };

  const subscribe = (workspaceId: number) => {
    const subs = get()._subs;
    if (subs.has(workspaceId)) return;
    const unsub = useAgentSSEStore.getState().addHandler(String(workspaceId), handleMessage);
    subs.set(workspaceId, unsub);
  };

  // Lazily load a plan's history from the backend the first time it's viewed.
  const ensureLoaded = async (issueId: number) => {
    const plan = get().plans.find((p) => p.issueId === issueId);
    if (!plan || plan.loaded) return;
    try {
      const res = await api.get<{ messages: AgentMessageRecord[] }>(
        `/workspaces/${plan.workspaceId}/messages`,
      );
      const messages: ChatMsg[] = [];
      const steps: PlanStep[] = [];
      let completed = false;
      for (const r of res.messages) {
        if (r.eventType === 'text' && r.content && (r.role === 'user' || r.role === 'assistant')) {
          const key = r.messageId || r.id;
          const idx = messages.findIndex((m) => m.key === key);
          if (idx >= 0) messages[idx] = { ...messages[idx], text: messages[idx].text + r.content };
          else messages.push({ key, role: r.role, text: r.content });
        } else if (r.eventType === 'tool_use') {
          steps.push({ key: r.toolUseId || r.id, label: r.toolName || '', status: 'done' });
        }
      }
      // Strip the completion sentinel from displayed text and mark done.
      for (let i = 0; i < messages.length; i++) {
        if (messages[i].role === 'assistant' && messages[i].text.includes(AUTOHOST_SENTINEL)) {
          completed = true;
          messages[i] = { ...messages[i], text: stripSentinel(messages[i].text) };
        }
      }
      set((s) => ({
        plans: s.plans.map((p) =>
          p.issueId === issueId
            ? {
                ...p,
                // Don't clobber any live deltas that arrived while loading.
                messages: p.messages.length === 0 ? messages : p.messages,
                steps: p.steps.length === 0 ? steps : p.steps,
                completed: p.completed || completed,
                loaded: true,
              }
            : p,
        ),
      }));
    } catch {
      // Mark loaded to avoid a refetch loop; an empty conversation is acceptable.
      set((s) => ({ plans: s.plans.map((p) => (p.issueId === issueId ? { ...p, loaded: true } : p)) }));
    }
  };

  const pushUser = (issueId: number, text: string) =>
    set((s) => ({
      _idCounter: s._idCounter + 1,
      plans: s.plans.map((p) =>
        p.issueId === issueId
          ? { ...p, messages: [...p.messages, { key: `u${s._idCounter + 1}`, role: 'user', text }] }
          : p,
      ),
    }));

  return {
    plans: [],
    activePlanId: loadActiveId(),
    busy: false,
    error: false,
    restoring: false,
    forceNewNext: false,
    _idCounter: 0,
    _subs: new Map(),

    async restore() {
      if (get().plans.length > 0 || get().restoring) return;
      set({ restoring: true });
      try {
        const dtos = await listAssistantPlans();
        const plans = dtos.map(planFromDTO);
        plans.forEach((p) => subscribe(p.workspaceId));
        // Keep the persisted selection if it still exists, else newest plan.
        const persisted = get().activePlanId;
        const active = plans.some((p) => p.issueId === persisted)
          ? persisted
          : (plans[0]?.issueId ?? null);
        set({ plans, activePlanId: active });
        persistActiveId(active);
        if (active) void ensureLoaded(active);
      } catch {
        /* leave empty; the empty state is shown */
      } finally {
        set({ restoring: false });
      }
    },

    // refresh re-fetches the plan list (the backend is the source of truth) and
    // merges it into local state: titles/status/parent update in place, new
    // plans (e.g. a managed task created in another tab, or a subtask spawned by
    // the agent) appear, and plans deleted elsewhere drop out. Live conversation
    // state (messages/steps/completed/loaded) is preserved for surviving plans.
    // Polled on an interval by the page so the rail stays live without a reload.
    async refresh() {
      if (get().restoring) return;
      let dtos: AssistantPlan[];
      try {
        dtos = await listAssistantPlans();
      } catch {
        return;
      }
      set((s) => {
        const byId = new Map(s.plans.map((p) => [p.issueId, p]));
        const plans = dtos.map((dto) => {
          const existing = byId.get(dto.issue_id);
          return existing
            ? {
                ...existing,
                title: dto.title,
                status: dto.status,
                updatedAt: dto.updated_at,
                parentIssueId: dto.parent_issue_id ?? 0,
                schedules: dto.schedules ?? [],
              }
            : planFromDTO(dto);
        });
        // Keep the active selection if it still exists. A null selection is
        // intentional ("new task" composing mode via newPlan()): leave it null
        // so the 5s poll can't yank the user into the first plan mid-typing
        // (#665). Only fall back to the newest plan when a *previously-selected*
        // plan was deleted elsewhere.
        const active =
          s.activePlanId == null
            ? null
            : plans.some((p) => p.issueId === s.activePlanId)
              ? s.activePlanId
              : (plans[0]?.issueId ?? null);
        if (active !== s.activePlanId) persistActiveId(active);
        return { plans, activePlanId: active };
      });
      // Subscribe any newly-appeared workspaces; load the active plan's history.
      get().plans.forEach((p) => subscribe(p.workspaceId));
      const active = get().activePlanId;
      if (active) void ensureLoaded(active);
    },

    async submit(text, files = [], knowledgePaths = []) {
      const trimmed = text.trim();
      if ((!trimmed && files.length === 0 && knowledgePaths.length === 0) || get().busy) return false;
      set({ busy: true, error: false });
      const forceNew = get().forceNewNext;
      const activePlanId = get().activePlanId ?? undefined;
      try {
        // 1. Route to a plan (no delivery yet) so the workspace exists before
        //    we upload attachments.
        const res = await dispatchAssistant({
          description: trimmed || i18n.t('assistant:dispatch.attachmentOnly'),
          active_plan_id: forceNew ? undefined : activePlanId,
          force_new: forceNew || undefined,
        });
        const dto = res.plan;
        set((s) => {
          const exists = s.plans.some((p) => p.issueId === dto.issue_id);
          const plans = exists
            ? s.plans.map((p) =>
                p.issueId === dto.issue_id
                  ? // A new turn restarts this plan's run: clear the completed
                    // flag + last run's steps and mark it running so its status
                    // reflects the new turn live (instead of staying stuck on
                    // "已完成"). SSE/refresh then drive it to the real state.
                    {
                      ...p,
                      status: 'running',
                      title: dto.title,
                      parentIssueId: dto.parent_issue_id ?? p.parentIssueId,
                      completed: false,
                      steps: [],
                    }
                  : p,
              )
            : [planFromDTO(dto), ...s.plans];
          return { plans, activePlanId: dto.issue_id, forceNewNext: false };
        });
        persistActiveId(dto.issue_id);
        subscribe(dto.workspace_id);
        if (res.is_new) {
          set((s) => ({ plans: s.plans.map((p) => (p.issueId === dto.issue_id ? { ...p, loaded: true } : p)) }));
        } else {
          await ensureLoaded(dto.issue_id);
        }

        // 2. Upload attachments to the (now-existing) workspace.
        const uploaded: AssistantAttachment[] = [];
        for (const f of files) {
          try {
            const r = await api.uploadAttachment(String(dto.workspace_id), f);
            uploaded.push({
              path: r.path,
              name: r.name,
              type: r.mimeType?.startsWith('image/') ? 'image' : 'file',
              mimeType: r.mimeType,
              size: r.size,
            });
          } catch {
            /* skip a failed upload, keep the rest */
          }
        }

        // 3. Build the delivered content. Knowledge dirs become a read-only
        //    ingest instruction (the agent runs ingest_directory → memory RAG).
        let content = trimmed;
        if (knowledgePaths.length > 0) {
          const lines = knowledgePaths.map((p) => `- ${p}`).join('\n');
          content =
            (content ? content + '\n\n' : '') +
            `${i18n.t('assistant:dispatch.knowledgePreamble')}\n${lines}`;
        }
        if (!content) content = i18n.t('assistant:dispatch.seeAttachment');

        // 4. Optimistic user bubble: the user's text + chips for what they attached.
        const note = [...files.map((f) => `📎 ${f.name}`), ...knowledgePaths.map((p) => `📚 ${p}`)].join('  ');
        pushUser(dto.issue_id, [trimmed, note].filter(Boolean).join('\n'));

        await sendAssistantMessage(dto.workspace_id, content, uploaded);
        return true;
      } catch {
        set({ error: true });
        return false;
      } finally {
        set({ busy: false });
      }
    },

    selectPlan(issueId) {
      set({ activePlanId: issueId, forceNewNext: false });
      persistActiveId(issueId);
      void ensureLoaded(issueId);
    },

    newPlan() {
      // Deselect and arm force-new; the empty state shows until the next send.
      set({ activePlanId: null, forceNewNext: true });
      persistActiveId(null);
    },

    async deletePlan(issueId) {
      const plan = get().plans.find((p) => p.issueId === issueId);
      try {
        await deleteAssistantPlan(issueId);
      } catch {
        set({ error: true });
        return;
      }
      if (plan) {
        const subs = get()._subs;
        subs.get(plan.workspaceId)?.();
        subs.delete(plan.workspaceId);
      }
      set((s) => {
        const plans = s.plans.filter((p) => p.issueId !== issueId);
        const active =
          s.activePlanId === issueId ? (plans[0]?.issueId ?? null) : s.activePlanId;
        persistActiveId(active);
        return { plans, activePlanId: active };
      });
      const active = get().activePlanId;
      if (active) void ensureLoaded(active);
    },
  };
});
