import { useState, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { Trash2, Pencil, Check, Play } from 'lucide-react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { Workspace, Issue } from '@/types/api'
import { toast } from 'sonner'
import { useChatInputBridge } from '@/stores/chat-input-bridge-store'
import { useColumns } from '@/lib/hooks/use-columns'
import { useIssueChecklists } from '@/lib/hooks/use-issue-checklists'
import { useIssueComments } from '@/lib/hooks/use-issue-comments'
import { useIssueTimeline } from '@/lib/hooks/use-issue-timeline'
import { IssueProperties } from '@/components/issue/issue-properties'
import { IssueDescription } from '@/components/issue/issue-description'
import { IssueGoalConditionPanel } from '@/components/issue/issue-goal-condition-panel'
import { IssueChecklist } from '@/components/issue/issue-checklist'
import { IssueTimeline } from '@/components/issue/issue-timeline'
import { IssueCommentInput } from '@/components/issue/issue-comment-input'
import { ExecutionTimeline } from '@/components/issue/execution-timeline'
import { EpicHierarchyControl } from '@/components/shared/kanban/EpicHierarchyControl'
import { EpicDetailSection } from '@/components/shared/kanban/EpicDetailSection'
import { IssueQuickCreateDialog } from '@/components/shared/kanban/issue-quick-create-dialog'
import { isIMEComposing } from '@/lib/ime'
import { confirm } from '@/lib/confirm'

const lifecycleColors: Record<string, string> = {
  created: 'bg-muted-foreground/60',
  spec: 'bg-blue-600 dark:bg-blue-700',
  'spec-review': 'bg-blue-500 dark:bg-blue-600',
  plan: 'bg-indigo-600 dark:bg-indigo-700',
  'plan-review': 'bg-indigo-500 dark:bg-indigo-600',
  implement: 'bg-amber-600 dark:bg-amber-700',
  'implement-review': 'bg-orange-600 dark:bg-orange-700',
  test: 'bg-purple-600 dark:bg-purple-700',
  completed: 'bg-success',
}

interface IssuePanelProps {
  workspace: Workspace
  readOnly?: boolean
}

export function IssuePanel({ workspace, readOnly }: IssuePanelProps) {
  const { t } = useTranslation('workspaces')
  const issueId = workspace.issue_id
  const queryClient = useQueryClient()
  const numericId = issueId ? Number(issueId) : 0
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState('')
  const [titleCopied, setTitleCopied] = useState(false)
  // #623 review: sub-issue (child) create dialog, aligned with the kanban panel.
  const [childParent, setChildParent] = useState<{ parentId: number; wave: number } | null>(null)

  const { data: issue, isLoading } = useQuery({
    queryKey: ['issue', issueId],
    queryFn: () => api.get<Issue>(`/issues/${issueId}`),
    enabled: !!issueId,
    retry: 1,
  })

  const { columns } = useColumns(issue?.project_id ?? null)
  const requestChatInput = useChatInputBridge((s) => s.request)

  // Compose the issue title + description as markdown and drop it into the
  // workspace chat input (without auto-sending) so the agent can be kicked off
  // from the issue's own words.
  const handleRun = () => {
    if (!issue) return
    const desc = (issue.description ?? '').trim()
    const md = desc ? `# ${issue.title}\n\n${desc}` : `# ${issue.title}`
    requestChatInput(String(workspace.id), md, false)
  }

  const { checklists, createChecklist, updateChecklist, deleteChecklist } = useIssueChecklists(numericId)
  const { createComment, updateComment, deleteComment, isCreating: isCommentCreating } = useIssueComments(numericId)
  const { timeline } = useIssueTimeline(numericId)

  const moveColumnMutation = useMutation({
    mutationFn: async (targetColumnId: number) => {
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
    if (readOnly || !issue || issue.column_id === targetColumnId) return
    moveColumnMutation.mutate(targetColumnId)
  }, [readOnly, issue, moveColumnMutation])

  // Review 闭环 (#623): keep the workspace issue panel aligned with the kanban issue
  // panel — the comment composer offers the same "需重做/打回" action when the issue
  // sits in a review lane (审查/人工审查) or the done lane (完成, = re-open for review).
  const currentCol = columns?.find((c) => c.id === issue?.column_id)
  const currentLifecycle = (currentCol?.lifecycle_mapping ?? '').toLowerCase()
  const isReviewCol = !!currentCol && (currentCol.name.includes('审查') || currentLifecycle.includes('review'))
  const isDoneCol = !!currentCol && (currentCol.name.includes('完成') || currentLifecycle.includes('complete'))
  const canRequestChanges = !readOnly && (isReviewCol || isDoneCol)
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
      // error handled by apiFetch
    }
  }, [issue, issueId, numericId, queryClient])

  const handleDelete = async () => {
    if (!(await confirm(t('panels.issue.confirmDelete')))) return
    try {
      await api.delete(`/issues/${issueId}`)
      queryClient.invalidateQueries({ queryKey: ['issues'] })
      queryClient.invalidateQueries({ queryKey: ['all-issues'] })
    } catch {
      // error handled by apiFetch
    }
  }

  const handleTitleSave = () => {
    if (titleDraft.trim() && titleDraft !== issue?.title) {
      handleUpdate({ title: titleDraft.trim() })
    }
    setEditingTitle(false)
  }

  const handleTitleCopy = () => {
    if (!issue) return
    navigator.clipboard.writeText(issue.title).catch(() => {})
    setTitleCopied(true)
    setTimeout(() => setTitleCopied(false), 1500)
  }

  if (!issueId) {
    return (
      <div className="flex flex-col h-full bg-background">
        <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
          {t('panels.issue.noIssue')}
        </div>
      </div>
    )
  }

  if (isLoading) {
    return (
      <div className="flex flex-col h-full bg-background">
        <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
          {t('panels.issue.loading')}
        </div>
      </div>
    )
  }

  if (!issue) {
    return (
      <div className="flex flex-col h-full bg-background">
        <div className="flex items-center justify-center py-8 text-xs text-muted-foreground">
          {t('panels.issue.notFound')}
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full bg-background overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-5 py-3 border-b border-border flex-shrink-0">
        <div className="flex items-center gap-2">
          <span
            className="text-xs text-muted-foreground cursor-pointer hover:text-foreground"
            onClick={() => navigator.clipboard.writeText(`#${issue.id}`).catch(() => {})}
            title={t('panels.issue.copyIdTitle')}
          >
            #{issue.id}
          </span>
          <span className={`text-[10px] text-white px-2 py-0.5 rounded ${lifecycleColors[issue.lifecycle_status ?? 'created'] ?? 'bg-muted-foreground/60'}`}>
            {issue.lifecycle_status}
          </span>
        </div>
        {!readOnly && (
          <div className="flex items-center gap-1.5">
            <button
              className="inline-flex items-center gap-1 rounded border border-brand/30 bg-brand-soft px-2 py-1 text-xs font-medium text-brand hover:bg-brand/10 transition-colors duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand"
              onClick={handleRun}
              title={t('panels.issue.runTitle')}
            >
              <Play className="w-3.5 h-3.5" aria-hidden="true" />
              {t('panels.issue.run')}
            </button>
            <button className="text-muted-foreground hover:text-destructive" onClick={handleDelete} title={t('panels.issue.confirmDelete')}>
              <Trash2 className="w-4 h-4" />
            </button>
          </div>
        )}
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
            <div className="flex items-center gap-2 group">
              <h2
                className="text-lg font-semibold cursor-pointer hover:text-primary flex-1"
                onClick={handleTitleCopy}
                title={t('panels.issue.copyTitleTitle')}
              >
                {issue.title}
              </h2>
              {titleCopied && <Check className="w-3.5 h-3.5 text-success flex-shrink-0" />}
              {!readOnly && (
                <button
                  className="text-muted-foreground hover:text-foreground flex-shrink-0 opacity-0 group-hover:opacity-100 transition-opacity"
                  onClick={() => { setTitleDraft(issue.title); setEditingTitle(true) }}
                  title={t('panels.issue.editTitleTitle')}
                >
                  <Pencil className="w-3.5 h-3.5" />
                </button>
              )}
            </div>
          )}
        </div>

        <IssueProperties issue={issue} columns={columns} onUpdate={handleUpdate} onMoveColumn={handleMoveColumn} isMovingColumn={moveColumnMutation.isPending} readOnly={readOnly} />
        {/* #623: sub-issue hierarchy (parent chip / add subtask), aligned with kanban */}
        <EpicHierarchyControl
          issue={issue}
          onAddChild={readOnly ? undefined : (parentId, wave) => setChildParent({ parentId, wave })}
        />
        {/* #623: child list grouped by wave (epic only) */}
        {issue.issue_type === 'epic' && (
          <EpicDetailSection
            epic={issue}
            onAddChild={readOnly ? undefined : (parentId, wave) => setChildParent({ parentId, wave })}
          />
        )}
        <IssueDescription description={issue.description ?? ''} onSave={(desc) => handleUpdate({ description: desc })} readOnly={readOnly} />
        {!readOnly && (
          <IssueGoalConditionPanel
            issueId={issue.id}
            initialCondition={issue.goal_condition ?? ''}
            onSave={async (next) => { await handleUpdate({ goal_condition: next }) }}
          />
        )}
        <IssueChecklist checklists={checklists} onCreate={createChecklist} onUpdate={updateChecklist} onDelete={deleteChecklist} readOnly={readOnly} />
        {issue.exec_status && issue.exec_status !== 'idle' && (
          <ExecutionTimeline issueId={issue.id} />
        )}
        <IssueTimeline
          timeline={timeline}
          onUpdateComment={updateComment}
          onDeleteComment={deleteComment}
          readOnly={readOnly}
        />
      </div>

      {/* Always-visible comment composer footer (#623 UX): add a comment or 需重做/打回
          without scrolling to the bottom of a long panel. */}
      {!readOnly && (
        <div className="flex-shrink-0 border-t border-border px-5 py-2 bg-background">
          <IssueCommentInput
            onSubmit={createComment}
            isSubmitting={isCommentCreating}
            onRequestChanges={canRequestChanges ? requestChangesMutation.mutate : undefined}
            requestChangesLabel={t(isDoneCol ? 'issues:requestChanges.reopen.submit' : 'issues:requestChanges.submit')}
            isRequestingChanges={requestChangesMutation.isPending}
          />
        </div>
      )}

      {/* #623: create-subtask dialog (fixed parent + wave), aligned with kanban */}
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
