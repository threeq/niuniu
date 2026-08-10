import { useState, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { RefreshCw, Pin, Plus, Play, Pencil, Trash2 } from 'lucide-react'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'
import { useScheduleMutations } from '@/lib/hooks/use-schedule-mutations'
import { ScheduleFormDialog } from './schedule-form-dialog'
import { ScheduleHistoryDrawer } from './schedule-history-drawer'
import { OwnerFilter } from '@/components/shared/owner-filter'
import { useAuthStore } from '@/stores/auth-store'
import type { OwnerRef } from '@/types/org'
import type { GlobalSchedule } from '@/types/api'
import { confirm } from '@/lib/confirm'

type Filter = 'all' | 'enabled' | 'disabled'

export function SchedulesPage() {
  const { t } = useTranslation('schedules')
  const currentUser = useAuthStore((s) => s.user)
  const [filter, setFilter] = useState<Filter>('all')
  const [ownerFilter, setOwnerFilter] = useState<OwnerRef[] | 'all'>('all')
  const [formOpen, setFormOpen] = useState(false)
  const [formWorkspaceId, setFormWorkspaceId] = useState<number | null>(null)
  const [editSchedule, setEditSchedule] = useState<GlobalSchedule | undefined>()
  const [historyOpen, setHistoryOpen] = useState(false)
  const [historySchedule, setHistorySchedule] = useState<GlobalSchedule | null>(null)

  const { data: schedules = [] } = useQuery({
    queryKey: ['schedules'],
    queryFn: () => api.get<GlobalSchedule[]>('/schedules'),
  })

  const filtered = useMemo(() => {
    if (filter === 'all') return schedules
    if (filter === 'enabled') return schedules.filter((s) => s.enabled)
    return schedules.filter((s) => !s.enabled)
  }, [schedules, filter])

  const grouped = useMemo(() => {
    const map = new Map<number, { name: string; status: string; schedules: GlobalSchedule[] }>()
    for (const s of filtered) {
      if (!map.has(s.workspace_id)) {
        map.set(s.workspace_id, {
          name: s.workspace_name,
          status: s.workspace_status,
          schedules: [],
        })
      }
      map.get(s.workspace_id)!.schedules.push(s)
    }
    return Array.from(map.entries())
  }, [filtered])

  const enabledCount = schedules.filter((s) => s.enabled).length
  const disabledCount = schedules.filter((s) => !s.enabled).length

  const openAdd = (workspaceId: number) => {
    setFormWorkspaceId(workspaceId)
    setEditSchedule(undefined)
    setFormOpen(true)
  }

  const openEdit = (schedule: GlobalSchedule) => {
    setFormWorkspaceId(schedule.workspace_id)
    setEditSchedule(schedule)
    setFormOpen(true)
  }

  const openHistory = (schedule: GlobalSchedule) => {
    setHistorySchedule(schedule)
    setHistoryOpen(true)
  }

  const statusDot: Record<string, string> = {
    running: 'bg-success',
    attention: 'bg-destructive',
    needs_review: 'bg-warning',
    created: 'bg-warning',
    idle: 'bg-muted-foreground/50',
    completed: 'bg-muted-foreground/50',
  }

  return (
    <div className="h-full overflow-y-auto">
      <div className="p-6 max-w-4xl mx-auto">
      {/* Header */}
      <div className="flex items-center justify-between mb-5">
        <div>
          <h1 className="text-lg font-semibold text-foreground">{t('list.title')}</h1>
          <p className="text-sm text-muted-foreground mt-0.5">{t('list.subtitle')}</p>
        </div>
        <div className="flex items-center gap-3">
          <OwnerFilter value={ownerFilter} onChange={setOwnerFilter} userId={currentUser?.id ?? 0} />
        </div>
        <div className="flex gap-1">
          {([
            ['all', t('list.filterAll', { count: schedules.length })],
            ['enabled', t('list.filterEnabled', { count: enabledCount })],
            ['disabled', t('list.filterDisabled', { count: disabledCount })],
          ] as [Filter, string][]).map(([key, label]) => (
            <button
              key={key}
              onClick={() => setFilter(key)}
              className={cn(
                'px-2.5 py-1 rounded-md text-xs transition-colors',
                filter === key
                  ? 'bg-info/10 text-info'
                  : 'bg-muted text-muted-foreground hover:bg-accent'
              )}
            >
              {label}
            </button>
          ))}
        </div>
      </div>

      {/* Workspace groups */}
      {grouped.length === 0 ? (
        <div className="border border-dashed rounded-lg p-8 text-center text-sm text-muted-foreground">
          {filter === 'all' ? t('list.emptyAll') : t('common:status.noMatchingResults')}
        </div>
      ) : (
        <div className="space-y-4">
          {grouped.map(([wsId, group]) => (
            <WorkspaceScheduleGroup
              key={wsId}
              workspaceId={wsId}
              name={group.name}
              status={group.status}
              schedules={group.schedules}
              statusDot={statusDot}
              onAdd={() => openAdd(wsId)}
              onEdit={openEdit}
              onHistory={openHistory}
            />
          ))}
        </div>
      )}

      {/* Form dialog */}
      {formWorkspaceId !== null && (
        <ScheduleFormDialog
          open={formOpen}
          onOpenChange={setFormOpen}
          workspaceId={formWorkspaceId}
          schedule={editSchedule}
        />
      )}

      {/* History drawer */}
      <ScheduleHistoryDrawer
        open={historyOpen}
        onOpenChange={setHistoryOpen}
        schedule={historySchedule}
      />
      </div>
    </div>
  )
}

interface WorkspaceScheduleGroupProps {
  workspaceId: number
  name: string
  status: string
  schedules: GlobalSchedule[]
  statusDot: Record<string, string>
  onAdd: () => void
  onEdit: (s: GlobalSchedule) => void
  onHistory: (s: GlobalSchedule) => void
}

function WorkspaceScheduleGroup({
  workspaceId,
  name,
  status,
  schedules,
  statusDot,
  onAdd,
  onEdit,
  onHistory,
}: WorkspaceScheduleGroupProps) {
  const { t } = useTranslation('schedules')
  return (
    <div className="bg-card border border-border rounded-lg">
      <div className="flex items-center justify-between px-4 py-3 border-b border-border">
        <div className="flex items-center gap-2">
          <span className={cn('w-1.5 h-1.5 rounded-full', statusDot[status] || 'bg-muted-foreground/50')} />
          <span className="text-sm font-medium text-foreground">{name || t('list.workspaceFallback', { id: workspaceId })}</span>
          <span className="text-[11px] text-muted-foreground/70 bg-muted px-1.5 py-0.5 rounded">
            {t('list.tasksCountSuffix', { count: schedules.length })}
          </span>
        </div>
        <button
          onClick={onAdd}
          className="flex items-center gap-0.5 text-xs text-info hover:text-info/80"
        >
          <Plus className="w-3 h-3" />
          {t('list.addTask')}
        </button>
      </div>
      {schedules.map((s) => (
        <ScheduleRow
          key={s.id}
          schedule={s}
          workspaceId={workspaceId}
          onEdit={() => onEdit(s)}
          onHistory={() => onHistory(s)}
        />
      ))}
    </div>
  )
}

interface ScheduleRowProps {
  schedule: GlobalSchedule
  workspaceId: number
  onEdit: () => void
  onHistory: () => void
}

function ScheduleRow({ schedule: s, workspaceId, onEdit, onHistory }: ScheduleRowProps) {
  const { t } = useTranslation('schedules')
  const { toggle, trigger, remove } = useScheduleMutations(workspaceId)

  const isOverdue = s.next_run_at && new Date(s.next_run_at) < new Date()
  const isFired = s.schedule_type === 'once' && s.fired_at

  const handleDelete = async () => {
    if (await confirm(t('list.confirmDelete'))) {
      remove.mutate(s.id)
    }
  }

  return (
    <div
      className="flex items-center gap-3 px-4 py-2.5 border-b border-border/50 last:border-0 hover:bg-accent cursor-pointer"
      onClick={onHistory}
    >
      {s.schedule_type === 'cron' ? (
        <RefreshCw className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
      ) : (
        <Pin className="w-3.5 h-3.5 text-muted-foreground shrink-0" />
      )}

      <div className="flex-1 min-w-0">
        <span className={cn(
          'text-sm block truncate',
          isFired ? 'text-muted-foreground/70 line-through' : 'text-foreground'
        )}>
          {s.name || (s.schedule_type === 'cron' ? s.cron_expr : s.run_at ? new Date(s.run_at).toLocaleString() : t('list.onceLabel'))}
        </span>
        <span className="text-[11px] text-muted-foreground/70 font-mono block truncate">
          {s.schedule_type === 'cron' && s.cron_expr}
          {s.next_run_at && !isFired && (
            <>
              {s.schedule_type === 'cron' && ' · '}
              {t('list.nextRun', { time: new Date(s.next_run_at).toLocaleString() })}
            </>
          )}
          {isFired && t('list.fired')}
          {isOverdue && !isFired && (
            <span className="text-destructive ml-1">{t('list.overdue')}</span>
          )}
        </span>
      </div>

      {s.last_run_at && (
        <span className="text-[11px] text-muted-foreground/70 shrink-0">
          {t('list.lastRun', { time: new Date(s.last_run_at).toLocaleString() })}
        </span>
      )}

      <button
        onClick={(e) => { e.stopPropagation(); toggle.mutate({ scheduleId: s.id, enabled: !s.enabled }) }}
        className={cn(
          'relative inline-flex h-5 w-9 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors',
          s.enabled ? 'bg-info' : 'bg-muted'
        )}
      >
        <span className={cn(
          'pointer-events-none inline-block h-4 w-4 rounded-full bg-white shadow transform transition-transform',
          s.enabled ? 'translate-x-4' : 'translate-x-0'
        )} />
      </button>

      <button
        onClick={(e) => { e.stopPropagation(); trigger.mutate(s.id) }}
        className="p-1 text-muted-foreground hover:text-info shrink-0"
        title={t('list.triggerManually')}
      >
        <Play className="w-3 h-3" />
      </button>
      <button
        onClick={(e) => { e.stopPropagation(); onEdit() }}
        className="p-1 text-muted-foreground hover:text-info shrink-0"
        title={t('list.edit')}
      >
        <Pencil className="w-3 h-3" />
      </button>
      <button
        onClick={(e) => { e.stopPropagation(); handleDelete() }}
        className="p-1 text-muted-foreground/50 hover:text-destructive shrink-0"
        title={t('list.delete')}
      >
        <Trash2 className="w-3 h-3" />
      </button>
    </div>
  )
}
