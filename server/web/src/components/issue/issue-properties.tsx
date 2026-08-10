import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import type { Issue, Column } from '@/types/api'
import { issuesApi } from '@/lib/api'
import { AssigneePicker } from './assignee-picker'
import { LabelPicker } from './label-picker'

interface IssuePropertiesProps {
  issue: Issue
  columns?: Column[]
  onUpdate: (data: Partial<Issue>) => void
  onMoveColumn?: (columnId: number) => void
  isMovingColumn?: boolean
  readOnly?: boolean
}

export function IssueProperties({ issue, columns, onUpdate, onMoveColumn, isMovingColumn, readOnly }: IssuePropertiesProps) {
  const { t } = useTranslation('projects')
  const qc = useQueryClient()
  const [editingField, setEditingField] = useState<string | null>(null)

  const priorityOptions = [
    { value: 0, label: t('issue.properties.low'), color: 'bg-green-100 text-green-700 dark:bg-green-950/50 dark:text-green-400' },
    { value: 1, label: t('issue.properties.medium'), color: 'bg-yellow-100 text-yellow-700 dark:bg-yellow-950/50 dark:text-yellow-400' },
    { value: 2, label: t('issue.properties.high'), color: 'bg-red-100 text-red-700 dark:bg-red-950/50 dark:text-red-400' },
    { value: 3, label: t('issue.properties.critical'), color: 'bg-red-200 text-red-800 dark:bg-red-950/70 dark:text-red-300' },
  ]

  const handleFieldSave = (field: string, value: string | number) => {
    if (readOnly) return
    onUpdate({ [field]: value })
    setEditingField(null)
  }

  const priorityOption = priorityOptions.find(p => p.value === (issue.priority ?? 0))

  return (
    <div className="grid grid-cols-[90px_1fr] gap-x-3 gap-y-2 text-sm border-b border-border pb-4 px-5">
      {/* Priority */}
      <span className="text-muted-foreground text-xs pt-1">{t('issue.properties.priority')}</span>
      <div>
        {editingField === 'priority' ? (
          <select
            className="bg-background border border-border rounded px-2 py-1 text-xs w-full"
            value={issue.priority ?? 0}
            onChange={(e) => handleFieldSave('priority', parseInt(e.target.value))}
            onBlur={() => setEditingField(null)}
            autoFocus
          >
            {priorityOptions.map(p => (
              <option key={p.value} value={p.value}>{p.label}</option>
            ))}
          </select>
        ) : (
          <span
            className={`inline-block px-2 py-0.5 rounded text-xs ${!readOnly ? 'cursor-pointer' : ''} ${priorityOption?.color}`}
            onClick={() => { if (!readOnly) setEditingField('priority') }}
          >
            {priorityOption?.label ?? t('issue.properties.low')}
          </span>
        )}
      </div>

      {/* Column */}
      {columns && columns.length > 0 && onMoveColumn && (
        <>
          <span className="text-muted-foreground text-xs pt-1">{t('issue.properties.column')}</span>
          <div>
            <select
              className="bg-background border border-border rounded px-2 py-1 text-xs w-full disabled:opacity-50"
              value={issue.column_id}
              disabled={isMovingColumn || readOnly}
              onChange={(e) => onMoveColumn(parseInt(e.target.value))}
            >
              {columns.map(col => (
                <option key={col.id} value={col.id}>{col.name}</option>
              ))}
            </select>
          </div>
        </>
      )}

      {/* Assignees */}
      <span className="text-muted-foreground text-xs pt-1">{t('issue.properties.assignees')}</span>
      <div>
        <AssigneePicker
          projectId={issue.project_id}
          value={(issue.assignees ?? []).map(a => a.id)}
          prefilled={issue.assignees}
          disabled={readOnly}
          onChange={async (ids) => {
            if (readOnly) return
            await issuesApi.setAssignees(issue.id, ids)
            qc.invalidateQueries({ queryKey: ['issue', issue.id] })
            qc.invalidateQueries({ queryKey: ['issue', String(issue.id)] })
            qc.invalidateQueries({ queryKey: ['all-issues'] })
          }}
        />
      </div>

      {/* Labels */}
      <span className="text-muted-foreground text-xs pt-1">{t('issue.properties.labels')}</span>
      <div>
        <LabelPicker
          projectId={issue.project_id}
          value={(issue.labels ?? []).map(l => l.id)}
          disabled={readOnly}
          onChange={async (ids) => {
            if (readOnly) return
            await issuesApi.setLabels(issue.id, ids)
            qc.invalidateQueries({ queryKey: ['issue', issue.id] })
            qc.invalidateQueries({ queryKey: ['issue', String(issue.id)] })
            qc.invalidateQueries({ queryKey: ['all-issues'] })
          }}
        />
      </div>

      {/* Start Date */}
      <span className="text-muted-foreground text-xs pt-1">{t('issue.properties.startDate')}</span>
      <input
        type="date"
        className="bg-background border border-border rounded px-2 py-1 text-xs w-fit"
        value={issue.start_date ?? ''}
        onChange={(e) => onUpdate({ start_date: e.target.value })}
        readOnly={readOnly}
        disabled={readOnly}
      />

      {/* Due Date */}
      <span className="text-muted-foreground text-xs pt-1">{t('issue.properties.dueDate')}</span>
      <input
        type="date"
        className="bg-background border border-border rounded px-2 py-1 text-xs w-fit"
        value={issue.due_date ?? ''}
        onChange={(e) => onUpdate({ due_date: e.target.value })}
        readOnly={readOnly}
        disabled={readOnly}
      />

      {/* Estimate */}
      <span className="text-muted-foreground text-xs pt-1">{t('issue.properties.estimate')}</span>
      <div className="flex gap-2 items-center">
        <input
          key={`est-${issue.estimate}`}
          type="number"
          className="bg-background border border-border rounded px-2 py-1 text-xs w-16"
          defaultValue={issue.estimate ?? 0}
          min={0}
          step={issue.estimate_type === 'hours' ? 0.5 : 1}
          onBlur={(e) => onUpdate({ estimate: parseFloat(e.target.value) || 0 })}
          readOnly={readOnly}
          disabled={readOnly}
        />
        {!readOnly && (
          <div className="flex border border-border rounded overflow-hidden text-xs">
            <button
              className={`px-2 py-1 ${(issue.estimate_type ?? '') === 'points' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground'}`}
              onClick={() => onUpdate({ estimate_type: 'points' })}
            >Points</button>
            <button
              className={`px-2 py-1 ${issue.estimate_type === 'hours' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground'}`}
              onClick={() => onUpdate({ estimate_type: 'hours' })}
            >Hours</button>
          </div>
        )}
      </div>

      {/* Actual Time */}
      <span className="text-muted-foreground text-xs pt-1">{t('issue.properties.actualTime')}</span>
      <div className="flex gap-1 items-center">
        <input
          key={`at-${issue.actual_time}`}
          type="number"
          className="bg-background border border-border rounded px-2 py-1 text-xs w-16"
          defaultValue={issue.actual_time ?? 0}
          min={0}
          step={0.5}
          onBlur={(e) => onUpdate({ actual_time: parseFloat(e.target.value) || 0 })}
          readOnly={readOnly}
          disabled={readOnly}
        />
        <span className="text-xs text-muted-foreground">{t('issue.properties.hours')}</span>
      </div>
    </div>
  )
}
