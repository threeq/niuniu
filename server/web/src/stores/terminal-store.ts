import { create } from 'zustand';
import { getAccessToken } from '@/stores/auth-store';

interface TerminalState {
  // WebSocket connection per workspace
  connections: Map<string, {
    ws: WebSocket;
    terminalInstance: import('@xterm/xterm').Terminal | null;
  }>;

  // Connect to a workspace's terminal
  connect: (workspaceId: string) => WebSocket | null;

  // Disconnect from a workspace's terminal
  disconnect: (workspaceId: string) => void;

  // Get existing connection
  getConnection: (workspaceId: string) => WebSocket | null;

  // Write data to terminal WebSocket (sends to PTY stdin)
  writeToTerminal: (workspaceId: string, data: string) => boolean;

  // Set terminal instance for a workspace
  setTerminal: (workspaceId: string, terminal: import('@xterm/xterm').Terminal | null) => void;

  // Get terminal instance for a workspace
  getTerminal: (workspaceId: string) => import('@xterm/xterm').Terminal | null;
}

export const useTerminalStore = create<TerminalState>((set, get) => ({
  connections: new Map(),

  connect: (workspaceId: string) => {
    const state = get();
    // Already connected?
    const existing = state.connections.get(workspaceId);
    if (existing?.ws && existing.ws.readyState === WebSocket.OPEN) {
      return existing.ws;
    }

    // Use relative URL so Vite proxy handles it (proxy requires relative path, not absolute ws://)
    const token = getAccessToken();
    const tokenParam = token ? `?token=${encodeURIComponent(token)}` : '';
    const wsUrl = `/ws/workspaces/${workspaceId}/terminal${tokenParam}`;

    const ws = new WebSocket(wsUrl);
    ws.binaryType = 'arraybuffer';

    ws.onclose = () => {
      // Clean up on close
      const current = get().connections.get(workspaceId);
      if (current?.ws === ws) {
        // Don't auto-reconnect here - let the UI decide
        const newConnections = new Map(get().connections);
        newConnections.delete(workspaceId);
        set({ connections: newConnections });
      }
    };

    // Store the connection
    const newConnections = new Map(get().connections);
    newConnections.set(workspaceId, { ws, terminalInstance: null });
    set({ connections: newConnections });

    return ws;
  },

  disconnect: (workspaceId: string) => {
    const state = get();
    const conn = state.connections.get(workspaceId);
    if (conn) {
      conn.ws.close();
      const newConnections = new Map(get().connections);
      newConnections.delete(workspaceId);
      set({ connections: newConnections });
    }
  },

  getConnection: (workspaceId: string) => {
    const conn = get().connections.get(workspaceId);
    return conn?.ws || null;
  },

  writeToTerminal: (workspaceId: string, data: string) => {
    const conn = get().connections.get(workspaceId);
    if (conn?.ws && conn.ws.readyState === WebSocket.OPEN) {
      conn.ws.send(data);
      return true;
    }
    return false;
  },

  setTerminal: (workspaceId: string, terminal: import('@xterm/xterm').Terminal | null) => {
    const state = get();
    const conn = state.connections.get(workspaceId);
    if (conn) {
      const newConnections = new Map(get().connections);
      newConnections.set(workspaceId, { ...conn, terminalInstance: terminal });
      set({ connections: newConnections });
    }
  },

  getTerminal: (workspaceId: string) => {
    return get().connections.get(workspaceId)?.terminalInstance || null;
  },
}));
