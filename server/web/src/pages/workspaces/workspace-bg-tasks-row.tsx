import { useTranslation } from 'react-i18next';
import { Bot, CalendarClock, Clock, MessageSquare, Terminal } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { BgTaskAggregateDTO } from '@/types/api';

interface Props {
  data: BgTaskAggregateDTO | undefined;
  /** When true, render compactly for embedding inside another row
   *  (drops own pl-3/mt-0.5 wrapper and the wide highlight line). */
  inline?: boolean;
}

// formatPast formats a past timestamp as "8m" / "45s" / "2h".
function formatPast(iso?: string): string {
  if (!iso) return '';
  const ms = Date.now() - new Date(iso).getTime();
  if (ms < 0) return '';
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m`;
  return `${Math.floor(min / 60)}h`;
}

// formatFuture formats a future timestamp as "in 8m" / "in 45s" / "in 2h".
function formatFuture(iso?: string): string {
  if (!iso) return '';
  const ms = new Date(iso).getTime() - Date.now();
  if (ms < 0) return 'now';
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `in ${sec}s`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `in ${min}m`;
  return `in ${Math.floor(min / 60)}h`;
}

export function WorkspaceBgTasksRow({ data, inline = false }: Props) {
  const { t } = useTranslation('workspaces');
  if (!data) return null;
  const hasAny =
    data.agent_busy ||
    data.bash_count > 0 ||
    data.wakeup_count > 0 ||
    data.subagent_count > 0 ||
    data.cron_count > 0;
  if (!hasAny) return null;

  const items: { key: string; icon: typeof MessageSquare; count: number; label: string; pulse?: boolean }[] = [
    {
      key: 'agent',
      icon: MessageSquare,
      count: data.agent_busy ? 1 : 0,
      label: t('bgTasks.agentReplying'),
      pulse: data.agent_busy,
    },
    { key: 'bash', icon: Terminal, count: data.bash_count, label: t('bgTasks.bashBackground') },
    { key: 'wakeup', icon: Clock, count: data.wakeup_count, label: t('bgTasks.wakeupQueued') },
    { key: 'subagent', icon: Bot, count: data.subagent_count, label: t('bgTasks.subagent') },
    { key: 'cron', icon: CalendarClock, count: data.cron_count, label: t('bgTasks.cronEnabled') },
  ];

  const tooltipText =
    items.filter((i) => i.count > 0).map((i) => t('bgTasks.tooltipItem', { count: i.count, label: i.label })).join(' · ') || t('bgTasks.none');

  const highlightTime =
    data.highlight?.kind === 'wakeup'
      ? formatFuture(data.highlight?.scheduled_for)
      : formatPast(data.highlight?.started_at);

  const highlightLine = data.highlight ? (
    <span
      aria-label={t('bgTasks.highlight')}
      className="ml-2 flex items-center gap-1 text-[10px] leading-none text-muted-foreground truncate max-w-[140px]"
    >
      <span className="truncate leading-none">{data.highlight.title}</span>
      {highlightTime && <span className="leading-none shrink-0">· {highlightTime}</span>}
    </span>
  ) : null;

  return (
    <div
      title={tooltipText}
      className={cn(
        'flex items-center gap-1.5 text-[10px] leading-none',
        inline ? 'shrink-0' : 'mt-0.5 pl-3'
      )}
    >
      {items.map((i) => {
        const active = i.count > 0;
        const Icon = i.icon;
        return (
          <span
            key={i.key}
            aria-label={i.label}
            className={cn(
              'flex items-center gap-0.5 leading-none',
              active ? 'text-foreground' : 'text-muted-foreground/40',
              i.pulse && 'animate-pulse'
            )}
          >
            <Icon className="w-3 h-3 shrink-0" />
            {active && i.count > 1 && <span className="text-[9px] leading-none">{i.count}</span>}
            {active && i.count === 1 && i.key !== 'agent' && <span className="text-[9px] leading-none">1</span>}
          </span>
        );
      })}
      {!inline && highlightLine}
    </div>
  );
}
