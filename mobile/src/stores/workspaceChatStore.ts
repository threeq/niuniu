import { create } from 'zustand';
import type { ChatAttachment } from '../api/types';

export interface ChatMessage {
  id: string;
  role: 'user' | 'assistant' | 'system';
  type: 'text' | 'tool_use' | 'tool_result' | 'thinking';
  content: string;
  timestamp: string;
  toolName?: string;
  toolInput?: string;
  toolStatus?: 'success' | 'error';
  messageId?: string;
  attachments?: ChatAttachment[];
  // Server-side agent_messages.id of the first server message that created
  // this bubble. Absent for optimistic inserts (handleSend) until the next
  // poll reconciles them. Used by loadMoreHistory's `?before=<id>` paging
  // and as a dedup key against re-fetched poll batches.
  serverId?: string;
}

interface WorkspaceChatState {
  /** Messages keyed by workspace ID */
  messages: Record<string, ChatMessage[]>;
  /** Agent status keyed by workspace ID */
  agentStatus: Record<string, string>;
  /** Typing indicator keyed by workspace ID */
  typing: Record<string, boolean>;
  /**
   * Server-side cursor: id of the newest agent_message we have already
   * ingested for each workspace. Persists across screen mounts so a
   * revisit asks `/messages?after=<lastSeenServerId>` and only fetches
   * what's genuinely new — instead of re-paying a full latest-N round-
   * trip just because the hook was re-instantiated.
   */
  lastSeenServerId: Record<string, string>;

  getMessages: (wsId: string) => ChatMessage[];
  getAgentStatus: (wsId: string) => string;
  isTyping: (wsId: string) => boolean;
  getLastSeenServerId: (wsId: string) => string | null;

  appendText: (wsId: string, text: string, serverId?: string) => void;
  addUserMessage: (wsId: string, content: string, attachments?: ChatAttachment[], serverId?: string) => void;
  addToolUse: (wsId: string, tool: { toolName: string; toolInput: string; messageId: string }, serverId?: string) => void;
  updateToolResult: (wsId: string, messageId: string, status: 'success' | 'error') => void;
  markDone: (wsId: string) => void;
  setAgentStatus: (wsId: string, status: string) => void;
  setHistoryMessages: (wsId: string, history: ChatMessage[]) => void;
  setLastSeenServerId: (wsId: string, id: string) => void;
  clearMessages: (wsId: string) => void;
}

let msgIdCounter = 0;
function nextId(): string {
  return `msg_${Date.now()}_${++msgIdCounter}`;
}

export const useWorkspaceChatStore = create<WorkspaceChatState>()((set, get) => ({
  messages: {},
  agentStatus: {},
  typing: {},
  lastSeenServerId: {},

  getMessages: (wsId) => get().messages[wsId] ?? [],

  getAgentStatus: (wsId) => get().agentStatus[wsId] ?? 'idle',

  isTyping: (wsId) => get().typing[wsId] ?? false,

  getLastSeenServerId: (wsId) => get().lastSeenServerId[wsId] ?? null,

  appendText: (wsId, text, serverId) => {
    set((state) => {
      const existing = state.messages[wsId] ?? [];
      const last = existing[existing.length - 1];
      const canAppend = last && last.role === 'assistant' && last.type === 'text';

      const msgs = canAppend
        ? [...existing.slice(0, -1), { ...last, content: last.content + text }]
        : [
            ...existing,
            {
              id: nextId(),
              role: 'assistant' as const,
              type: 'text' as const,
              content: text,
              timestamp: new Date().toISOString(),
              ...(serverId ? { serverId } : {}),
            },
          ];

      return {
        messages: { ...state.messages, [wsId]: msgs },
        typing: { ...state.typing, [wsId]: true },
      };
    });
  },

  addUserMessage: (wsId, content, attachments, serverId) => {
    set((state) => ({
      messages: {
        ...state.messages,
        [wsId]: [
          ...(state.messages[wsId] ?? []),
          {
            id: nextId(),
            role: 'user',
            type: 'text',
            content,
            timestamp: new Date().toISOString(),
            ...(attachments && attachments.length > 0 ? { attachments } : {}),
            ...(serverId ? { serverId } : {}),
          },
        ],
      },
    }));
  },

  addToolUse: (wsId, tool, serverId) => {
    set((state) => ({
      messages: {
        ...state.messages,
        [wsId]: [
          ...(state.messages[wsId] ?? []),
          {
            id: nextId(),
            role: 'assistant',
            type: 'tool_use',
            content: '',
            timestamp: new Date().toISOString(),
            toolName: tool.toolName,
            toolInput: tool.toolInput,
            messageId: tool.messageId,
            ...(serverId ? { serverId } : {}),
          },
        ],
      },
    }));
  },

  updateToolResult: (wsId, messageId, status) => {
    set((state) => ({
      messages: {
        ...state.messages,
        [wsId]: (state.messages[wsId] ?? []).map((msg) =>
          msg.messageId === messageId ? { ...msg, toolStatus: status } : msg
        ),
      },
    }));
  },

  markDone: (wsId) => {
    set((state) => ({
      typing: { ...state.typing, [wsId]: false },
      agentStatus: { ...state.agentStatus, [wsId]: 'idle' },
    }));
  },

  setAgentStatus: (wsId, status) => {
    set((state) => ({
      agentStatus: { ...state.agentStatus, [wsId]: status },
    }));
  },

  setHistoryMessages: (wsId, history) => {
    set((state) => ({
      messages: {
        ...state.messages,
        [wsId]: [...history, ...(state.messages[wsId] ?? [])],
      },
    }));
  },

  setLastSeenServerId: (wsId, id) => {
    set((state) => ({
      lastSeenServerId: { ...state.lastSeenServerId, [wsId]: id },
    }));
  },

  clearMessages: (wsId) => {
    set((state) => {
      const { [wsId]: _m, ...messages } = state.messages;
      const { [wsId]: _t, ...typing } = state.typing;
      const { [wsId]: _a, ...agentStatus } = state.agentStatus;
      const { [wsId]: _c, ...lastSeenServerId } = state.lastSeenServerId;
      return { messages, typing, agentStatus, lastSeenServerId };
    });
  },
}));
