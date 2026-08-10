import { useEffect, useRef, useCallback, useState } from 'react';
import { AppState } from 'react-native';
import { api } from '../api/client';
import { useWorkspaceChatStore } from '../stores/workspaceChatStore';
import type { AgentMessagesResponse, ChatAttachment } from '../api/types';

// Server stores attachments as a JSON string on agent_messages.attachments;
// parse defensively because it's user-influenced content stored verbatim.
function parseAttachments(raw?: string): ChatAttachment[] | undefined {
  if (!raw) return undefined;
  try {
    const arr = JSON.parse(raw);
    if (Array.isArray(arr) && arr.length > 0) return arr as ChatAttachment[];
  } catch {
    /* ignore */
  }
  return undefined;
}

const POLL_INTERVAL_MS = 5000;
/**
 * First-poll size cap. Most users only see the bottom 10–20 messages on
 * open; trim the cold-start payload accordingly. Older history pages in
 * via `?before=<id>` from the inverted FlatList's onEndReached.
 */
const INITIAL_FETCH_LIMIT = 20;

export interface UseWorkspaceSSEResult {
  /**
   * True after the first poll for the current workspaceId has settled
   * (success or error). Used by the screen to render an in-area "loading
   * messages" indicator instead of a full-screen spinner. Resets to
   * false when workspaceId changes.
   */
  historyLoaded: boolean;
}

/**
 * Polls workspace messages and agent status while the agent is running.
 *
 * RN doesn't have native EventSource, so we poll as a pragmatic alternative.
 *
 * Cursor model:
 *   * The cursor (id of the newest server message we've ingested) is
 *     persisted in `useWorkspaceChatStore.lastSeenServerId[wsId]`, so a
 *     screen revisit reads it on mount and the very first poll uses
 *     `?after=<cursor>` instead of paying a full latest-N round-trip.
 *   * On a truly cold cache (no cursor in store) the first poll uses
 *     `?limit=20` — bottom of the chat fills instantly, older history
 *     loads via `?before=<id>` paging in the screen, not here.
 *   * Every subsequent poll uses `?after=<cursor>` so payload is bounded
 *     by "what's new since last tick", not by total history size.
 */
export function useWorkspaceSSE(workspaceId: string | undefined): UseWorkspaceSSEResult {
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  // Mirrors `lastSeenServerId[wsId]` in the chat store. Held as a ref so
  // updates inside `poll` don't re-trigger the useEffect that owns the
  // setInterval.
  const lastSeenIdRef = useRef<string | null>(null);
  // Single-flight guard. Without this, a slow first `?limit=20` request
  // (cold-start, no cursor) lets the setInterval fire a second poll while
  // the first is still in flight; both see cursor=null, both fetch the
  // same batch, and the screen renders 2x the messages because the cursor
  // hasn't been set yet to dedupe the second batch.
  const inFlightRef = useRef(false);
  const [historyLoaded, setHistoryLoaded] = useState(false);

  const poll = useCallback(async () => {
    if (!workspaceId) return;
    if (inFlightRef.current) return;
    inFlightRef.current = true;

    try {
      const statusRes = await api.get<{ status: string }>(`/workspaces/${workspaceId}/agent/status`);
      const store = useWorkspaceChatStore.getState();
      store.setAgentStatus(workspaceId, statusRes.status ?? 'idle');

      const cursor = lastSeenIdRef.current;
      const path = cursor
        ? `/workspaces/${workspaceId}/messages?after=${encodeURIComponent(cursor)}`
        : `/workspaces/${workspaceId}/messages?limit=${INITIAL_FETCH_LIMIT}`;
      const res = await api.get<AgentMessagesResponse>(path);
      const messages = res?.messages ?? [];
      if (messages.length) {
        // Dedup against server ids already present in the store. Defense
        // against any path that re-feeds the same server message (e.g. a
        // future cursor-rewind for retry). Empty on a true cold start, in
        // which case all incoming messages pass through.
        const existingServerIds = new Set(
          store
            .getMessages(workspaceId)
            .map((m) => m.serverId)
            .filter((s): s is string => Boolean(s))
        );
        for (const msg of messages) {
          if (existingServerIds.has(msg.id)) continue;
          if (msg.eventType === 'tool_use') {
            store.addToolUse(
              workspaceId,
              {
                toolName: msg.toolName ?? 'tool',
                toolInput: msg.toolInput ?? '',
                messageId: msg.messageId,
              },
              msg.id
            );
          } else if (msg.eventType === 'tool_result') {
            store.updateToolResult(
              workspaceId,
              msg.messageId,
              msg.isError ? 'error' : 'success'
            );
          } else if (msg.role === 'assistant' && msg.content) {
            store.appendText(workspaceId, msg.content, msg.id);
          } else if (msg.role === 'user' && msg.content) {
            // Deduplication best-effort against an optimistic addUserMessage
            // emitted by the screen when the user sent the message: if the
            // store's tail is a user message with identical content, skip.
            // Strict id-dedup doesn't work for the optimistic path because
            // optimistic inserts have no serverId yet.
            const existing = store.getMessages(workspaceId);
            const last = existing[existing.length - 1];
            if (last && last.role === 'user' && last.content === msg.content) continue;
            store.addUserMessage(
              workspaceId,
              msg.content,
              parseAttachments(msg.attachments),
              msg.id
            );
          }
        }
        // Server returns ASC order, so the last entry is the newest. Advance
        // the cursor in both the ref (for the next poll in this mount) and
        // the store (so a future remount picks it up).
        const newCursor = messages[messages.length - 1].id;
        lastSeenIdRef.current = newCursor;
        store.setLastSeenServerId(workspaceId, newCursor);
      }

      if (statusRes.status === 'idle' || statusRes.status === 'completed') {
        store.markDone(workspaceId);
      }
    } catch {
      // Silently ignore poll errors — the next tick retries.
    } finally {
      inFlightRef.current = false;
      // Drop the in-area loading indicator after the first attempt regardless
      // of outcome; otherwise a transient network error would spin forever.
      setHistoryLoaded(true);
    }
  }, [workspaceId]);

  useEffect(() => {
    if (!workspaceId) return;

    // Pull cursor from store: revisits use `?after=<cursor>`, cold starts
    // (no entry yet) fall through to the `?limit=20` path inside `poll`.
    lastSeenIdRef.current =
      useWorkspaceChatStore.getState().getLastSeenServerId(workspaceId);
    // Reset single-flight on workspace switch; a stale in-flight from a
    // previous workspace would otherwise lock out the new one.
    inFlightRef.current = false;
    setHistoryLoaded(false);

    poll();
    intervalRef.current = setInterval(poll, POLL_INTERVAL_MS);

    const subscription = AppState.addEventListener('change', (state) => {
      if (state === 'active' && !intervalRef.current) {
        intervalRef.current = setInterval(poll, POLL_INTERVAL_MS);
      } else if (state !== 'active' && intervalRef.current) {
        clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
    });

    return () => {
      if (intervalRef.current) {
        clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
      subscription.remove();
    };
  }, [workspaceId, poll]);

  return { historyLoaded };
}
