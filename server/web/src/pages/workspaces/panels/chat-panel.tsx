import { useEffect, useRef, useMemo, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';
import {
  listPinnedMessages,
  createPinnedMessage,
  deletePinnedMessage,
} from '@/lib/pinned-messages-api';
import { scrollToChatMessage } from '@/lib/scroll-to-chat-message';
import { deriveBlockKeys } from '@/lib/chat-block-key';
import { groupTimelineEvents } from '@/lib/group-timeline-events';
import { useAgentSSEStore } from '@/stores/agent-sse-store';
import type { AgentMessage, AgentMessageRecord, Workspace, ChatAttachment } from '@/types/api';
import { ChatMessage, type TimelineEvent } from './chat-message';
import { stripAttachmentPrefix } from '@/lib/strip-attachment-prefix';
import { ChatStickyUserHeader } from './chat-sticky-user-header';
import { ToolGroup } from './tool-group';
import { ChatInput, type ChatInputHandle } from './chat-input';
import { useWorkspaceDiff } from '@/lib/hooks/use-workspace-diff';
import { useWorkspacePanelStore } from '@/stores/workspace-panel-store';
import { useWorkspaceTaskStore } from '@/stores/workspace-task-store';
import { QueueList } from './queue-list';
import { toast } from 'sonner';
import { useAttachmentStore } from '@/stores/attachment-store';
import { usePermissionStore } from '@/stores/permission-store';
import { useAskUserStore } from '@/stores/ask-user-store';
import { useChatInputBridge } from '@/stores/chat-input-bridge-store';
import { PermissionPromptCard } from '@/components/agent/PermissionPromptCard';
import { AskUserQuestionCard } from '@/components/agent/AskUserQuestionCard';

interface ChatPanelProps {
  workspace: Workspace;
  readOnly?: boolean;
}

function recordToEvent(r: AgentMessageRecord): TimelineEvent {
  let attachments: ChatAttachment[] | undefined;
  if (r.attachments) {
    try { attachments = JSON.parse(r.attachments); } catch { /* ignore */ }
  }
  return {
    id: r.id,
    messageId: r.messageId,
    type: r.eventType,
    role: r.role,
    content: r.content,
    toolName: r.toolName,
    toolInput: r.toolInput,
    toolUseId: r.toolUseId,
    isError: r.isError,
    costUsd: r.costUsd,
    numTurns: r.numTurns,
    durationMs: r.durationMs,
    inputTokens: r.inputTokens,
    outputTokens: r.outputTokens,
    createdAt: r.createdAt,
    attachments,
  };
}

let textBlockCounter = 0;

export function ChatPanel({ workspace, readOnly }: ChatPanelProps) {
  const { t } = useTranslation('workspaces');
  const workspaceId = workspace.id;
  const queryClient = useQueryClient();
  const [events, setEvents] = useState<TimelineEvent[]>([]);
  const [sessionState, setSessionState] = useState<'idle' | 'running' | 'waiting_confirm'>('idle');
  // True while the send POST is in flight (optimistic bubble shown, agent not
  // yet running). Drives the send button's in-progress look so a slow network
  // doesn't feel like a dead click.
  const [sending, setSending] = useState(false);
  const [cliType, setCliType] = useState(workspace.cli_type ?? 'claude');
  const [contextPercent, setContextPercent] = useState<number | undefined>(undefined);
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const loadingMoreRef = useRef(false);
  const oldestDbIdRef = useRef<string | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const chatInputRef = useRef<ChatInputHandle>(null);
  const addHandler = useAgentSSEStore.getState().addHandler;
  // Source the chat-input changes badge from the SAME "vs baseline" diff the
  // changes panel uses for its total title, so the two stay in sync (the old
  // git-status-only hook counted uncommitted files only and reported +0/-0).
  const { totalFiles, totalAdditions, totalDeletions } = useWorkspaceDiff(workspaceId);
  const changesSummary = { files: totalFiles, additions: totalAdditions, deletions: totalDeletions };
  const togglePanel = useWorkspacePanelStore((s) => s.togglePanel);
  const attachmentStore = useAttachmentStore();

  // Permission prompts — pending requests rendered inline in the chat stream.
  const wsIdNum = Number(workspaceId);
  // Select the raw entry (stable ref between store updates); derive the filtered
  // list outside the selector. Returning `.filter()` from the selector would
  // produce a fresh array each render and trip zustand's Object.is snapshot
  // check → infinite re-render loop (React #185 in prod).
  const wsRequests = usePermissionStore((s) => s.byWorkspace.get(wsIdNum));
  const pendingPermissions = useMemo(
    () => (wsRequests ?? []).filter((r) => r.status === 'pending'),
    [wsRequests],
  );
  const decidePermission = usePermissionStore((s) => s.decide);

  // Ask-user-question prompts — same pattern as permission prompts.
  const wsAskRequests = useAskUserStore((s) => s.byWorkspace.get(wsIdNum));
  const pendingAskUser = useMemo(
    () => (wsAskRequests ?? []).filter((r) => r.status === 'pending'),
    [wsAskRequests],
  );
  const decideAskUser = useAskUserStore((s) => s.decide);

  // Working directory is the workspace root (contains .worktrees/ for all repos)
  const workDir = workspace.path;

  // Warm the echarts chunk during idle once the chat panel mounts, so the first
  // niuniu-data chart in the stream renders without a fetch stall. The renderer
  // is the only echarts importer (separate chunk); import() here primes the
  // module cache. requestIdleCallback with a setTimeout fallback; cleanup
  // cancels a still-pending warm-up. (plan task D4)
  useEffect(() => {
    const ric =
      window.requestIdleCallback ??
      ((cb: () => void) => window.setTimeout(cb, 200) as unknown as number);
    const cancel = window.cancelIdleCallback ?? window.clearTimeout;
    const handle = ric(() => {
      void import('@/components/data/echarts-renderer');
    });
    return () => cancel(handle as number);
  }, []);

  // Backfill any pending permission requests left from a server restart so
  // the inline cards appear immediately when this workspace mounts.
  useEffect(() => {
    usePermissionStore
      .getState()
      .load(wsIdNum)
      .catch((e) => console.error('permission load failed', e));
    useAskUserStore
      .getState()
      .load(wsIdNum)
      .catch((e) => console.error('ask-user load failed', e));
  }, [wsIdNum]);

  // Load history and restore session state on mount
  useEffect(() => {
    if (readOnly) {
      // Only load history messages
      api
        .get<{ messages: AgentMessageRecord[]; hasMore: boolean }>(`/workspaces/${workspaceId}/messages`)
        .then((res) => {
          const mapped = res.messages.map(recordToEvent);
          setEvents(mapped);
          setHasMore(res.hasMore);
          if (res.messages.length > 0) {
            oldestDbIdRef.current = res.messages[0].id;
          }
        })
        .catch(() => {});
      return;
    }

    const loadHistory = api
      .get<{ messages: AgentMessageRecord[]; hasMore: boolean }>(`/workspaces/${workspaceId}/messages`)
      .then((res) => {
        const mapped = res.messages.map(recordToEvent);
        setEvents(mapped);
        setHasMore(res.hasMore);
        if (res.messages.length > 0) {
          oldestDbIdRef.current = res.messages[0].id;
        }
        return res.messages;
      })
      .catch(() => [] as AgentMessageRecord[]);

    const loadSession = api
      .get<{ sessionId: string | null; status: string }>(`/workspaces/${workspaceId}/session`)
      .catch(() => ({ sessionId: null, status: 'idle' }));

    // Wait for both to resolve so streaming indicator can be applied to loaded messages
    Promise.all([loadHistory, loadSession]).then(([, session]) => {
      // GetSession returns the backend's loop-scoped Status() — the SAME signal
      // the queue gate uses. Mirror it in BOTH directions so a missed loop-end
      // 'idle' SSE (e.g. disconnect at the exact end) self-heals on next load.
      if (session.status === 'running') {
        setSessionState('running');
        // Restore streaming indicator on the last assistant message
        setEvents((prev) => {
          const lastAssistantIdx = [...prev].reverse().findIndex(e => e.role === 'assistant' && e.type === 'text');
          if (lastAssistantIdx === -1) return prev;
          const actualIdx = prev.length - 1 - lastAssistantIdx;
          const updated = [...prev];
          updated[actualIdx] = { ...updated[actualIdx], streaming: true };
          return updated;
        });
      } else {
        setSessionState('idle');
      }
    });

    // Load workspace tasks on mount
    useWorkspaceTaskStore.getState().loadTasks(Number(workspaceId));
  }, [workspaceId, readOnly]);

  // Recover state on SSE reconnect
  useEffect(() => {
    if (readOnly) return;
    const unsub = useAgentSSEStore.getState().addOnReconnect(() => {
      // Reload tasks on SSE reconnect
      useWorkspaceTaskStore.getState().loadTasks(Number(workspaceId));
      // Re-check session status on reconnect
      api.get<{ sessionId: string | null; status: string }>(`/workspaces/${workspaceId}/session`)
        .then((res) => {
          // Mirror loop-scoped Status() in both directions (see mount handler):
          // resyncs the input gate to the backend's authoritative running state.
          setSessionState(res.status === 'running' ? 'running' : 'idle');
        })
        .catch(() => {});
    });
    return unsub;
  }, [workspaceId, readOnly]);

  // SSE handler
  useEffect(() => {
    if (readOnly) return;
    const unsubscribe = addHandler(workspaceId, (msg: AgentMessage) => {
      switch (msg.type) {
        case 'text': {
          setSessionState('running');
          setEvents((prev) => {
            // Find the last text event with the same messageId
            const idx = prev.findLastIndex(
              (e) => e.messageId === msg.messageId && e.type === 'text' && e.role === msg.role,
            );
            if (idx !== -1) {
              // Check if a tool_use or tool_result appeared after this text event.
              // If so, this is a new text block — create a separate event instead of appending.
              const hasToolAfter = prev.slice(idx + 1).some(
                (e) => e.type === 'tool_use' || e.type === 'tool_result',
              );
              if (!hasToolAfter) {
                const updated = [...prev];
                updated[idx] = {
                  ...updated[idx],
                  content: updated[idx].content + msg.content,
                  streaming: true,
                };
                return updated;
              }
            }
            // For user echoes, adopt the server messageId on the matching
            // optimistic bubble (placeholder id starts with 'user-') instead
            // of appending a duplicate.
            if (msg.role === 'user') {
              const optIdx = prev.findLastIndex(
                (e) =>
                  e.type === 'text' &&
                  e.role === 'user' &&
                  e.messageId.startsWith('user-') &&
                  e.content === msg.content,
              );
              if (optIdx !== -1) {
                const updated = [...prev];
                updated[optIdx] = {
                  ...updated[optIdx],
                  id: msg.messageId,
                  messageId: msg.messageId,
                };
                return updated;
              }
            }
            // Queue-dequeued / scheduler / harness user echoes have no
            // optimistic bubble to reconcile against. Parse attachments from
            // the SSE event so the preview renders alongside the message.
            let echoAttachments: ChatAttachment[] | undefined;
            if (msg.role === 'user' && msg.attachments) {
              try { echoAttachments = JSON.parse(msg.attachments); } catch { /* ignore */ }
            }
            return [
              ...prev,
              {
                id: msg.messageId + '-text-' + (++textBlockCounter),
                messageId: msg.messageId,
                type: 'text',
                role: msg.role,
                content: msg.content,
                streaming: true,
                createdAt: Date.now(),
                attachments: echoAttachments,
              },
            ];
          });
          break;
        }

        case 'tool_use': {
          setEvents((prev) => {
            if (msg.toolInput) {
              // Update existing entry by toolUseId
              const idx = prev.findIndex(
                (e) => e.type === 'tool_use' && e.toolUseId === msg.toolUseId,
              );
              if (idx !== -1) {
                const updated = [...prev];
                updated[idx] = { ...updated[idx], toolInput: msg.toolInput };
                return updated;
              }
            }
            return [
              ...prev,
              {
                id: msg.toolUseId ?? msg.messageId,
                messageId: msg.messageId,
                type: 'tool_use',
                role: msg.role,
                content: msg.content,
                toolName: msg.toolName,
                toolInput: msg.toolInput,
                toolUseId: msg.toolUseId,
              },
            ];
          });
          break;
        }

        case 'tool_result': {
          setEvents((prev) => [
            ...prev,
            {
              id: (msg.toolUseId ?? msg.messageId) + '-result',
              messageId: msg.messageId,
              type: 'tool_result',
              role: msg.role,
              content: msg.content,
              toolUseId: msg.toolUseId,
              isError: msg.isError,
            },
          ]);
          break;
        }

        case 'thinking': {
          setEvents((prev) => {
            const idx = prev.findIndex(
              (e) => e.messageId === msg.messageId && e.type === 'thinking',
            );
            if (idx !== -1) {
              const updated = [...prev];
              updated[idx] = {
                ...updated[idx],
                content: updated[idx].content + msg.content,
                streaming: true,
              };
              return updated;
            }
            return [
              ...prev,
              {
                id: msg.messageId + '-thinking',
                messageId: msg.messageId,
                type: 'thinking',
                role: msg.role,
                content: msg.content,
                streaming: true,
              },
            ];
          });
          break;
        }

        case 'system_info': {
          if (msg.cliType === 'claude' || msg.cliType === 'codex') setCliType(msg.cliType);
          // Update context window usage from token data
          if (msg.inputTokens && msg.inputTokens > 0) {
            // Fallback context window size — real percentage comes from /claude-status API.
            const MAX_CONTEXT_TOKENS = 1_000_000;
            setContextPercent(Math.min(100, (msg.inputTokens / MAX_CONTEXT_TOKENS) * 100));
            break;
          }
          if (msg.outputTokens && msg.outputTokens > 0) {
            // output token updates — skip rendering as system message
            break;
          }
          if (msg.content === 'session started') {
            useWorkspaceTaskStore.getState().clearTasks();
            break;
          }
          setEvents((prev) => [
            ...prev,
            {
              id: msg.messageId + '-sys-' + Date.now(),
              messageId: msg.messageId,
              type: 'system_info',
              role: msg.role,
              content: msg.content,
            },
          ]);
          break;
        }

        case 'task_update': {
          if (msg.taskData) {
            useWorkspaceTaskStore.getState().handleTaskEvent(msg.taskData);
          }
          break;
        }

        case 'done': {
          // NOTE: do NOT set sessionState='idle' here. 'done' is a PER-TURN result
          // event — it fires after every turn, including between autohost-continue
          // turns where the loop (and the backend's loop-scoped `running`) is still
          // alive. Inferring idle from it unlocks the input mid-chain while the
          // backend still queues, so the input gate and the queue gate disagree.
          // The session gate flips to idle only on the loop-end 'idle' (EventIdle)
          // event, which the backend now guarantees on every SendLoop exit.
          // Update context percent from result token data if available; otherwise keep last known value
          if (msg.inputTokens && msg.inputTokens > 0) {
            const MAX_CONTEXT_TOKENS = 200_000;
            setContextPercent(Math.min(100, (msg.inputTokens / MAX_CONTEXT_TOKENS) * 100));
          }
          queryClient.invalidateQueries({ queryKey: ['workspace', String(workspaceId), 'queue'] });
          // Finalize all streaming events
          setEvents((prev) =>
            prev.map((e) =>
              e.streaming ? { ...e, streaming: false } : e,
            ),
          );
          // Append done event with token + timing info (completion time = now)
          setEvents((prev) => [
            ...prev,
            {
              id: msg.messageId + '-done',
              messageId: msg.messageId,
              type: 'done',
              role: msg.role,
              content: '',
              costUsd: msg.costUsd,
              numTurns: msg.numTurns,
              durationMs: msg.durationMs,
              inputTokens: msg.inputTokens,
              outputTokens: msg.outputTokens,
              createdAt: Date.now(),
            },
          ]);
          break;
        }

        case 'error': {
          // Per-turn error (like 'done'): in autohost mode the loop may auto-recover
          // and keep running, so don't flip the loop-scoped gate here. The loop-end
          // 'idle' (EventIdle) event is the single authority that unlocks the input.
          // Finalize all streaming events
          setEvents((prev) =>
            prev.map((e) => (e.streaming ? { ...e, streaming: false } : e)),
          );
          setEvents((prev) => [
            ...prev,
            {
              id: msg.messageId + '-error',
              messageId: msg.messageId,
              type: 'error',
              role: msg.role,
              content: msg.content,
            },
          ]);
          break;
        }

        case 'idle': {
          setSessionState('idle');
          break;
        }
        case 'schedule_trigger': {
          toast.info(t('panels.chat.scheduleToast'), { description: msg.content || t('panels.chat.scheduleDesc') });
          break;
        }

      }
    });

    return unsubscribe;
  }, [workspaceId, addHandler, queryClient, readOnly, t]);

  // Auto-scroll on new events (only if user is near the bottom)
  const isNearBottomRef = useRef(true);
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    if (isNearBottomRef.current) {
      el.scrollTop = el.scrollHeight;
    }
  }, [events]);

  // Load older messages when scrolling to top
  const hasMoreRef = useRef(hasMore);
  hasMoreRef.current = hasMore;

  const loadOlderMessages = useCallback(async () => {
    if (loadingMoreRef.current || !hasMoreRef.current) return;
    const cursorId = oldestDbIdRef.current;
    if (!cursorId) return;
    loadingMoreRef.current = true;
    setLoadingMore(true);
    try {
      const res = await api.get<{ messages: AgentMessageRecord[]; hasMore: boolean }>(
        `/workspaces/${workspaceId}/messages?before=${encodeURIComponent(cursorId)}`
      );
      setHasMore(res.hasMore);
      if (res.messages.length > 0) {
        oldestDbIdRef.current = res.messages[0].id;
        const olderEvents = res.messages.map(recordToEvent);
        setEvents((prev) => [...olderEvents, ...prev]);
      }
    } catch {
      // ignore
    } finally {
      loadingMoreRef.current = false;
      setLoadingMore(false);
    }
  }, [workspaceId]);

  // Track scroll position for auto-scroll and load-more
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const handleScroll = () => {
      // Near bottom check (within 100px)
      isNearBottomRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 100;
      // Near top check — load older messages
      if (el.scrollTop < 50 && !loadingMoreRef.current && hasMoreRef.current) {
        const prevScrollHeight = el.scrollHeight;
        loadOlderMessages().then(() => {
          // Preserve scroll position after prepending
          requestAnimationFrame(() => {
            el.scrollTop = el.scrollHeight - prevScrollHeight;
          });
        });
      }
    };
    el.addEventListener('scroll', handleScroll);
    return () => el.removeEventListener('scroll', handleScroll);
  }, [loadOlderMessages]);

  // Build toolResults map from tool_result events
  const toolResults = useMemo(() => {
    const map = new Map<string, { content: string; isError?: boolean }>();
    for (const e of events) {
      if (e.type === 'tool_result' && e.toolUseId) {
        map.set(e.toolUseId, { content: e.content, isError: e.isError });
      }
    }
    return map;
  }, [events]);

  // Helper: add a local system message to chat
  const addLocalMessage = useCallback((text: string) => {
    const id = 'local-' + Date.now();
    setEvents((prev) => [
      ...prev,
      { id, messageId: id, type: 'system_info', role: 'system', content: text },
    ]);
  }, []);

  // Handle builtin slash commands locally (they don't work in -p mode)
  const handleBuiltinCommand = useCallback(
    async (cmd: string): Promise<boolean> => {
      const trimmed = cmd.trim();
      const parts = trimmed.split(/\s+/);
      const command = parts[0].toLowerCase();

      // Commands that are unsupported in -p mode (interactive-only)
      const unsupportedCommands = [
        '/doctor', '/bug', '/vim', '/history', '/login', '/logout',
        '/config', '/permissions', '/mcp',
      ];
      if (unsupportedCommands.includes(command)) {
        addLocalMessage(t('panels.chat.builtin.unsupported', { command }));
        return true;
      }

      switch (command) {
        case '/clear': {
          await api.post(`/workspaces/${workspaceId}/session/clear`).catch(() => {});
          setEvents([]);
          setHasMore(false);
          oldestDbIdRef.current = null;
          setContextPercent(undefined);
          addLocalMessage(t('panels.chat.builtin.cleared'));
          return true;
        }
        case '/cost': {
          try {
            const res = await api.get<{
              totalCostUsd: number;
              totalTurns: number;
              totalDurationMs: number;
              totalInteractions: number;
            }>(`/workspaces/${workspaceId}/costs`);
            if (res.totalInteractions === 0) {
              addLocalMessage(t('panels.chat.builtin.noCost'));
            } else {
              const mins = Math.floor(res.totalDurationMs / 60000);
              const secs = Math.floor((res.totalDurationMs % 60000) / 1000);
              addLocalMessage(
                t('panels.chat.builtin.costSummary', {
                  cost: res.totalCostUsd.toFixed(4),
                  turns: res.totalTurns,
                  interactions: res.totalInteractions,
                  minutes: mins,
                  seconds: secs,
                }),
              );
            }
          } catch {
            addLocalMessage(t('panels.chat.builtin.costFailed'));
          }
          return true;
        }
        case '/status': {
          try {
            const res = await api.get<{ sessionId: string | null; status: string }>(
              `/workspaces/${workspaceId}/session`,
            );
            addLocalMessage(
              t('panels.chat.builtin.statusLine', {
                status: res.status,
                session: res.sessionId ?? t('panels.chat.builtin.statusNone'),
              }),
            );
          } catch {
            addLocalMessage(t('panels.chat.builtin.statusFailed'));
          }
          return true;
        }
        case '/model': {
          try {
            const res = await api.get<{ env: Record<string, string> }>(
              `/workspaces/${workspaceId}/env`,
            );
            const model = res.env['NIUNIU_MODEL'] || t('panels.chat.builtin.modelDefault');
            addLocalMessage(t('panels.chat.builtin.currentModel', { model }));
          } catch {
            addLocalMessage(t('panels.chat.builtin.modelFailed'));
          }
          return true;
        }
        case '/help': {
          addLocalMessage(t('panels.chat.builtin.helpText'));
          return true;
        }
        case '/compact':
        case '/spec':
        case '/superpowers:spec':
        case '/plan':
        case '/superpowers:plan':
        case '/coding':
        case '/superpowers:coding':
        case '/review':
        case '/superpowers:review':
        case '/memory':
        case '/init': {
          // Translated to prompts — fall through to translateBuiltinPrompt
          return false;
        }
        default:
          return false;
      }
    },
    [workspaceId, addLocalMessage, t],
  );

  // Translate builtin commands to natural prompts for -p mode
  const translateBuiltinPrompt = (content: string): string => {
    const parts = content.trim().split(/\s+/);
    const cmd = parts[0].toLowerCase();
    const rest = parts.slice(1).join(' ');
    const isCodexWorkspace = cliType === 'codex';
    const memoryFile = isCodexWorkspace ? 'AGENTS.md' : 'CLAUDE.md';
    switch (cmd) {
      case '/compact':
        // Pass the REAL CLI /compact command through unchanged so the
        // conversation history is actually compacted: Claude rewrites its
        // --resume history into a summary, shrinking the next turn's prompt. A
        // natural-language "please summarize" prompt would only ADD tokens and
        // never reduce the context window — that's the bug we're fixing. This
        // matches the automatic path (server/internal/agentproxy/auto_compact.go),
        // and preserves any user-supplied focus instruction ("/compact keep X").
        // Codex has no /compact command, so fall back to the summarize prompt.
        return isCodexWorkspace
          ? 'Please compact and summarize our conversation so far to free up context space. Keep key decisions, file paths, and current task state.'
          : content.trim();
      case '/spec':
      case '/superpowers:spec':
        return [
          'Use the superpowers spec workflow for this Codex workspace.',
          rest ? `User request: ${rest}` : 'Use the current user request and workspace context.',
          'Compare against existing design docs under docs/superpowers/specs/ when relevant.',
          'Clarify requirements, non-goals, risks, and acceptance criteria.',
          'Create or update the appropriate spec document under docs/superpowers/specs/.',
          'Do not implement code unless the user explicitly asks you to continue.',
        ].join('\n');
      case '/plan':
      case '/superpowers:plan':
        return [
          'Use the superpowers plan workflow for this Codex workspace.',
          rest ? `Planning target: ${rest}` : 'Plan from the confirmed spec and current workspace state.',
          'Read the relevant spec under docs/superpowers/specs/ and inspect the existing code structure.',
          'Create or update an execution plan under docs/superpowers/plans/ with small verifiable tasks.',
          'Include touched files, validation commands, risks, and rollback notes.',
        ].join('\n');
      case '/coding':
      case '/superpowers:coding':
        return [
          'Use the superpowers coding workflow for this Codex workspace.',
          rest ? `Implementation target: ${rest}` : 'Implement the next approved plan step.',
          'Follow the relevant docs/superpowers plan and keep changes tightly scoped.',
          'Keep adapter code as CLI driver only; shared business runtime, session, store, permissions, and event flow must remain common.',
          'Run the focused validation commands for the changed area and report any failures.',
        ].join('\n');
      case '/review':
      case '/superpowers:review':
        return 'Review all current code changes in this workspace. Check for bugs, style issues, and potential improvements.';
      case '/memory':
        return rest
          ? `Check the ${memoryFile} memory file and ${rest}`
          : `Show the contents of the ${memoryFile} memory file in the current workspace.`;
      case '/init':
        return `Initialize a ${memoryFile} file in the current workspace with project context and conventions.`;
      default:
        return content;
    }
  };

  const handleSend = useCallback(
    async (content: string, attachments: ChatAttachment[] = []) => {
      // Check if it's a builtin command handled locally
      if (content.trim().startsWith('/')) {
        const handled = await handleBuiltinCommand(content);
        if (handled) return;
        // Not fully handled locally — translate and send as prompt
        content = translateBuiltinPrompt(content);
      }

      // Optimistically add user message. Match server agentproxy.go
      // SendMessage finalContent format exactly (paths joined, not names) so
      // the SSE echo's content-based reconcile adopts this bubble instead of
      // appending a duplicate.
      const now = Date.now();
      const attachLabel = String.fromCharCode(0x9644, 0x4ef6); // attachment marker
      const localContent =
        attachments.length > 0
          ? `${content}\n\n[${attachLabel}: ${attachments.map((a) => a.path).join(', ')}]`
          : content;
      const userEvent: TimelineEvent = {
        id: 'user-' + now,
        messageId: 'user-' + now,
        type: 'text',
        role: 'user',
        content: localContent,
        createdAt: now,
        attachments: attachments.length > 0 ? attachments : undefined,
      };
      setEvents((prev) => [...prev, userEvent]);

      setSending(true);
      try {
        const body: { content: string; attachments?: ChatAttachment[] } = { content };
        if (attachments.length > 0) body.attachments = attachments;
        const result = await api.post<{ status?: string }>(`/workspaces/${workspaceId}/messages`, body);
        if (result?.status === 'queued') {
          // Message queued — remove optimistic user message (it's in the queue, not chat)
          setEvents((prev) => prev.filter((e) => e.id !== userEvent.id));
          queryClient.invalidateQueries({ queryKey: ['workspace', String(workspaceId), 'queue'] });
        } else {
          setSessionState('running');
        }
        attachmentStore.clearAttachments();
      } catch {
        // API error — session stays idle, no state change needed
      } finally {
        setSending(false);
      }
    },
    [workspaceId, handleBuiltinCommand, attachmentStore, queryClient, cliType],
  );

  // Cross-panel prompt injection (e.g. "sync from branch" in the changes panel
  // dispatches a pre-composed prompt here). Auto-send routes through handleSend
  // so the message takes the same builtin-command + queue path as a normal send.
  const bridgePending = useChatInputBridge((s) => s.pending);
  const consumeBridge = useChatInputBridge((s) => s.consume);
  useEffect(() => {
    if (!bridgePending) return;
    if (bridgePending.workspaceId !== String(workspaceId)) return;
    const { content, autoSend, attachments, nonce } = bridgePending;
    if (autoSend) {
      handleSend(content, attachments ?? []);
    } else {
      chatInputRef.current?.setValue(content);
      chatInputRef.current?.focus();
    }
    consumeBridge(nonce);
  }, [bridgePending, workspaceId, handleSend, consumeBridge]);

  // Handle stop agent
  const handleStopAgent = useCallback(() => {
    api.stopAgent(workspaceId).catch(() => {});
  }, [workspaceId]);

  // The last user message in the stream — i.e. the message currently being
  // executed. Used by fusion-send to prepend to a queued item.
  const lastUserMessage = useMemo(() => {
    for (let i = events.length - 1; i >= 0; i--) {
      const e = events[i];
      if (e.role === 'user' && e.type === 'text') {
        return stripAttachmentPrefix(e.content ?? '', e.attachments).trim();
      }
    }
    return '';
  }, [events]);

  // Fusion send: interrupt the running message, concatenate the last sent
  // message with this queued item, and refill chat-input for the user to
  // review and resend.
  const handleFuseToInput = useCallback(
    (content: string) => {
      handleStopAgent();
      const combined = [lastUserMessage, content].filter(Boolean).join('\n\n');
      chatInputRef.current?.setValue(combined);
      chatInputRef.current?.focus();
    },
    [handleStopAgent, lastUserMessage],
  );

  const scrollToMessage = useCallback((messageId: string) => {
    scrollToChatMessage(messageId);
  }, []);

  // Pinned messages — bookmarks shown in the pin-message panel. Backed by
  // /api/workspaces/:id/pinned-messages (persisted), so they survive reloads.
  const { data: pins } = useQuery({
    queryKey: ['pinned-messages', String(workspaceId)],
    queryFn: () => listPinnedMessages(workspaceId),
  });
  const pinnedKeys = useMemo(() => new Set((pins ?? []).map((p) => p.message_id)), [pins]);
  const pinIdByKey = useMemo(() => {
    const m = new Map<string, number>();
    for (const p of pins ?? []) m.set(p.message_id, p.id);
    return m;
  }, [pins]);

  // Per-block pin key. The DB `message_id` groups every block of one assistant
  // turn (text -> tool -> more text all share it), so keying a pin off the raw
  // messageId would pin/highlight every sibling block. Instead derive a stable
  // per-block key: the first block of a turn keeps the bare messageId (so
  // task-locate and pre-existing pins still resolve), later blocks get
  // `messageId#N`. Order-derived, so it stays stable across reloads.
  const blockKeyByEventId = useMemo(() => deriveBlockKeys(events), [events]);
  const blockKeyOf = useCallback(
    (ev: TimelineEvent) => blockKeyByEventId.get(ev.id) ?? ev.messageId,
    [blockKeyByEventId],
  );

  const invalidatePins = useCallback(() => {
    queryClient.invalidateQueries({ queryKey: ['pinned-messages', String(workspaceId)] });
  }, [queryClient, workspaceId]);

  // Build a short plain-text preview captured at pin time so the panel can
  // render even when the message is later scrolled out of the loaded window.
  const previewOf = useCallback((ev: TimelineEvent): string => {
    const raw = stripAttachmentPrefix(ev.content ?? '', ev.attachments);
    return raw.replace(/\s+/g, ' ').trim().slice(0, 200);
  }, []);

  const handleTogglePin = useCallback(
    (ev: TimelineEvent) => {
      const key = blockKeyOf(ev);
      const existingPinId = pinIdByKey.get(key);
      if (existingPinId != null) {
        deletePinnedMessage(workspaceId, existingPinId)
          .then(invalidatePins)
          .catch(() => toast.error(t('chatMessage.pinFailed')));
        return;
      }
      createPinnedMessage(workspaceId, {
        message_id: key,
        role: ev.role,
        preview: previewOf(ev),
      })
        .then(invalidatePins)
        .catch(() => toast.error(t('chatMessage.pinFailed')));
    },
    [workspaceId, pinIdByKey, blockKeyOf, invalidatePins, previewOf, t],
  );

  // Determine whether to show agent label: show before first assistant text after user msg
  const showAgentLabelSet = new Set<string>();
  let lastWasUser = false;
  for (const e of events) {
    if (e.role === 'user') {
      lastWasUser = true;
    } else if (e.role === 'assistant' && e.type === 'text' && lastWasUser) {
      showAgentLabelSet.add(e.id);
      lastWasUser = false;
    }
  }

  const grouped = useMemo(() => groupTimelineEvents(events), [events]);

  return (
    <div className="flex flex-col h-full bg-warm-surface">
      {/* Scrollable message area */}
      <div ref={scrollRef} className="flex-1 overflow-y-auto overflow-x-hidden px-4 pb-4">
        {/* Sticky header sits OUTSIDE the padded list and pins flush at the
            scroll-area top. The list's pt-4 gives the first message its top
            breathing — keeping that breathing as scroll-container padding-top
            would instead leave a 16px see-through strip above the pinned
            header that scrolling content peeks through. */}
        <ChatStickyUserHeader scrollRef={scrollRef} events={events} />
        <div className="max-w-3xl mx-auto space-y-2 min-w-0 pt-4">
          {loadingMore && (
            <div className="flex justify-center py-2">
              <span className="text-xs text-warm-text-muted">{t('panels.chat.loadMore')}</span>
            </div>
          )}
          {grouped.map((item, idx) => {
            if (Array.isArray(item)) {
              // Tool group (multiple consecutive tool_use events). Use the
              // per-block key so it never collides with a sibling text block's
              // `msg-<messageId>` id.
              return (
                <div key={`tg-${idx}`} id={`msg-${blockKeyOf(item[0])}`}>
                  <ToolGroup events={item} toolResults={toolResults} />
                </div>
              );
            }
            const event = item;
            const blockKey = blockKeyOf(event);
            return (
              <ChatMessage
                key={event.id}
                event={event}
                cliType={cliType}
                showAgentLabel={showAgentLabelSet.has(event.id)}
                toolResults={toolResults}
                workspaceId={String(workspaceId)}
                blockKey={blockKey}
                isPinned={pinnedKeys.has(blockKey)}
                onTogglePin={readOnly ? undefined : handleTogglePin}
              />
            );
          })}
          {sessionState === 'running' && (
            <div className="flex items-center gap-2 text-sm text-warm-text-muted py-2">
              <div className="flex gap-0.5" aria-hidden="true">
                <span className="w-1.5 h-1.5 bg-brand rounded-full animate-bounce [animation-delay:0ms]" />
                <span className="w-1.5 h-1.5 bg-brand rounded-full animate-bounce [animation-delay:150ms]" />
                <span className="w-1.5 h-1.5 bg-brand rounded-full animate-bounce [animation-delay:300ms]" />
              </div>
              <span aria-live="polite">{t('panels.chat.agentWorking')}</span>
            </div>
          )}
          {pendingPermissions.map((req) => (
            <div key={`perm-${req.id}`}>
              <PermissionPromptCard
                request={req}
                onDecide={(body) => {
                  decidePermission(req.id, body).catch((e) =>
                    console.error('decide failed', e),
                  );
                }}
              />
            </div>
          ))}
          {pendingAskUser.map((req) => (
            <div key={`ask-${req.id}`}>
              <AskUserQuestionCard
                request={req}
                onDecide={(body) => {
                  decideAskUser(req.id, body).catch((e) =>
                    console.error('ask-user decide failed', e),
                  );
                }}
              />
            </div>
          ))}
        </div>
      </div>

      {/* Queue list */}
      {!readOnly && <QueueList
        workspaceId={String(workspaceId)}
        sessionState={sessionState}
        onSendToInput={(content) => {
          chatInputRef.current?.setValue(content);
          chatInputRef.current?.focus();
        }}
        onFuseToInput={handleFuseToInput}
      />}

      {/* Input */}
      {!readOnly && (
        <ChatInput
          ref={chatInputRef}
          workspace={workspace}
          workDir={workDir}
          onSend={handleSend}
          sessionState={sessionState}
          sending={sending}
          contextPercent={contextPercent}
          totalChanges={changesSummary}
          onToggleChanges={() => togglePanel('changes')}
          onStopAgent={handleStopAgent}
          onClickTask={scrollToMessage}
          messagesScrollRef={scrollRef}
        />
      )}
    </div>
  );
}
