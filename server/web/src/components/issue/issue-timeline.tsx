import ReactMarkdown from 'react-markdown'
import { remarkGfmSafariSafe } from '@/lib/remark-gfm-safari-safe'
import { rehypeLinkify } from '@/lib/rehype-linkify'
import { Pencil, Trash2 } from 'lucide-react'

const REMARK_PLUGINS = [remarkGfmSafariSafe()]
const REHYPE_PLUGINS = [rehypeLinkify()]
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TimelineEntry } from '@/types/api'

interface IssueTimelineProps {
  timeline: TimelineEntry[]
  onUpdateComment: (data: { id: number; content: string }) => void
  onDeleteComment: (id: number) => void
  readOnly?: boolean
  /** True iff the parent issue is bound to an external tracker. Toggles
   *  the "comment edit won't sync" hint while editing external-issue comments. */
  isExternal?: boolean
}

// NOTE: the comment composer (create + 需重做/打回) is NOT rendered here — it lives
// in an always-visible panel footer (see KanbanIssueDetailPanel / workspace IssuePanel)
// so it never requires scrolling to the bottom of a long panel (#623 UX review).
export function IssueTimeline({ timeline, onUpdateComment, onDeleteComment, readOnly, isExternal }: IssueTimelineProps) {
  const { t } = useTranslation('projects')
  const [editingCommentId, setEditingCommentId] = useState<number | null>(null)
  const [editContent, setEditContent] = useState('')

  const fieldLabels: Record<string, string> = {
    title: t('issue.timeline.fields.title'),
    description: t('issue.timeline.fields.description'),
    priority: t('issue.timeline.fields.priority'),
    labels: t('issue.timeline.fields.labels'),
    assignee: t('issue.timeline.fields.assignee'),
    start_date: t('issue.timeline.fields.startDate'),
    due_date: t('issue.timeline.fields.dueDate'),
    estimate_type: t('issue.timeline.fields.estimateType'),
    estimate: t('issue.timeline.fields.estimate'),
    actual_time: t('issue.timeline.fields.actualTime'),
    column: t('issue.timeline.fields.column'),
    lifecycle_status: t('issue.timeline.fields.lifecycleStatus'),
  }

  const priorityLabels: Record<string, string> = {
    '0': t('issue.timeline.priority.0'),
    '1': t('issue.timeline.priority.1'),
    '2': t('issue.timeline.priority.2'),
    '3': t('issue.timeline.priority.3'),
  }

  const formatValue = (field: string, value: string): string => {
    if (field === 'priority') return priorityLabels[value] ?? value
    return value || t('issue.timeline.emptyValue')
  }

  const formatTime = (dateStr: string): string => {
    const date = new Date(dateStr)
    const now = new Date()
    const diff = now.getTime() - date.getTime()
    const mins = Math.floor(diff / 60000)
    if (mins < 1) return t('issue.timeline.now')
    if (mins < 60) return t('issue.timeline.minutesAgo', { count: mins })
    const hours = Math.floor(mins / 60)
    if (hours < 24) return t('issue.timeline.hoursAgo', { count: hours })
    const days = Math.floor(hours / 24)
    if (days < 30) return t('issue.timeline.daysAgo', { count: days })
    return date.toLocaleDateString()
  }

  const handleStartEdit = (entry: TimelineEntry) => {
    setEditingCommentId(entry.id)
    setEditContent(entry.content ?? '')
  }

  const handleSaveEdit = () => {
    if (editingCommentId && editContent.trim()) {
      onUpdateComment({ id: editingCommentId, content: editContent.trim() })
    }
    setEditingCommentId(null)
  }

  return (
    <div className="px-5 py-4">
      <span className="text-xs font-semibold text-muted-foreground mb-3 block">{t('issue.timeline.title')}</span>

      <div className="flex flex-col gap-3">
        {timeline.map((entry) => {
          if (entry.type === 'activity') {
            return (
              <div key={`a-${entry.id}`} className="flex gap-2 pl-1">
                <div className="w-0.5 bg-border flex-shrink-0 mt-1 rounded" />
                <div>
                  <div className="text-xs text-muted-foreground">
                    {entry.author && <span className="text-primary">{entry.author} </span>}
                    {entry.action === 'created' && t('issue.timeline.createdIssue')}
                    {entry.action === 'updated' && (
                      <>{t('issue.timeline.updatedField', { field: fieldLabels[entry.field ?? ''] ?? entry.field })} <span className="text-foreground/70">{formatValue(entry.field ?? '', entry.old_value ?? '')}</span> {t('issue.timeline.to')} <span className="text-foreground/70">{formatValue(entry.field ?? '', entry.new_value ?? '')}</span></>
                    )}
                    {entry.action === 'moved' && (<>{t('issue.timeline.movedTo')} <span className="text-foreground/70">{entry.new_value}</span></>)}
                    {entry.action === 'lifecycle_changed' && (<>{t('issue.timeline.lifecycleChanged')} <span className="text-foreground/70">{entry.old_value}</span> {t('issue.timeline.to')} <span className="text-foreground/70">{entry.new_value}</span></>)}
                  </div>
                  <div className="text-[10px] text-muted-foreground/50 mt-0.5">{formatTime(entry.created_at)}</div>
                </div>
              </div>
            )
          }

          return (
            <div key={`c-${entry.id}`} className="bg-accent/30 border border-border rounded-lg p-3 group">
              <div className="flex justify-between mb-1">
                <span className="text-xs text-primary font-medium">{entry.author || t('issue.comment.anonymous')}</span>
                <div className="flex items-center gap-2">
                  <span className="text-[10px] text-muted-foreground/50">{formatTime(entry.created_at)}</span>
                  {!readOnly && (
                    <div className="opacity-0 group-hover:opacity-100 flex gap-1">
                      <button onClick={() => handleStartEdit(entry)} className="text-muted-foreground hover:text-foreground"><Pencil className="w-3 h-3" /></button>
                      <button onClick={() => onDeleteComment(entry.id)} className="text-muted-foreground hover:text-destructive"><Trash2 className="w-3 h-3" /></button>
                    </div>
                  )}
                </div>
              </div>

              {editingCommentId === entry.id ? (
                <div>
                  <textarea className="w-full bg-background border border-border rounded p-2 text-sm resize-none" value={editContent} onChange={(e) => setEditContent(e.target.value)} autoFocus />
                  <div className="flex justify-end gap-2 mt-1">
                    <button className="text-xs px-2 py-0.5 border border-border rounded" onClick={() => setEditingCommentId(null)}>{t('issue.timeline.cancelEdit')}</button>
                    <button className="text-xs px-2 py-0.5 bg-primary text-primary-foreground rounded" onClick={handleSaveEdit}>{t('issue.timeline.saveEdit')}</button>
                  </div>
                </div>
              ) : (
                <div className="prose prose-sm dark:prose-invert max-w-none text-sm">
                  <ReactMarkdown
                    remarkPlugins={REMARK_PLUGINS}
                    rehypePlugins={REHYPE_PLUGINS}
                  >
                    {entry.content ?? ''}
                  </ReactMarkdown>
                </div>
              )}
              {isExternal && editingCommentId === entry.id && (
                <div className="mt-1 flex items-center justify-end">
                  <span className="text-[10px] text-warm-text-muted">{t('externalLink.commentEditWontSync')}</span>
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
