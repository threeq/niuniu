import { useEffect, useRef, useCallback, useState } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebLinksAddon } from '@xterm/addon-web-links';
import { useTerminalStore } from '@/stores/terminal-store';
import { useThemeStore } from '@/stores/theme-store';
import { LIGHT_TERMINAL_THEME, DARK_TERMINAL_THEME } from '@/lib/terminal-themes';

interface UseTerminalOptions {
  workspaceId: string;
  onData?: (data: string) => void;
}

interface UseTerminalReturn {
  terminalRef: React.RefObject<HTMLDivElement | null>;
  isConnected: boolean;
  isReconnecting: boolean;
  write: (data: string) => void;
  resize: (cols: number, rows: number) => void;
}

export function useTerminal({ workspaceId, onData }: UseTerminalOptions): UseTerminalReturn {
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme);
  const terminalRef = useRef<HTMLDivElement | null>(null);
  const terminalInstanceRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const [isConnected, setIsConnected] = useState(false);
  const [isReconnecting, setIsReconnecting] = useState(false);

  const connect = useTerminalStore((state) => state.connect);
  const getConnection = useTerminalStore((state) => state.getConnection);
  const setTerminal = useTerminalStore((state) => state.setTerminal);
  const getTerminal = useTerminalStore((state) => state.getTerminal);

  // Try to use existing connection or create new one
  const initConnection = useCallback(() => {
    if (!workspaceId) return;

    // Check if we already have a connection for this workspace
    let ws = getConnection(workspaceId);

    if (!ws || ws.readyState !== WebSocket.OPEN) {
      // Create new connection via global store
      ws = connect(workspaceId);
    }

    if (!ws) return;

    wsRef.current = ws;

    // Set up WebSocket handlers
    ws.onopen = () => {
      setIsConnected(true);
      setIsReconnecting(false);
    };

    ws.onclose = () => {
      setIsConnected(false);
      // Don't auto-reconnect here - let the UI decide
      // If user switches back, the store will try to reuse or recreate
    };

    ws.onerror = () => {
      // Let onclose handle cleanup
    };

    ws.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) {
        const decoder = new TextDecoder();
        const text = decoder.decode(event.data);
        terminalInstanceRef.current?.write(text);
      } else if (typeof event.data === 'string') {
        terminalInstanceRef.current?.write(event.data);
      }
    };

    return ws;
  }, [workspaceId, connect, getConnection]);

  useEffect(() => {
    if (!terminalRef.current || !workspaceId) return;

    // Check if we have an existing terminal for this workspace
    const existingTerminal = getTerminal(workspaceId);

    // Initialize terminal
    const terminal = existingTerminal || new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: '"JetBrains Mono", "Fira Code", Consolas, "Microsoft YaHei UI", "PingFang SC", "Hiragino Sans GB", "Noto Sans Mono CJK SC", "Noto Sans CJK SC", monospace',
      theme: resolvedTheme === 'dark' ? DARK_TERMINAL_THEME : LIGHT_TERMINAL_THEME,
    });

    if (!existingTerminal) {
      const fitAddon = new FitAddon();
      const webLinksAddon = new WebLinksAddon();

      terminal.loadAddon(fitAddon);
      terminal.loadAddon(webLinksAddon);
      fitAddonRef.current = fitAddon;
    } else {
      // Reuse existing fit addon if available
      const existingFitAddon = fitAddonRef.current;
      if (existingFitAddon) {
        terminal.loadAddon(existingFitAddon);
      }
    }

    terminal.open(terminalRef.current);

    if (!existingTerminal) {
      // Only fit if this is a new terminal
      fitAddonRef.current?.fit();
    }

    terminalInstanceRef.current = terminal;

    // Initialize WebSocket connection first (creates the store entry)
    // then store the terminal instance (requires the store entry to exist)
    initConnection();
    setTerminal(workspaceId, terminal);

    // Handle terminal input
    const handleData = (data: string) => {
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        wsRef.current.send(data);
      }
      onData?.(data);
    };

    terminal.onData(handleData);

    // Handle resize
    const handleResize = () => {
      fitAddonRef.current?.fit();
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        const cols = terminal.cols;
        const rows = terminal.rows;
        wsRef.current.send(JSON.stringify({ type: 'resize', cols, rows }));
      }
    };

    const resizeObserver = new ResizeObserver(handleResize);
    if (terminalRef.current) {
      resizeObserver.observe(terminalRef.current);
    }

    return () => {
      resizeObserver.disconnect();
      // Don't dispose terminal or close WebSocket here - they're managed globally
      // Just clear the local refs
      terminalInstanceRef.current = null;
      fitAddonRef.current = null;
      wsRef.current = null;
    };
    // resolvedTheme intentionally omitted: the terminal is created once with
    // the initial palette; the separate effect below hot-swaps theme in place
    // without destroying the xterm instance (which would lose scrollback).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, connect, getConnection, initConnection, setTerminal, getTerminal, onData]);

  useEffect(() => {
    if (terminalInstanceRef.current) {
      terminalInstanceRef.current.options.theme =
        resolvedTheme === 'dark' ? DARK_TERMINAL_THEME : LIGHT_TERMINAL_THEME;
    }
  }, [resolvedTheme]);

  // Reconnect handler - exposed for manual reconnect if needed
  useEffect(() => {
    if (!wsRef.current && workspaceId) {
      // Try to reconnect after a delay
      const timer = setTimeout(() => {
        if (!wsRef.current) {
          setIsReconnecting(true);
          initConnection();
        }
      }, 2000);
      return () => clearTimeout(timer);
    }
  }, [workspaceId, initConnection]);

  const write = useCallback((data: string) => {
    terminalInstanceRef.current?.write(data);
  }, []);

  const resize = useCallback((cols: number, rows: number) => {
    terminalInstanceRef.current?.resize(cols, rows);
    fitAddonRef.current?.fit();
  }, []);

  return {
    terminalRef,
    isConnected,
    isReconnecting,
    write,
    resize,
  };
}
