import { useState, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { X, Trash2 } from 'lucide-react'
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { Issue, Column } from '@/types/api'
import { useIssueChecklists } from '@/lib/hooks/use-issue-checklists'
import { useIssueComments } from '@/lib/hooks/use-issue-comments'
import { useIssueTimeline } from '@/lib/hooks/use-issue-timeline'
import { IssueProperties } from '@/components/issue/issue-properties'
import { IssueDescription } from '@/components/issue/issue-description'
import { IssueGoalConditionPanel } from '@/components/issue/issue-goal-condition-panel'
import { IssueChecklist } from '@/components/issue/issue-checklist'
import { IssueTimeline } from '@/components/issue/issue-timeline'
import { IssueCommentInput } from '@/components/issue/issue-comment-input'
import { toast } from 'sonner'
import { ExecutionTimeline } from '@/components/issue/execution-timeline'
import { IssueWorkspace } from '@/components/issue/issue-workspace'
import { ExternalIssueHeader } from './ExternalIssueHeader'
import { ExternalCommentsSnapshot } from './ExternalCommentsSnapshot'
import { EpicHierarchyControl } from './EpicHierarchyControl'
import { EpicDetailSection } from './EpicDetailSection'
import { IssueQuickCreateDialog } from './issue-quick-create-dialog'
import { isIMEComposing } from '@/lib/ime'
import { confirm } from '@/lib/confirm'

const lifecycleColors: Record<string, string> = {
  created: 'bg-muted-foreground',
  spec: 'bg-info',
  'spec-review': 'bg-info',
  plan: 'bg-indigo-600',
  'plan-review': 'bg-indigo-500',
  implement: 'bg-amber-600',
  'implement-review': 'bg-orange-600',
  test: 'bg-purple-600',
  completed: 'bg-success',
}

interface KanbanIssueDetailPanelProps {
  issueId: string
  columns?: Column[]
  onClose: () => void
  /** Executable Epic: open another issue (parent epic or child) in the panel. */
  onOpenIssue?: (issueId: number) => void
}

export function KanbanIssueDetailPanel({ issueId, columns, onClose, onOpenIssue }: KanbanIssueDetailPanelProps) {
  const { t } = useTranslation('projects')
  const queryClient = useQueryClient()
  const numericId = Number(issueId)
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState('')
  // Create-child dialog state (Executable Epic). When set, the create-issue
  // dialog opens pre-set with this epic as parent + the chosen wave.
  const [childParent, setChildParent] = useState<{ parentId: number; wave: number } | null>(null)

  const { data: issue, isLoading } = useQuery({
    queryKey: ['issue', issueId],
    queryFn: () => api.get<Issue>(`/issues/${issueId}`),
    retry: 1,
  })

  const { checklists, createChecklist, updateChecklist, deleteChecklist } = useIssueChecklists(numericId)
  const { createComment, updateComment, deleteComment, isCreating: isCommentCreating } = useIssueComments(numericId)
  const { timeline } = useIssueTimeline(numericId)

  const handleUpdate = useCallback(async (data: Partial<Issue>) => {
    if (!issue) return
    try {
      await api.put<Issue>(`/issues/${issueId}`, {
        title: issue.title,
        description: issue.description ?? '',
        priority: issue.priority ?? 0,
        start_date: issue.start_date ?? '',
        due_date: issue.due_date ?? '',
        estimate_type: issue.estimate_type ?? '',
        estimate: issue.estimate ?? 0,
        actual_time: issue.actual_time ?? 0,
        ...data,
      })
      queryClient.invalidateQueries({ queryKey: ['issue', issueId] })
      queryClient.invalidateQueries({ queryKey: ['issues'] })
      queryClient.invalidateQueries({ queryKey: ['all-issues'] })
      queryClient.invalidateQueries({ queryKey: ['issue-timeline', numericId] })
    } catch {
      // error toast handled by apiFetch
    }
  }, [issue, issueId, numericId, queryClient])

  const moveColumnMutation = useMutation({
    mutationFn: async (targetColumnId: number) => {
      // Fetch target column issues to get accurate append position
      // (cache may be empty if column was never scrolled into view)
      let targetIssues = queryClient.getQueryData<Issue[]>(['issues', targetColumnId])
      if (!targetIssues) {
        targetIssues = await api.get<Issue[]>(`/columns/${targetColumnId}/issues`)
      }
      const position = targetIssues?.length ?? 0
      return api.put(`/issues/${issueId}/move`, { column_id: targetColumnId, position })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['issue', issueId] })
      queryClient.invalidateQueries({ queryKey: ['issues'] })
      queryClient.invalidateQueries({ queryKey: ['all-issues'] })
      queryClient.invalidateQueries({ queryKey: ['issue-timeline', numericId] })
    },
  })

  const handleMoveColumn = useCallback((targetColumnId: number) => {
    if (!issue || issue.column_id === targetColumnId) return
    moveColumnMutation.mutate(targetColumnId)
  }, [issue, moveColumnMutation])

  // Review 闭环 (#623): "需重做/打回" is folded into the comment composer — same text
  // box, but this path posts the feedback AND bounces the card back to the implement
  // lane (injecting the two-layer review context). Available while the issue is in a
  // review lane (审查/人工审查) or the done lane (完成, = re-open for re-review).
  const currentCol = columns?.find((c) => c.id === issue?.column_id)
  const currentLifecycle = (currentCol?.lifecycle_mapping ?? '').toLowerCase()
  const isReviewCol = !!currentCol && (currentCol.name.includes('审查') || currentLifecycle.includes('review'))
  const isDoneCol = !!currentCol && (currentCol.name.includes('完成') || currentLifecycle.includes('complete'))
  const canRequestChanges = isReviewCol || isDoneCol
  const requestChangesMutation = useMutation({
    mutationFn: (data: { author: string; content: string }) =>
      api.requestChanges(numericId, { comment: data.content, author: data.author }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['issue', issueId] })
      queryClient.invalidateQueries({ queryKey: ['issues'] })
      queryClient.invalidateQueries({ queryKey: ['all-issues'] })
      queryClient.invalidateQueries({ queryKey: ['issue-comments', issueId] })
      queryClient.invalidateQueries({ queryKey: ['issue-timeline', numericId] })
      toast.success(t(isDoneCol ? 'issues:requestChanges.reopen.done' : 'issues:requestChanges.done'))
    },
    onError: () => toast.error(t(isDoneCol ? 'issues:requestChanges.reopen.failed' : 'issues:requestChanges.failed')),
  })

  const handleDelete = async () => {
    if (!(await confirm(t('issue.actions.deleteConfirm')))) return
    try {
      await api.delete(`/issues/${issueId}`)
      queryClient.invalidateQueries({ queryKey: ['issues'] })
      queryClient.invalidateQueries({ queryKey: ['all-issues'] })
      onClose()
    } catch {
      // error toast handled by apiFetch
    }
  }

  const handleTitleSave = () => {
    if (titleDraft.trim() && titleDraft !== issue?.title) {
      handleUpdate({ title: titleDraft.trim() })
    }
    setEditingTitle(false)
  }

  if (isLoading) {
    return (
      <div className="w-[480px] border-l border-border bg-background flex items-center justify-center">
        <span className="text-muted-foreground text-sm">{t('issue.loading')}</span>
      </div>
    )
  }

  if (!issue) return null

  return (
    <div className="w-[480px] border-l border-border bg-background flex flex-col h-full overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-5 py-3 border-b border-border flex-shrink-0">
        <div className="flex items-center gap-2">
          <span
            className="text-xs text-muted-foreground cursor-pointer hover:text-foreground"
            onClick={() => navigator.clipboard.writeText(`#${issue.id}`).catch(() => {})}
            title={t('issue.copyId')}
          >
            #{issue.id}
          </span>
          <span className={`text-[10px] text-white px-2 py-0.5 rounded ${lifecycleColors[issue.lifecycle_status ?? 'created'] ?? 'bg-muted-foreground'}`}>
            {issue.lifecycle_status}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <button className="text-muted-foreground hover:text-destructive" onClick={handleDelete}>
            <Trash2 className="w-4 h-4" />
          </button>
          <button className="text-muted-foreground hover:text-foreground" onClick={onClose}>
            <X className="w-4 h-4" />
          </button>
        </div>
      </div>

      {/* Scrollable content */}
      <div className="flex-1 overflow-y-auto">
        {/* Title */}
        <div className="px-5 pt-3 pb-2">
          {editingTitle ? (
            <input
              className="w-full text-lg font-semibold bg-background border border-border rounded px-2 py-1"
              value={titleDraft}
              onChange={(e) => setTitleDraft(e.target.value)}
              onBlur={handleTitleSave}
              onKeyDown={(e) => { if (isIMEComposing(e)) return; if (e.key === 'Enter') handleTitleSave() }}
              autoFocus
            />
          ) : (
            <h2
              className="text-lg font-semibold cursor-pointer hover:text-primary"
              onClick={() => { setTitleDraft(issue.title); setEditingTitle(true) }}
            >
              {issue.title}
            </h2>
          )}
        </div>

        {issue.external_source && issue.external_source !== '' && (
          <ExternalIssueHeader
            externalSource={issue.external_source}
            externalId={issue.external_id ?? ''}
            externalUrl={issue.external_url ?? ''}
            externalSnapshotAt={issue.external_snapshot_at ?? null}
          />
        )}

        <IssueProperties issue={issue} columns={columns} onUpdate={handleUpdate} onMoveColumn={handleMoveColumn} isMovingColumn={moveColumnMutation.isPending} />

        {/* Executable Epic: parent-Epic chip / mark-as-Epic / wave control */}
        <EpicHierarchyControl
          issue={issue}
          onOpenIssue={onOpenIssue}
          onAddChild={(parentId, wave) => setChildParent({ parentId, wave })}
        />

        {/* Executable Epic: child list + execution control (epic only) */}
        {issue.issue_type === 'epic' && (
          <EpicDetailSection
            epic={issue}
            onOpenIssue={onOpenIssue}
            onAddChild={(parentId, wave) => setChildParent({ parentId, wave })}
          />
        )}

        <IssueWorkspace issueId={numericId} issueTitle={issue.title} />

        <IssueDescription description={issue.description ?? ''} onSave={(desc) => handleUpdate({ description: desc })} />
        <IssueGoalConditionPanel
          issueId={issue.id}
          initialCondition={issue.goal_condition ?? ''}
          onSave={async (next) => { await handleUpdate({ goal_condition: next }) }}
        />
        <IssueChecklist checklists={checklists} onCreate={createChecklist} onUpdate={updateChecklist} onDelete={deleteChecklist} />
        {issue.external_source && issue.external_source !== '' && (
          <ExternalCommentsSnapshot
            source={issue.external_source as 'github' | 'tapd' | ''}
            externalUrl={issue.external_url ?? ''}
            snapshot={issue.external_comments_snapshot ?? []}
            snapshotAt={issue.external_comments_snapshot_at ?? null}
          />
        )}
        {issue.exec_status && issue.exec_status !== 'idle' && (
          <ExecutionTimeline issueId={issue.id} />
        )}
        <IssueTimeline
          timeline={timeline}
          onUpdateComment={updateComment}
          onDeleteComment={deleteComment}
          isExternal={!!issue.external_source && issue.external_source !== ''}
        />
      </div>

      {/* Always-visible comment composer footer (#623 UX): add a comment or 需重做/打回
          without scrolling to the bottom of a long panel. */}
      <div className="flex-shrink-0 border-t border-border px-5 py-2 bg-background">
        <IssueCommentInput
          onSubmit={createComment}
          isSubmitting={isCommentCreating}
          onRequestChanges={canRequestChanges ? requestChangesMutation.mutate : undefined}
          requestChangesLabel={t(isDoneCol ? 'issues:requestChanges.reopen.submit' : 'issues:requestChanges.submit')}
          isRequestingChanges={requestChangesMutation.isPending}
        />
      </div>

      {/* Create-child dialog (Executable Epic) — lightweight quick-create in
          subtask mode: fixed parent + wave, no workspace checkbox (workspaces
          are auto-created by the Epic orchestrator, so a manual one here would
          conflict with the auto-orchestration). */}
      {childParent && (
        <IssueQuickCreateDialog
          open={!!childParent}
          onOpenChange={(open) => { if (!open) setChildParent(null) }}
          projectId={issue.project_id}
          columnId={issue.column_id}
          parentIssueId={childParent.parentId}
          defaultWave={childParent.wave}
        />
      )}
    </div>
  )
}
