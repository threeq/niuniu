import { useEffect, useRef } from 'react';
import { AppState } from 'react-native';
import { useAuthStore } from '../stores/authStore';
import { useServerStore } from '../stores/serverStore';
import { useNotificationStore } from '../stores/notificationStore';
import { isWorkspaceSSEActive } from './sseState';

type SSECallback = (event: { type: string; data: any }) => void;

export function useSSE(onEvent: SSECallback) {
  const server = useServerStore((s) => {
    const srv = s.servers.find((x) => x.id === s.activeServerId);
    return srv ? `${srv.scheme}://${srv.host}:${srv.port}` : null;
  });
  const accessToken = useAuthStore((s) => s.accessToken);
  const retryCount = useRef(0);
  const controllerRef = useRef<AbortController | null>(null);
  const retryTimerRef = useRef<NodeJS.Timeout | null>(null);
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  useEffect(() => {
    if (!server || !accessToken) return;

    const appStateListener = AppState.addEventListener('change', (state) => {
      if (state === 'background') {
        controllerRef.current?.abort();
      } else if (state === 'active') {
        connect();
      }
    });

    function connect() {
      if (isWorkspaceSSEActive()) return; // Skip if workspace SSE is handling events
      controllerRef.current?.abort();
      const controller = new AbortController();
      controllerRef.current = controller;

      // Use Authorization header instead of query param to avoid token in logs
      fetch(`${server}/api/events/stream`, {
        signal: controller.signal,
        headers: {
          Accept: 'text/event-stream',
          Authorization: `Bearer ${accessToken}`,
        },
      })
        .then(async (response) => {
          // Handle 401 — token may have expired
          if (response.status === 401) {
            const { refreshToken, setTokens, clearTokens } = useAuthStore.getState();
            if (!refreshToken) return;
            try {
              const baseUrl = useServerStore.getState().getBaseUrl();
              const res = await fetch(`${baseUrl}/api/auth/refresh`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ refresh_token: refreshToken }),
              });
              if (!res.ok) {
                await clearTokens();
                return;
              }
              const data = await res.json();
              await setTokens(data.access_token, data.refresh_token);
              // Reconnect with new token (will be picked up by the accessToken selector)
              return;
            } catch {
              await clearTokens();
              return;
            }
          }
          if (!response.ok || !response.body) return;
          const reader = response.body.getReader();
          const decoder = new TextDecoder();
          let buffer = '';

          while (true) {
            const { done, value } = await reader.read();
            if (done) break;

            buffer += decoder.decode(value, { stream: true });
            const lines = buffer.split('\n');
            buffer = lines.pop() ?? '';

            // SSE spec-compliant parser: accumulate data lines, emit on empty line
            let eventType = '';
            let dataLines: string[] = [];

            for (const line of lines) {
              if (line.startsWith('event: ')) {
                eventType = line.slice(7);
              } else if (line.startsWith('data: ')) {
                dataLines.push(line.slice(6));
              } else if (line === '') {
                if (dataLines.length > 0) {
                  const type = eventType || 'message';
                  const data = dataLines.join('\n');
                  try {
                    onEventRef.current({ type, data: JSON.parse(data) });
                  } catch { /* ignore parse errors */ }
                  retryCount.current = 0;
                }
                // Reset for next event
                eventType = '';
                dataLines = [];
              }
            }
          }
        })
        .catch((err) => {
          if (err.name === 'AbortError') return;
          const delay = Math.min(1000 * Math.pow(2, retryCount.current), 30000);
          retryCount.current++;
          if (retryTimerRef.current) clearTimeout(retryTimerRef.current);
          retryTimerRef.current = setTimeout(connect, delay);
        });
    }

    connect();

    return () => {
      controllerRef.current?.abort();
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current);
      appStateListener.remove();
    };
  }, [server, accessToken]);
}

// ─── SSE event data shapes ───────────────────────────────────────────

interface AgentDoneData {
  workspace_id: number;
  workspace_name?: string;
}

interface AgentFailedData {
  workspace_id: number;
  workspace_name?: string;
}

// ─── useSSENotifications ─────────────────────────────────────────────
// Converts specific SSE events into persisted agent notifications.
// Intended to be mounted at the app root (e.g. _layout.tsx).

export function useSSENotifications() {
  useSSE((event: { type: string; data: any }) => {
    const { type, data } = event;

    if (type === 'agent_done') {
      const d = data as AgentDoneData;
      useNotificationStore.getState().upsertAgent({
        workspaceId: String(d.workspace_id),
        workspaceName: d.workspace_name ?? `Workspace ${d.workspace_id}`,
        status: 'completed',
        summary: `Agent completed in workspace ${d.workspace_name ?? d.workspace_id}`,
        updatedAt: new Date().toISOString(),
      });
    } else if (type === 'agent_failed') {
      const d = data as AgentFailedData;
      useNotificationStore.getState().upsertAgent({
        workspaceId: String(d.workspace_id),
        workspaceName: d.workspace_name ?? `Workspace ${d.workspace_id}`,
        status: 'failed',
        summary: `Agent failed in workspace ${d.workspace_name ?? d.workspace_id}`,
        updatedAt: new Date().toISOString(),
      });
    }
    // kanban_update events are no longer handled — Inbox is agent-only
  });
}
