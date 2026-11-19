import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight, Layers } from 'lucide-react'
import type { Column, Issue, Workspace, WorkspaceStatus } from '@/types/api'
import { Checkbox } from '@/components/ui/checkbox'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { WORKSPACE_STATUS_LABELS } from '@/lib/workspace-status'
import { ExecStatusBadge } from './exec-status-badge'
import { HighlightText } from './search-filter'

interface KanbanListViewProps {
  columns: Column[]
  issues: Issue[]
  workspaceByIssueId: Record<string, Workspace>
  selectedIds: Set<number>
  selectionActive: boolean
  /** Shift-range selection over the currently visible flattened rows. */
  onToggleSelect: (issueId: number, shiftKey: boolean, visible: Issue[]) => void
  onToggleSelectMany: (issueIds: number[], checked: boolean) => void
  onIssueClick: (issueId: number) => void
  highlightQuery?: string
}

const PRIORITY_DOT: Record<number, string> = {
  0: 'bg-prio-low',
  1: 'bg-prio-medium',
  2: 'bg-prio-high',
  3: 'bg-prio-critical',
}

/**
 * Flat list counterpart of the kanban board. Epics expand/collapse their
 * subtasks inline, and every row (including children) has a bulk-selection
 * checkbox — this is the surface where subtask batch operations become
 * possible. Rows are grouped by column, mirroring the board's left-to-right
 * order.
 */
