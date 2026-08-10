import { useState } from 'react';
import { ChevronRight, Wrench } from 'lucide-react';
import { cn } from '@/lib/utils';
import { ToolCallCard } from './tool-call-card';
import type { TimelineEvent } from './chat-message';

interface ToolGroupProps {
  events: TimelineEvent[];
  toolResults: Map<string, { content: string; isError?: boolean }>;
}

export function ToolGroup({ events, toolResults }: ToolGroupProps) {
  const [expanded, setExpanded] = useState(false);

  if (events.length === 1) {
    const e = events[0];
    const result = e.toolUseId ? toolResults.get(e.toolUseId) : undefined;
    return (
      <ToolCallCard
        toolName={e.toolName ?? ''}
        toolInput={e.toolInput}
        toolUseId={e.toolUseId}
        result={result?.content}
        isError={result?.isError ?? e.isError}
      />
    );
  }

  const completedCount = events.filter(e => {
    const r = e.toolUseId ? toolResults.get(e.toolUseId) : undefined;
    return r != null;
  }).length;
  const allDone = completedCount === events.length;
  const hasError = events.some(e => {
    const r = e.toolUseId ? toolResults.get(e.toolUseId) : undefined;
    return r?.isError;
  });

  const toolNames = [...new Set(events.map(e => e.toolName).filter(Boolean))];

  return (
    <div className="my-0.5 text-xs font-mono">
      <button
        onClick={() => setExpanded(v => !v)}
        className="flex w-full items-center gap-1.5 text-left py-0.5 text-muted-foreground hover:text-foreground group"
      >
        <ChevronRight
          className={cn(
            'h-3 w-3 shrink-0 text-muted-foreground/70 transition-transform',
            expanded && 'rotate-90',
          )}
        />
        <Wrench className="h-3.5 w-3.5 shrink-0" />
        <span className="font-semibold">{events.length} tool uses</span>
        {!expanded && (
          <span className="text-muted-foreground/70 truncate">
            ({toolNames.join(', ')})
          </span>
        )}
      </button>

      {!expanded && (
        <div className="flex items-center gap-1.5 pl-5 text-muted-foreground/70 py-0.5">
          <span className="text-muted-foreground/50 shrink-0">⎿</span>
          {allDone ? (
            <span className={hasError ? 'text-red-500' : ''}>
              Done ({completedCount} tool uses)
            </span>
          ) : (
            <span className="text-blue-400">
              {completedCount}/{events.length} completed...
            </span>
          )}
        </div>
      )}

      {expanded && (
        <div className="pl-3 border-l border-border ml-1.5 space-y-0">
          {events.map(e => {
            const result = e.toolUseId ? toolResults.get(e.toolUseId) : undefined;
            return (
              <ToolCallCard
                key={e.id}
                toolName={e.toolName ?? ''}
                toolInput={e.toolInput}
                toolUseId={e.toolUseId}
                result={result?.content}
                isError={result?.isError ?? e.isError}
              />
            );
          })}
        </div>
      )}
    </div>
  );
}
