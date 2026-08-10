import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Plus, Trash2 } from 'lucide-react'
import type { IssueChecklist as ChecklistItem } from '@/types/api'
import { isIMEComposing } from '@/lib/ime'

interface IssueChecklistProps {
  checklists: ChecklistItem[]
  onCreate: (title: string) => void
  onUpdate: (data: { id: number; title: string; is_completed: number }) => void
  onDelete: (id: number) => void
  readOnly?: boolean
}

export function IssueChecklist({ checklists, onCreate, onUpdate, onDelete, readOnly }: IssueChecklistProps) {
  const { t } = useTranslation('projects')
  const [newTitle, setNewTitle] = useState('')
  const [editingId, setEditingId] = useState<number | null>(null)
  const [editTitle, setEditTitle] = useState('')

  const total = checklists.length
  const completed = checklists.filter(c => c.is_completed === 1).length
  const progress = total > 0 ? (completed / total) * 100 : 0

  const handleAdd = () => {
    if (!newTitle.trim()) return
    onCreate(newTitle.trim())
    setNewTitle('')
  }

  const handleToggle = (item: ChecklistItem) => {
    onUpdate({ id: Number(item.id), title: item.title, is_completed: item.is_completed === 1 ? 0 : 1 })
  }

  const handleStartEdit = (item: ChecklistItem) => {
    setEditingId(Number(item.id))
    setEditTitle(item.title)
  }

  const handleSaveEdit = (item: ChecklistItem) => {
    if (editTitle.trim()) {
      onUpdate({ id: Number(item.id), title: editTitle.trim(), is_completed: item.is_completed })
    }
    setEditingId(null)
  }

  return (
    <div className="border-b border-border px-5 py-4">
      <div className="flex justify-between items-center mb-2">
        <span className="text-xs font-semibold text-muted-foreground">{t('issue.checklist.title')}</span>
        {total > 0 && <span className="text-xs text-muted-foreground">{completed}/{total}</span>}
      </div>

      {total > 0 && (
        <div className="bg-accent h-1 rounded-full mb-3">
          <div className="bg-green-500 dark:bg-green-400 h-1 rounded-full transition-all duration-300" style={{ width: `${progress}%` }} />
        </div>
      )}

      <div className="flex flex-col gap-1">
        {checklists.map(item => (
          <div key={item.id} className="flex items-center gap-2 group hover:bg-accent/30 rounded px-1 py-0.5">
            <button
              className={`w-4 h-4 rounded border flex-shrink-0 flex items-center justify-center ${
                item.is_completed === 1 ? 'bg-green-600 border-green-600 dark:bg-green-500 dark:border-green-500' : 'border-muted-foreground/40'
              }`}
              onClick={() => { if (!readOnly) handleToggle(item) }}
              disabled={readOnly}
            >
              {item.is_completed === 1 && <Check className="w-3 h-3 text-white" />}
            </button>

            {editingId === Number(item.id) ? (
              <input
                className="flex-1 bg-background border border-border rounded px-2 py-0.5 text-sm"
                value={editTitle}
                onChange={(e) => setEditTitle(e.target.value)}
                onBlur={() => handleSaveEdit(item)}
                onKeyDown={(e) => { if (isIMEComposing(e)) return; if (e.key === 'Enter') handleSaveEdit(item) }}
                autoFocus
              />
            ) : (
              <span
                className={`flex-1 text-sm ${!readOnly ? 'cursor-pointer' : ''} ${item.is_completed === 1 ? 'line-through text-muted-foreground' : ''}`}
                onClick={() => { if (!readOnly) handleStartEdit(item) }}
              >
                {item.title}
              </span>
            )}

            {!readOnly && (
              <button className="opacity-0 group-hover:opacity-100 text-muted-foreground hover:text-destructive" onClick={() => onDelete(Number(item.id))}>
                <Trash2 className="w-3 h-3" />
              </button>
            )}
          </div>
        ))}
      </div>

      {!readOnly && (
        <div className="flex gap-2 mt-2">
          <input
            className="flex-1 bg-background border border-border rounded px-2 py-1 text-xs placeholder:text-muted-foreground"
            placeholder={t('issue.checklist.addPlaceholder')}
            value={newTitle}
            onChange={(e) => setNewTitle(e.target.value)}
            onKeyDown={(e) => { if (isIMEComposing(e)) return; if (e.key === 'Enter') handleAdd() }}
          />
          <button className="text-primary hover:text-primary/80" onClick={handleAdd} disabled={!newTitle.trim()}>
            <Plus className="w-4 h-4" />
          </button>
        </div>
      )}
    </div>
  )
}