export function KanbanListView({
  columns,
  issues,
  workspaceByIssueId,
  selectedIds,
  selectionActive,
  onToggleSelect,
  onToggleSelectMany,
  onIssueClick,
  highlightQuery,
}: KanbanListViewProps) {
  const { t } = useTranslation('projects')
  const [expanded, setExpanded] = useState<Set<number>>(() => new Set())
  const [collapsedColumns, setCollapsedColumns] = useState<Set<number>>(() => new Set())

  const childrenByParent = useMemo(() => {
    const map = new Map<number, Issue[]>()
    for (const issue of issues) {
      const parent = issue.parent_issue_id
      if (parent == null) continue
      const arr = map.get(parent) ?? []
      arr.push(issue)
      map.set(parent, arr)
    }
    for (const arr of map.values()) arr.sort((a, b) => a.position - b.position)
    return map
  }, [issues])

  const topLevel = useMemo(() => issues.filter((i) => i.parent_issue_id == null), [issues])

  // Group top-level rows by column. A filtered-out epic takes its matching
  // children with it only when it remains visible; children whose parent is not
  // in the list are surfaced as top-level rows so they stay selectable.
  const groups = useMemo(() => {
    const visibleIds = new Set(topLevel.map((i) => i.id))
    const orphans = issues.filter(
      (i) => i.parent_issue_id != null && !visibleIds.has(i.parent_issue_id),
    )
    return columns.map((column) => ({
      column,
      items: [...topLevel, ...orphans]
        .filter((i) => i.column_id === column.id)
        .sort((a, b) => a.position - b.position),
    }))
  }, [columns, issues, topLevel])

  const epicIdsWithChildren = useMemo(
    () => topLevel.filter((i) => (childrenByParent.get(i.id)?.length ?? 0) > 0).map((i) => i.id),
    [topLevel, childrenByParent],
  )

  // Flattened order drives shift-range selection: epics, then their children
  // when expanded. Rows in collapsed columns are not visible and therefore not
  // part of the shift-range.
  const visibleRows = useMemo(() => {
    const rows: Issue[] = []
    for (const group of groups) {
      if (collapsedColumns.has(group.column.id)) continue
      for (const issue of group.items) {
        rows.push(issue)
        if (expanded.has(issue.id)) {
          rows.push(...(childrenByParent.get(issue.id) ?? []))
        }
      }
    }
    return rows
  }, [groups, expanded, childrenByParent, collapsedColumns])

  const toggleColumn = (id: number) => {
    setCollapsedColumns((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const toggleExpanded = (id: number) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  // The header pair expands/collapses both levels: every column and every
  // epic's subtasks. Individual column headers and epic rows stay independent.
  const expandAll = () => {
    setExpanded(new Set(epicIdsWithChildren))
    setCollapsedColumns(new Set())
  }
  const collapseAll = () => {
    setExpanded(new Set())
    setCollapsedColumns(new Set(columns.map((c) => c.id)))
  }

  const toggleEpicChildren = (epicId: number, checked: boolean) => {
    onToggleSelectMany((childrenByParent.get(epicId) ?? []).map((c) => c.id), checked)
  }

  if (issues.length === 0) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-warm-text-muted">
        {t('kanban.list.empty')}
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-warm-canvas">
      <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b border-warm-border bg-warm-surface px-3 py-1.5">
        <span className="text-xs text-warm-text-muted tabular-nums">
          {t('kanban.header.totalIssuesPrefix')}
          {issues.length}
          {t('kanban.header.totalIssuesSuffix')}
        </span>
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={expandAll}>
            {t('kanban.list.expandAll')}
          </Button>
          <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={collapseAll}>
            {t('kanban.list.collapseAll')}
          </Button>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {groups.map(({ column, items }) => {
          const collapsed = collapsedColumns.has(column.id)
          return (
          <section key={column.id}>
            <div className="sticky top-0 z-10 border-b border-warm-border bg-warm-surface">
              <button
                type="button"
                onClick={() => toggleColumn(column.id)}
                aria-expanded={!collapsed}
                aria-label={t(
                  collapsed ? 'kanban.list.expandColumn' : 'kanban.list.collapseColumn',
                  { name: column.name },
                )}
                className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs font-medium text-warm-text-muted hover:bg-warm-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand"
              >
                {collapsed ? (
                  <ChevronRight className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                ) : (
                  <ChevronDown className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                )}
                <span className="truncate">{column.name}</span>
                <span className="rounded-full bg-warm-muted px-1.5 tabular-nums">{items.length}</span>
              </button>
            </div>
            {!collapsed && (items.length === 0 ? (
              <p className="px-4 py-2 text-xs text-warm-text-muted">{t('kanban.list.empty')}</p>
            ) : (
              <ul>
                {items.map((issue) => {
                  const children = childrenByParent.get(issue.id) ?? []
                  const hasChildren = children.length > 0
                  const isExpanded = expanded.has(issue.id)
                  const selected = selectedIds.has(issue.id)
                  const ws = workspaceByIssueId[String(issue.id)]
                  const priority = issue.priority ?? 1
                  return (
                    <li key={issue.id}>
                      <div
                        className={cn(
                          'group flex items-center gap-2 border-b border-warm-border/60 px-3 py-2',
                          selected && 'bg-brand-soft/50',
                          selectionActive && !selected && 'hover:bg-warm-muted/60',
                        )}
                      >
                        <div
                          className="flex shrink-0 items-center"
                          onPointerDown={(e) => e.stopPropagation()}
                          onClick={(e) => {
                            e.stopPropagation()
                            onToggleSelect(issue.id, e.shiftKey, visibleRows)
                          }}
                        >
                          <Checkbox
                            checked={selected}
                            className="pointer-events-none bg-warm-surface"
                            aria-label={t('kanban.bulk.selectIssue', { title: issue.title })}
                          />
                        </div>

                        {hasChildren ? (
                          <button
                            type="button"
                            onClick={() => toggleExpanded(issue.id)}
                            aria-label={t(
                              isExpanded ? 'kanban.card.collapseSubtasks' : 'kanban.card.expandSubtasks',
                            )}
                            aria-expanded={isExpanded}
                            className="flex h-6 w-6 shrink-0 items-center justify-center rounded text-warm-text-muted hover:bg-warm-muted hover:text-warm-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand"
                          >
                            {isExpanded ? (
                              <ChevronDown className="h-4 w-4" aria-hidden="true" />
                            ) : (
                              <ChevronRight className="h-4 w-4" aria-hidden="true" />
                            )}
                          </button>
                        ) : (
                          <span className="h-6 w-6 shrink-0" aria-hidden="true" />
                        )}

                        <span className="shrink-0 text-[11px] tabular-nums text-warm-text-muted">
                          #{issue.id}
                        </span>
                        <span
                          className={cn('h-2 w-2 shrink-0 rounded-full', PRIORITY_DOT[priority])}
                          title={t(`kanban.priority.${['low', 'medium', 'high', 'critical'][priority] ?? 'medium'}`)}
                          aria-hidden="true"
                        />
                        <button
                          type="button"
                          onClick={() => onIssueClick(issue.id)}
                          className="min-w-0 flex-1 truncate text-left text-sm text-warm-text hover:text-brand focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand rounded"
                        >
                          {highlightQuery ? (
                            <HighlightText text={issue.title} query={highlightQuery} />
                          ) : (
                            issue.title
                          )}
                        </button>

                        {hasChildren && (
                          <span className="flex shrink-0 items-center gap-1 rounded bg-brand-soft px-1.5 py-0.5 text-[10px] font-medium text-brand">
                            <Layers className="h-3 w-3" aria-hidden="true" />
                            {t('kanban.list.childCount', { count: children.length })}
                          </span>
                        )}
                        {issue.exec_status && issue.exec_status !== 'idle' && (
                          <ExecStatusBadge status={issue.exec_status} />
                        )}
                        {ws && (
                          <span className="hidden shrink-0 items-center gap-1 rounded border border-warm-border bg-warm-muted px-1.5 py-0.5 text-[10px] text-warm-text-muted sm:inline-flex">
                            <span className="max-w-24 truncate">{ws.name}</span>
                            <span>{WORKSPACE_STATUS_LABELS[(ws.status as WorkspaceStatus)] ?? ws.status}</span>
                          </span>
                        )}

                        {hasChildren && (
                          <div
                            className="flex shrink-0 items-center gap-1"
                            onClick={(e) => e.stopPropagation()}
                          >
                            <Checkbox
                              checked={
                                children.length > 0 && children.every((c) => selectedIds.has(c.id))
                              }
                              onCheckedChange={(v) => toggleEpicChildren(issue.id, v === true)}
                              aria-label={t('kanban.list.selectAllChildren')}
                              title={t('kanban.list.selectAllChildren')}
                            />
                          </div>
                        )}
                      </div>

                      {hasChildren && isExpanded && (
                        <ul>
                          {children.map((child) => {
                            const childSelected = selectedIds.has(child.id)
                            const childWs = workspaceByIssueId[String(child.id)]
                            const childPriority = child.priority ?? 1
                            return (
                              <li key={child.id}>
                                <div
                                  className={cn(
                                    'flex items-center gap-2 border-b border-warm-border/40 bg-warm-surface/40 px-3 py-1.5 pl-12',
                                    childSelected && 'bg-brand-soft/40',
                                  )}
                                >
                                  <div
                                    className="flex shrink-0 items-center"
                                    onPointerDown={(e) => e.stopPropagation()}
                                    onClick={(e) => {
                                      e.stopPropagation()
                                      onToggleSelect(child.id, e.shiftKey, visibleRows)
                                    }}
                                  >
                                    <Checkbox
                                      checked={childSelected}
                                      className="pointer-events-none bg-warm-surface"
                                      aria-label={t('kanban.bulk.selectIssue', { title: child.title })}
                                    />
                                  </div>
                                  <span className="shrink-0 text-[11px] tabular-nums text-warm-text-muted">
                                    #{child.id}
                                  </span>
                                  <span
                                    className={cn('h-2 w-2 shrink-0 rounded-full', PRIORITY_DOT[childPriority])}
                                    aria-hidden="true"
                                  />
                                  <button
                                    type="button"
                                    onClick={() => onIssueClick(child.id)}
                                    className="min-w-0 flex-1 truncate text-left text-sm text-warm-text hover:text-brand focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand rounded"
                                  >
                                    {highlightQuery ? (
                                      <HighlightText text={child.title} query={highlightQuery} />
                                    ) : (
                                      child.title
                                    )}
                                  </button>
                                  {child.exec_status && child.exec_status !== 'idle' && (
                                    <ExecStatusBadge status={child.exec_status} />
                                  )}
                                  {childWs && (
                                    <span className="hidden shrink-0 items-center gap-1 rounded border border-warm-border bg-warm-muted px-1.5 py-0.5 text-[10px] text-warm-text-muted sm:inline-flex">
                                      <span className="max-w-24 truncate">{childWs.name}</span>
                                      <span>{WORKSPACE_STATUS_LABELS[(childWs.status as WorkspaceStatus)] ?? childWs.status}</span>
                                    </span>
                                  )}
                                </div>
                              </li>
                            )
                          })}
                        </ul>
                      )}
                    </li>
                  )
                })}
              </ul>
            ))}
          </section>
          )
        })}
      </div>
    </div>
  )
}
