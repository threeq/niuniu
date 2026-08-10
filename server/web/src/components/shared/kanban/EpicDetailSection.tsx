import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { Layers, Plus, ChevronRight, GitMerge } from 'lucide-react'
import { toast } from 'sonner'
import { api, epicApi } from '@/lib/api'
import type { Issue, EpicProgress } from '@/types/api'
import { Button } from '@/components/ui/button'
import { ExecStatusBadge } from './exec-status-badge'

interface EpicDetailSectionProps {
  epic: Issue
  /** Open another issue (a child) in the detail panel. */
  onOpenIssue?: (issueId: number) => void
  /** Open the create-issue dialog pre-set with this epic as parent + a wave. */
  onAddChild?: (parentId: number, wave: number) => void
}

// Child lifecycle/exec status -> token style. Mirrors the design system status
// chip pattern (color + text dual-encoding).
function childStatusStyle(status: string): string {
  switch (status) {
    case 'completed':
    case 'done':
      return 'bg-col-done/10 text-col-done border-col-done/30'
    case 'running':
    case 'implement':
    case 'implement-review':
      return 'bg-col-in-progress/10 text-col-in-progress border-col-in-progress/30'
    case 'failed':
      return 'bg-destructive/10 text-destructive border-destructive/30'
    case 'paused':
      return 'bg-warning/10 text-warning border-warning/30'
    default:
      return 'bg-warm-muted text-warm-text-muted border-warm-border'
  }
}

