import { useState, useEffect, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import type { Issue } from '@/types/api'
import { ParentIssueSelect } from './parent-issue-select'
import {
  loadIssueDraft,
  saveIssueDraft,
  clearIssueDraft,
  type IssueDraft,
  type IssuePriority,
} from '@/lib/issue-draft'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  projectId: number
  columnId: number
  /** Project issues, used to populate the optional parent picker. Unused in
   *  subtask mode (parent is fixed via `parentIssueId`). */
  issues?: Issue[]
  /** Executable Epic subtask mode: when set, the created issue becomes a child
   *  of this Epic (fixed parent — no picker — plus `exec_wave`). */
  parentIssueId?: number
  /** Executable Epic subtask mode: wave the new child is added into. */
  defaultWave?: number
  /** Called after a successful create with the new issue id (e.g. open detail panel). */
  onCreated?: (issueId: number) => void
}

const PRIORITY_VALUE: Record<IssuePriority, number> = { low: 0, medium: 1, high: 2, critical: 3 }
const EMPTY: IssueDraft = { title: '', description: '', priority: 'medium', parentIssueId: null }

export function IssueQuickCreateDialog({ open, onOpenChange, projectId, columnId, issues = [], parentIssueId, defaultWave, onCreated }: Props) {
  const { t } = useTranslation('projects')
  const qc = useQueryClient()
  const [draft, setDraft] = useState<IssueDraft>(EMPTY)
  const [submitting, setSubmitting] = useState(false)

  // Epic subtask mode: parent is fixed, so the parent picker is hidden and
  // localStorage drafts are skipped (they key on project+column and would
  // collide with the general quick-create flow on the Epic's column).
  const isSubtask = parentIssueId != null

  // On open: restore any involuntarily-kept draft for this project+column.
  useEffect(() => {
    if (open) {
      setDraft(isSubtask ? EMPTY : (loadIssueDraft(projectId, columnId) ?? EMPTY))
      setSubmitting(false)
    }
  }, [open, projectId, columnId, isSubtask])

  // Mirror the form into localStorage as the user types (empty draft is not stored).
  useEffect(() => {
    if (open && !isSubtask) saveIssueDraft(projectId, columnId, draft)
  }, [open, projectId, columnId, draft, isSubtask])

  const finish = useCallback((openState: boolean) => {
    if (!isSubtask) clearIssueDraft(projectId, columnId)
    onOpenChange(openState)
  }, [projectId, columnId, onOpenChange, isSubtask])

  const handleCancel = useCallback(() => finish(false), [finish])

  const handleSave = useCallback(async () => {
    if (!draft.title.trim() || submitting) return
    setSubmitting(true)
    try {
      const parent = isSubtask ? parentIssueId : (draft.parentIssueId ?? undefined)
      const created = await api.post<{ id: number }>(`/columns/${columnId}/issues`, {
        title: draft.title.trim(),
        description: draft.description.trim() || undefined,
        priority: PRIORITY_VALUE[draft.priority],
        parent_issue_id: parent,
        issue_type: parent != null ? 'task' : undefined,
        exec_wave: isSubtask ? (defaultWave ?? 0) : undefined,
      })
      qc.invalidateQueries({ queryKey: ['issues'] })
      qc.invalidateQueries({ queryKey: ['all-issues', projectId] })
      if (isSubtask) qc.invalidateQueries({ queryKey: ['epic-progress', parentIssueId] })
      if (!isSubtask) clearIssueDraft(projectId, columnId)
      onOpenChange(false)
      onCreated?.(created.id)
    } catch (e) {
      console.error('Failed to create issue:', e)
      setSubmitting(false)
    }
  }, [draft, submitting, columnId, projectId, isSubtask, parentIssueId, defaultWave, qc, onOpenChange, onCreated])

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) handleCancel() }}>
      <DialogContent className="sm:max-w-[480px]">
        <DialogHeader>
          <DialogTitle>{isSubtask ? t('kanban.epic.addSubtask') : t('createIssue.title')}</DialogTitle>
          <DialogDescription>{t('createIssue.description')}</DialogDescription>
        </DialogHeader>

        <div className="grid gap-4 py-2">
          <div className="grid gap-2">
            <label htmlFor="qc-title" className="text-sm font-medium">
              {t('createIssue.fields.titleLabel')} <span className="text-destructive">*</span>
            </label>
            <Input
              id="qc-title"
              data-testid="quick-create-title"
              value={draft.title}
              onChange={(e) => setDraft((d) => ({ ...d, title: e.target.value }))}
              placeholder={t('createIssue.placeholders.title')}
              disabled={submitting}
              autoFocus
            />
          </div>

          <div className="grid gap-2">
            <label htmlFor="qc-desc" className="text-sm font-medium">
              {t('createIssue.fields.descriptionLabel')}
            </label>
            <textarea
              id="qc-desc"
              data-testid="quick-create-description"
              value={draft.description}
              onChange={(e) => setDraft((d) => ({ ...d, description: e.target.value }))}
              placeholder={t('createIssue.placeholders.description')}
              disabled={submitting}
              rows={3}
              className="flex min-h-[60px] w-full rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
            />
          </div>

          <div className={isSubtask ? 'grid gap-2' : 'grid grid-cols-2 gap-3'}>
            <div className="grid gap-2">
              <label htmlFor="qc-priority" className="text-sm font-medium">
                {t('createIssue.fields.priorityLabel')}
              </label>
              <select
                id="qc-priority"
                data-testid="quick-create-priority"
                value={draft.priority}
                onChange={(e) => setDraft((d) => ({ ...d, priority: e.target.value as IssuePriority }))}
                disabled={submitting}
                className="flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
              >
                <option value="low">{t('createIssue.priority.low')}</option>
                <option value="medium">{t('createIssue.priority.medium')}</option>
                <option value="high">{t('createIssue.priority.high')}</option>
                <option value="critical">{t('createIssue.priority.critical')}</option>
              </select>
            </div>
            {/* Parent picker — general mode only. In Epic subtask mode the parent
                is fixed (passed via `parentIssueId`), so the picker is hidden. */}
            {!isSubtask && (
              <div className="grid gap-2">
                <label htmlFor="qc-parent" className="text-sm font-medium">
                  {t('createIssue.fields.parentLabel')}
                </label>
                <ParentIssueSelect
                  id="qc-parent"
                  data-testid="quick-create-parent"
                  issues={issues}
                  value={draft.parentIssueId ?? null}
                  onChange={(next) => setDraft((d) => ({ ...d, parentIssueId: next }))}
                  disabled={submitting}
                  noneLabel={t('createIssue.parentNone')}
                />
              </div>
            )}
          </div>
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" data-testid="quick-create-cancel" onClick={handleCancel} disabled={submitting}>
            {t('createIssue.actions.cancel')}
          </Button>
          <Button type="button" data-testid="quick-create-save" onClick={handleSave} disabled={!draft.title.trim() || submitting}>
            {submitting ? t('createIssue.actions.creating') : t('createIssue.actions.create')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
