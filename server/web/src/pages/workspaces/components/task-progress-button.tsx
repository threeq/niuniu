import { useEffect, useMemo, useRef, useState } from 'react';
import { Loader2, Check, AlertTriangle } from 'lucide-react';
import { cn } from '@/lib/utils';
import { useWorkspaceTaskStore } from '@/stores/workspace-task-store';
import { TaskListPopover } from './task-list-popover';
import type { TaskUpdatePayload } from '@/types/workspace-task';

interface TaskProgressButtonProps {
  onClickTask?: (messageId: string) => void;
}

export function TaskProgressButton({ onClickTask }: TaskProgressButtonProps) {
  const tasks = useWorkspaceTaskStore((s) => s.tasks);

  const { hasVisible, progress, currentTask, hasInterrupted } = useMemo(() => {
    let completed = 0, total = 0, hasVis = false, hasInt = false;
    let current: TaskUpdatePayload | undefined;
    for (const task of tasks.values()) {
      if (task.status === 'deleted') continue;
      hasVis = true;
      total++;
      if (task.status === 'completed') completed++;
      if (task.status === 'in_progress' && !current) current = task;
      if (task.status === 'interrupted') hasInt = true;
    }
    return {
      hasVisible: hasVis,
      progress: { completed, total },
      currentTask: current,
      hasInterrupted: hasInt,
    };
  }, [tasks]);

  const [open, setOpen] = useState(false);
  const [visible, setVisible] = useState(false);
  const fadeTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const popoverRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (fadeTimerRef.current) clearTimeout(fadeTimerRef.current);

    if (hasVisible) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- timer-driven fade-out: setState is coordinated with setTimeout to implement a delayed hide; splitting into separate effects would break the timer cancellation on hasVisible change
      setVisible(true);
      const allDone = progress.total > 0 && progress.completed === progress.total && !hasInterrupted;
      if (allDone) {
        fadeTimerRef.current = setTimeout(() => setVisible(false), 5000);
      }
    } else {
      setVisible(false);
    }

    return () => {
      if (fadeTimerRef.current) clearTimeout(fadeTimerRef.current);
    };
  }, [hasVisible, progress.completed, progress.total, hasInterrupted]);

  useEffect(() => {
    if (!open) return;
    const handler = (e: MouseEvent) => {
      if (popoverRef.current && !popoverRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('keydown', handler);
    return () => document.removeEventListener('keydown', handler);
  }, [open]);

  if (!visible) return null;

  const isRunning = !!currentTask;
  const allComplete = progress.total > 0 && progress.completed === progress.total && !hasInterrupted;
  const truncatedName = currentTask
    ? (currentTask.activeForm || currentTask.subject).slice(0, 20) +
      ((currentTask.activeForm || currentTask.subject).length > 20 ? '…' : '')
    : '';

  return (
    <div className="relative" ref={popoverRef}>
      <button
        onClick={() => setOpen(!open)}
        className={cn(
          'flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs transition-colors',
          allComplete
            ? 'bg-success/10 text-success'
            : hasInterrupted
            ? 'bg-warning/10 text-warning'
            : 'bg-info/10 text-info',
        )}
      >
        {isRunning && <Loader2 className="w-3 h-3 animate-spin" />}
        {allComplete && <Check className="w-3 h-3" />}
        {hasInterrupted && !isRunning && <AlertTriangle className="w-3 h-3" />}

        <span className="font-medium">
          {progress.completed}/{progress.total}
        </span>

        {isRunning && (
          <>
            <span className="text-muted-foreground/50">·</span>
            <span className="truncate max-w-[100px]">{truncatedName}</span>
          </>
        )}

        {allComplete && <span>complete</span>}
        {hasInterrupted && !isRunning && <span>interrupted</span>}
      </button>

      {open && (
        <div className="absolute bottom-full left-0 mb-1 w-80 bg-card rounded-lg shadow-lg border border-border z-50">
          <TaskListPopover onClickTask={(msgId) => { setOpen(false); onClickTask?.(msgId); }} />
        </div>
      )}
    </div>
  );
}
