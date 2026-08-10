import { useTranslation } from 'react-i18next'
import {
  Circle, Loader2, CircleCheck, CircleAlert, Pause, ScanSearch,
  ShieldAlert, MessageCircleQuestion, CircleSlash,
} from 'lucide-react'
import { cn } from '@/lib/utils'
import type { ExecStatus } from '@/types/api'

type Props = { status: ExecStatus | null | undefined; className?: string }

// Executable-Epic exec_status -> design-system token mapping. Workflow/status
// tokens only (col-in-progress, col-done, destructive, warning, warm-muted);
// 'idle' uses neutral warm tokens so it never reads as a semantic state.
// gate_blocked (blocked-needs-human) reads as destructive; waiting_input
// (waiting-user-input) as warning; abandoned stays neutral so it reads "parked"
// not "error" (spec section 19).
const STYLES: Record<ExecStatus, string> = {
  idle:          'bg-warm-muted text-warm-text-muted border-warm-border',
  running:       'bg-col-in-progress/10 text-col-in-progress border-col-in-progress/30',
  reviewing:     'bg-info/10 text-info border-info/30',
  done:          'bg-col-done/10 text-col-done border-col-done/30',
  failed:        'bg-destructive/10 text-destructive border-destructive/30',
  paused:        'bg-warning/10 text-warning border-warning/30',
  gate_checking: 'bg-info/10 text-info border-info/30',
  gate_blocked:  'bg-destructive/10 text-destructive border-destructive/30',
  waiting_input: 'bg-warning/10 text-warning border-warning/30',
  abandoned:     'bg-warm-muted text-warm-text-muted border-warm-border',
}

const ICON: Record<ExecStatus, React.ComponentType<{ className?: string }>> = {
  idle:          Circle,
  running:       Loader2,
  reviewing:     ScanSearch,
  done:          CircleCheck,
  failed:        CircleAlert,
  paused:        Pause,
  gate_checking: Loader2,
  gate_blocked:  ShieldAlert,
  waiting_input: MessageCircleQuestion,
  abandoned:     CircleSlash,
}

const LABEL_KEY: Record<ExecStatus, string> = {
  idle:          'kanban.epic.status.idle',
  running:       'kanban.epic.status.running',
  reviewing:     'kanban.epic.status.reviewing',
  done:          'kanban.epic.status.done',
  failed:        'kanban.epic.status.failed',
  paused:        'kanban.epic.status.paused',
  gate_checking: 'kanban.epic.status.gateChecking',
  gate_blocked:  'kanban.epic.status.gateBlocked',
  waiting_input: 'kanban.epic.status.waitingInput',
  abandoned:     'kanban.epic.status.abandoned',
}

// Statuses whose icon should spin (in-flight).
const SPIN: Partial<Record<ExecStatus, boolean>> = { running: true, gate_checking: true }

export function ExecStatusBadge({ status, className }: Props) {
  const { t } = useTranslation('projects')
  if (!status) return null
  const Icon = ICON[status]
  return (
    <span
      role="status"
      className={cn(
        'inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[10px] font-medium',
        STYLES[status],
        className,
      )}
    >
      <Icon className={cn('h-3 w-3', SPIN[status] && 'animate-spin')} aria-hidden="true" />
      {t(LABEL_KEY[status])}
    </span>
  )
}