export function EpicDetailSection({ epic, onOpenIssue, onAddChild }: EpicDetailSectionProps) {
  const { t } = useTranslation('projects')
  const queryClient = useQueryClient()

  const { data: allIssues } = useQuery({
    queryKey: ['all-issues', epic.project_id],
    queryFn: () => api.get<Issue[]>(`/projects/${epic.project_id}/issues`),
    enabled: !!epic.project_id,
  })

  const { data: progress } = useQuery<EpicProgress>({
    queryKey: ['epic-progress', epic.id],
    queryFn: () => epicApi.epicProgress(epic.id),
    staleTime: 15_000,
  })

  const execStatus = progress?.exec_status ?? epic.exec_status ?? 'idle'
  const isReviewing = execStatus === 'reviewing'
  const isDone = execStatus === 'done'

  const invalidateExec = () => {
    queryClient.invalidateQueries({ queryKey: ['epic-progress', epic.id] })
    queryClient.invalidateQueries({ queryKey: ['issue', String(epic.id)] })
    queryClient.invalidateQueries({ queryKey: ['all-issues', epic.project_id] })
  }
  const onExecError = (err: unknown) => {
    toast.error(t('kanban.epic.executeFailed'), {
      description: err instanceof Error ? err.message : undefined,
    })
  }

  const mergeMutation = useMutation({
    mutationFn: () => epicApi.mergeToMain(epic.id),
    onSuccess: () => {
      invalidateExec()
      toast.success(t('kanban.epic.mergeToMainStarted'))
    },
    onError: onExecError,
  })
  const anyPending = mergeMutation.isPending

  // Group children by exec_wave, ascending.
  const wavesGrouped = useMemo(() => {
    const children = (allIssues ?? []).filter((i) => i.parent_issue_id === epic.id)
    const byWave = new Map<number, Issue[]>()
    for (const c of children) {
      const w = c.exec_wave ?? 0
      const arr = byWave.get(w) ?? []
      arr.push(c)
      byWave.set(w, arr)
    }
    return Array.from(byWave.entries())
      .sort((a, b) => a[0] - b[0])
      .map(([wave, items]) => ({
        wave,
        items: items.sort((a, b) => a.position - b.position),
      }))
  }, [allIssues, epic.id])

  // Next wave to add a child into: max existing wave (or 0).
  const nextWave = wavesGrouped.length > 0 ? wavesGrouped[wavesGrouped.length - 1].wave : 0

  return (
    <div className="border-b border-border px-5 py-4">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <Layers className="h-4 w-4 text-brand" aria-hidden="true" />
          <span className="text-sm font-medium text-warm-text">{t('kanban.epic.sectionTitle')}</span>
          {/* Status badge only once execution has actually started — 'idle' is noise. */}
          {execStatus !== 'idle' && <ExecStatusBadge status={execStatus} />}
        </div>
        {progress && progress.total > 0 && (
          <span className="text-xs tabular-nums text-warm-text-muted">
            {t('kanban.epic.progress', { done: progress.done, total: progress.total })}
          </span>
        )}
      </div>

      {/* Execution control: the epic is driven by its orchestration agent (created by
          making a workspace on the epic issue), which dispatches children itself — the
          mode-A execute/pause/resume controls were retired in stage 9. The only manual
          action left is the post-review merge to main; reviewing shows a hint. */}
      {(isReviewing || isDone) && (
        <div className="flex items-center gap-2 mb-4">
          {isReviewing ? (
            // Reviewing -> the review agent is verifying the feature branch. The
            // status badge above conveys progress.
            <span className="text-xs text-warm-text-muted">{t('kanban.epic.reviewingHint')}</span>
          ) : (
            // Done (review complete) -> let the human kick off the merge to main.
            // The agent performs the merge; this only sends the prompt.
            <Button size="sm" variant="outline" onClick={() => mergeMutation.mutate()} disabled={anyPending}>
              <GitMerge className="h-4 w-4 mr-1.5" aria-hidden="true" />
              {t('kanban.epic.mergeToMain')}
            </Button>
          )}
        </div>
      )}

      {/* Child list grouped by wave */}
      {wavesGrouped.length === 0 ? (
        <p className="text-xs text-warm-text-muted py-2">{t('kanban.epic.noChildren')}</p>
      ) : (
        <div className="space-y-3">
          {wavesGrouped.map(({ wave, items }) => (
            <div key={wave}>
              <div className="flex items-center justify-between mb-1.5">
                <span className="text-[11px] font-medium uppercase tracking-wide text-warm-text-muted">
                  {t('kanban.epic.wave', { wave })}
                </span>
                {onAddChild && (
                  <button
                    type="button"
                    onClick={() => onAddChild(epic.id, wave)}
                    className="inline-flex items-center gap-1 text-[11px] text-brand hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand rounded"
                  >
                    <Plus className="h-3 w-3" aria-hidden="true" />
                    {t('kanban.epic.addToWave')}
                  </button>
                )}
              </div>
              <ul className="space-y-1">
                {items.map((child) => (
                  <li key={child.id}>
                    <button
                      type="button"
                      onClick={() => onOpenIssue?.(child.id)}
                      className="w-full flex items-center gap-2 rounded-md border border-warm-border bg-warm-surface px-2 py-1.5 text-left hover:shadow-sm transition-shadow duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand"
                    >
                      <span className="text-[11px] text-warm-text-muted tabular-nums flex-shrink-0">
                        #{child.id}
                      </span>
                      <span className="flex-1 min-w-0 truncate text-sm text-warm-text">
                        {child.title}
                      </span>
                      <span
                        className={`inline-flex items-center rounded border px-1.5 py-0.5 text-[10px] font-medium flex-shrink-0 ${childStatusStyle(
                          child.exec_status && child.exec_status !== 'idle'
                            ? child.exec_status
                            : child.lifecycle_status ?? 'created',
                        )}`}
                      >
                        {child.exec_status && child.exec_status !== 'idle'
                          ? t(`kanban.epic.status.${child.exec_status}`)
                          : child.lifecycle_status ?? t('kanban.epic.status.idle')}
                      </span>
                      <ChevronRight className="h-3.5 w-3.5 text-warm-text-muted flex-shrink-0" aria-hidden="true" />
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      )}

      {/* Add a child into a brand-new wave */}
      {onAddChild && (
        <Button
          variant="ghost"
          size="sm"
          className="mt-3 text-brand"
          onClick={() => onAddChild(epic.id, wavesGrouped.length === 0 ? 0 : nextWave + 1)}
        >
          <Plus className="h-4 w-4 mr-1.5" aria-hidden="true" />
          {t('kanban.epic.addChild')}
        </Button>
      )}
    </div>
  )
}
