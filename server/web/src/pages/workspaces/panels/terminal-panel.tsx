import { useEffect } from 'react';
import { useTerminal } from '@/lib/hooks/use-terminal';
import { useTerminalStore } from '@/stores/terminal-store';
import { useThemeStore } from '@/stores/theme-store';
import { LIGHT_TERMINAL_THEME, DARK_TERMINAL_THEME } from '@/lib/terminal-themes';
import { cn } from '@/lib/utils';

interface TerminalPanelProps {
  workspaceId: string;
}

export function TerminalPanel({ workspaceId }: TerminalPanelProps) {
  const { terminalRef, isConnected, isReconnecting } = useTerminal({ workspaceId });
  const resolvedTheme = useThemeStore((s) => s.resolvedTheme);

  // Fix 2: Clean up terminal resources when panel unmounts
  useEffect(() => {
    return () => {
      const store = useTerminalStore.getState();
      const terminal = store.getTerminal(workspaceId);
      if (terminal) {
        terminal.dispose();
      }
      store.disconnect(workspaceId);
    };
  }, [workspaceId]);

  const termBg = resolvedTheme === 'dark' ? DARK_TERMINAL_THEME.background : LIGHT_TERMINAL_THEME.background;

  return (
    <div className="flex flex-col h-full" style={{ background: termBg }}>
      {/* Header bar */}
      <div className="flex items-center justify-between px-3 py-1.5 border-b border-border bg-background/80 shrink-0">
        <span className="text-xs font-medium text-foreground">Terminal</span>
        <div className="flex items-center gap-1.5">
          <span
            className={cn(
              'h-2 w-2 rounded-full',
              isReconnecting
                ? 'bg-amber-400 animate-pulse'
                : isConnected
                ? 'bg-green-400'
                : 'bg-red-400',
            )}
          />
          <span className="text-xs text-muted-foreground">
            {isReconnecting ? 'Reconnecting…' : isConnected ? 'Connected' : 'Disconnected'}
          </span>
        </div>
      </div>

      {/* Terminal area */}
      <div ref={terminalRef} className="flex-1 min-h-0 overflow-hidden p-1" />
    </div>
  );
}
