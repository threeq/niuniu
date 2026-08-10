import { useTranslation } from 'react-i18next'
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { Layers, ArrowUpRight, Plus } from 'lucide-react'
import { toast } from 'sonner'
import { api, epicApi } from '@/lib/api'
import type { Issue } from '@/types/api'
import { ParentIssueSelect } from './parent-issue-select'

interface EpicHierarchyControlProps {
  issue: Issue
  /** Open another issue (the parent epic) in the detail panel. */
  onOpenIssue?: (issueId: number) => void
  /** Open the create-issue dialog pre-set with this issue as parent + a wave. */
  onAddChild?: (parentId: number, wave: number) => void
}

// Token-styled control for the Epic hierarchy (max two levels). An issue's type
// is NOT chosen here — it auto-becomes an Epic once it has children. The two
// hierarchy actions ("add subtask" / "specify parent") appear ONLY while the
// issue is free (no parent, no children); once it has either, the relevant
// chip/section is shown instead and both actions disappear.
export function EpicHierarchyControl({ issue, onOpenIssue, onAddChild }: EpicHierarchyControlProps) {
  const { t } = useTranslation('projects')
  const queryClient = useQueryClient()

  const parentId = issue.parent_issue_id ?? null
  const isEpic = issue.issue_type === 'epic'
  // Free issue: neither a child nor a parent -> may go either way (2-level cap).
  const showHierarchyActions = parentId === null && !isEpic

  const { data: allIssues } = useQuery({
    queryKey: ['all-issues', issue.project_id],
    queryFn: () => api.get<Issue[]>(`/projects/${issue.project_id}/issues`),
    // Needed for the parent chip (child) or the parent picker (free issue).
    enabled: !!issue.project_id && (parentId !== null || showHierarchyActions),
  })
  const parent = (allIssues ?? []).find((i) => i.id === parentId)

  // Eligible parents: same-project, not self, and top-level (so the result stays
  // within two levels). The server re-validates these constraints.
  const eligibleParents = (allIssues ?? []).filter(
    (i) => i.id !== issue.id && (i.parent_issue_id ?? null) === null,
  )

  const mutation = useMutation({
    mutationFn: (body: {
      parent_issue_id: number | null
      issue_type: string
      exec_wave: number
      exec_status: string
    }) => epicApi.setExecFields(issue.id, body),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['issue', String(issue.id)] })
      queryClient.invalidateQueries({ queryKey: ['issue', issue.id] })
      queryClient.invalidateQueries({ queryKey: ['all-issues', issue.project_id] })
      queryClient.invalidateQueries({ queryKey: ['epic-progress', issue.id] })
    },
    onError: (err) => {
      toast.error(t('kanban.epic.execFieldsFailed'), {
        description: err instanceof Error ? err.message : undefined,
      })
    },
  })

  const setWave = (wave: number) => {
    mutation.mutate({
      parent_issue_id: parentId,
      issue_type: issue.issue_type ?? 'task',
      exec_wave: wave,
      exec_status: issue.exec_status ?? 'idle',
    })
  }

  const setParent = (pid: number) => {
    mutation.mutate({
      parent_issue_id: pid,
      issue_type: issue.issue_type ?? 'task',
      exec_wave: issue.exec_wave ?? 0,
      exec_status: issue.exec_status ?? 'idle',
    })
  }

  // Nothing to render for a top-level Epic — the EpicDetailSection owns its UI.
  if (parentId === null && !showHierarchyActions) {
    return null
  }

  return (
    <div className="border-b border-border px-5 py-3 space-y-3">
      {/* Parent-Epic chip (children only) */}
      {parentId !== null && (
        <div className="flex items-center gap-2">
          <span className="text-muted-foreground text-xs">{t('kanban.epic.parentLabel')}</span>
          <button
            type="button"
            onClick={() => onOpenIssue?.(parentId)}
            className="inline-flex items-center gap-1 rounded border border-brand/30 bg-brand-soft px-1.5 py-0.5 text-xs font-medium text-brand hover:bg-brand/10 transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand"
          >
            <Layers className="h-3 w-3" aria-hidden="true" />
            <span className="max-w-48 truncate">
              {parent ? parent.title : `#${parentId}`}
            </span>
            <ArrowUpRight className="h-3 w-3" aria-hidden="true" />
          </button>
        </div>
      )}

      {/* Wave control (children only) */}
      {parentId !== null && (
        <div className="grid grid-cols-[90px_1fr] gap-x-3 items-center text-sm">
          <span className="text-muted-foreground text-xs">{t('kanban.epic.waveLabel')}</span>
          <input
            type="number"
            min={0}
            value={issue.exec_wave ?? 0}
            onChange={(e) => setWave(Math.max(0, parseInt(e.target.value, 10) || 0))}
            disabled={mutation.isPending}
            className="bg-background border border-border rounded px-2 py-1 text-xs w-16 tabular-nums focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
          />
        </div>
      )}

      {/* Free issue: choose to add a subtask (becomes an Epic) OR specify a
          parent (becomes a child). Selecting either hides both on next render. */}
      {showHierarchyActions && (
        <div className="flex flex-wrap items-center gap-3">
          <button
            type="button"
            onClick={() => onAddChild?.(issue.id, 0)}
            disabled={mutation.isPending}
            className="inline-flex items-center gap-1 text-xs font-medium text-brand hover:text-brand/80 transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand rounded disabled:opacity-50"
          >
            <Plus className="h-3.5 w-3.5" aria-hidden="true" />
            {t('kanban.epic.addSubtask')}
          </button>
          {eligibleParents.length > 0 && (
            <ParentIssueSelect
              ariaLabel={t('kanban.epic.specifyParent')}
              issues={eligibleParents}
              value={null}
              onChange={(pid) => {
                if (pid != null) setParent(pid)
              }}
              allowNone={false}
              noneLabel={t('kanban.epic.specifyParent')}
              disabled={mutation.isPending}
              triggerClassName="h-8 w-48 text-xs"
            />
          )}
        </div>
      )}
    </div>
  )
}
