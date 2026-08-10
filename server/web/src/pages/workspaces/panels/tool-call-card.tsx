import { useState } from 'react';
import {
  FileEdit,
  Terminal,
  Search,
  FileText,
  ListTodo,
  Users,
  Wrench,
  ChevronRight,
  Loader2,
} from 'lucide-react';
import { cn } from '@/lib/utils';

interface ToolCallCardProps {
  toolName: string;
  toolInput?: string;
  toolUseId?: string;
  result?: string;
  isError?: boolean;
}

const ICON_MAP: Record<string, React.ElementType> = {
  Edit: FileEdit,
  Write: FileEdit,
  Bash: Terminal,
  Grep: Search,
  Glob: Search,
  Read: FileText,
  TodoWrite: ListTodo,
  TaskCreate: ListTodo,
  TaskUpdate: ListTodo,
  Agent: Users,
};

function getToolIcon(toolName: string): React.ElementType {
  return ICON_MAP[toolName] ?? Wrench;
}

interface AgentToolInput {
  description?: string;
  prompt?: string;
  model?: string;
  subagent_type?: string;
  isolation?: string;
  run_in_background?: boolean;
}

function parseAgentInput(toolInput?: string): AgentToolInput | null {
  if (!toolInput) return null;
  try {
    return JSON.parse(toolInput) as AgentToolInput;
  } catch {
    return null;
  }
}

function extractSummary(toolName: string, toolInput?: string): string {
  if (!toolInput) return '';
  try {
    const parsed = JSON.parse(toolInput);
    if (['Edit', 'Write', 'Read'].includes(toolName) && parsed.file_path) {
      return parsed.file_path;
    }
    if (toolName === 'Bash' && parsed.command) {
      return String(parsed.command).slice(0, 120);
    }
    if (['Grep', 'Glob'].includes(toolName) && parsed.pattern) {
      return parsed.pattern;
    }
    if (toolName === 'Agent' && parsed.description) {
      return parsed.description;
    }
    // Fallback: first string value found
    const firstStr = Object.values(parsed).find((v) => typeof v === 'string');
    if (firstStr) return String(firstStr).slice(0, 120);
  } catch {
    return toolInput.slice(0, 120);
  }
  return '';
}

/** Estimate result size for display */
function formatResultMeta(result: string): string {
  const len = result.length;
  if (len > 1024) {
    return `${(len / 1024).toFixed(1)}k chars`;
  }
  return `${len} chars`;
}

export function ToolCallCard({ toolName, toolInput, result, isError }: ToolCallCardProps) {
  const [expanded, setExpanded] = useState(false);

  // getToolIcon returns a stable lucide-react component reference from a static ICON_MAP
  const Icon = getToolIcon(toolName);
  const summary = extractSummary(toolName, toolInput);
  const isDone = result != null;
  const isAgent = toolName === 'Agent';

  return (
    <div className="my-0.5 text-xs font-mono">
      {/* Header line: ToolName(summary) */}
      <button
        onClick={() => setExpanded((v) => !v)}
        className={cn(
          'flex w-full items-center gap-1.5 text-left py-0.5 group',
          isError ? 'text-destructive' : 'text-muted-foreground hover:text-foreground',
        )}
      >
        <ChevronRight
          className={cn(
            'h-3 w-3 shrink-0 text-muted-foreground/70 transition-transform',
            expanded && 'rotate-90',
          )}
        />
        {/* eslint-disable-next-line react-hooks/static-components -- Icon is a stable lucide-react ref from a static ICON_MAP, not a dynamically-created component */}
        <Icon className="h-3.5 w-3.5 shrink-0" />
        <span className={cn('font-semibold', isError && 'text-destructive')}>
          {toolName}
        </span>
        {summary && (
          <span className="text-muted-foreground/70 truncate">
            ({summary})
          </span>
        )}
        {!isDone && (
          <Loader2 className="h-3 w-3 shrink-0 animate-spin text-blue-400 ml-auto" />
        )}
      </button>

      {/* Agent tool badges (model, isolation, background) */}
      {isAgent && !expanded && (() => {
        const agentInput = parseAgentInput(toolInput);
        if (!agentInput) return null;
        return (
          <div className="flex items-center gap-1 pl-5 py-0.5 flex-wrap">
            {agentInput.model && (
              <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-info/10 text-info border border-info/30">
                {agentInput.model}
              </span>
            )}
            {agentInput.subagent_type && agentInput.subagent_type !== 'general-purpose' && (
              <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-purple-50 text-purple-600 border border-purple-200 dark:bg-purple-950/30 dark:text-purple-300 dark:border-purple-800">
                {agentInput.subagent_type}
              </span>
            )}
            {agentInput.isolation === 'worktree' && (
              <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-warning/10 text-warning border border-warning/30">
                worktree
              </span>
            )}
            {agentInput.run_in_background && (
              <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-success/10 text-success border border-success/30">
                background
              </span>
            )}
          </div>
        );
      })()}

      {/* Result summary line: ⎿  Done / Error */}
      {isDone && !expanded && (
        <div className="flex items-start gap-1.5 pl-5 text-muted-foreground/70 py-0.5">
          <span className="text-muted-foreground/50 shrink-0">⎿</span>
          {isError ? (
            <span className="text-destructive truncate">
              Error: {result!.slice(0, 120)}
            </span>
          ) : isAgent ? (
            <span>Done ({formatResultMeta(result!)})</span>
          ) : (
            <span className="truncate text-muted-foreground">
              {result!.slice(0, 200).replace(/\n/g, ' ')}
            </span>
          )}
        </div>
      )}

      {/* Expanded body */}
      {expanded && (
        <div className="pl-5 pb-1 pt-0.5 space-y-1">
          {toolInput && (
            <pre className="whitespace-pre-wrap break-all rounded bg-muted p-2 text-muted-foreground overflow-x-auto max-h-48 text-[11px]">
              {formatJSON(toolInput.slice(0, 2000))}
              {toolInput.length > 2000 && (
                <span className="text-muted-foreground/70"> …(truncated)</span>
              )}
            </pre>
          )}
          {result != null && (
            <div>
              <div className={cn('font-semibold mb-0.5', isError ? 'text-destructive' : 'text-muted-foreground/70')}>
                {isError ? '✗ Error' : '✓ Result'}
              </div>
              <pre className={cn(
                'whitespace-pre-wrap break-all rounded p-2 overflow-x-auto max-h-64 text-[11px]',
                isError ? 'bg-destructive/10 text-destructive dark:bg-red-950/30 dark:text-red-300' : 'bg-muted text-muted-foreground',
              )}>
                {result.slice(0, 5000)}
                {result.length > 5000 && (
                  <span className="text-muted-foreground/70"> …(truncated)</span>
                )}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function formatJSON(input: string): string {
  try {
    return JSON.stringify(JSON.parse(input), null, 2);
  } catch {
    return input;
  }
}
