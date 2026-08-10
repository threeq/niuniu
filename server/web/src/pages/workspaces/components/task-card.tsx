import { useEffect, useState } from 'react';
import { Loader2, Check, Circle, AlertTriangle, X } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { TaskUpdatePayload } from '@/types/workspace-task';

interface TaskCardProps {
  task: TaskUpdatePayload;
  onClickTask?: (messageId: string) => void;
}

function formatDuration(ms: number): string {
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const secs = seconds % 60;
  return `${minutes}m ${secs}s`;
}

export function TaskCard({ task, onClickTask }: TaskCardProps) {
  const [elapsed, setElapsed] = useState('');
  const [hidden, setHidden] = useState(false);

  useEffect(() => {
    if (task.status !== 'in_progress' || !task.startedAt) return;
    const update = () => setElapsed(formatDuration(Date.now() - task.startedAt!));
    update();
    const timer = setInterval(update, 1000);
    return () => clearInterval(timer);
  }, [task.status, task.startedAt]);

  if (hidden) return null;

  const completedDuration =
    task.completedAt && task.startedAt
      ? formatDuration(task.completedAt - task.startedAt)
      : null;

  return (
    <button
      onTransitionEnd={() => { if (task.status === 'deleted') setHidden(true); }}
      onClick={() => task.messageId && onClickTask?.(task.messageId)}
      className={cn(
        'flex items-center gap-2.5 w-full px-3 py-2 rounded-md text-left transition-all duration-300',
        'hover:bg-muted',
        task.status === 'in_progress' && 'bg-blue-50/50 animate-pulse',
        task.status === 'deleted' && 'opacity-0 h-0 py-0 overflow-hidden',
      )}
    >
      <div className="flex-shrink-0">
        {task.status === 'in_progress' && (
          <Loader2 className="w-4 h-4 text-blue-500 animate-spin" />
        )}
        {task.status === 'completed' && (
          <Check className="w-4 h-4 text-green-500" />
        )}
        {task.status === 'pending' && (
          <Circle className="w-4 h-4 text-muted-foreground/50" />
        )}
        {task.status === 'interrupted' && (
          <AlertTriangle className="w-4 h-4 text-amber-500" />
        )}
        {task.status === 'deleted' && (
          <X className="w-4 h-4 text-muted-foreground/50" />
        )}
      </div>

      <div className="flex-1 min-w-0">
        <div className="text-sm text-foreground truncate">
          {task.status === 'in_progress' && task.activeForm
            ? task.activeForm
            : task.subject}
        </div>
        {(elapsed || completedDuration) && (
          <div className="text-xs text-muted-foreground/70 mt-0.5">
            {task.status === 'in_progress' ? elapsed : completedDuration}
          </div>
        )}
      </div>
    </button>
  );
}
