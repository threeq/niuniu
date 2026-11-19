import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { I18nextProvider } from 'react-i18next'
import i18n from '@/i18n'
import { KanbanListView } from './KanbanListView'
import type { Column, Issue } from '@/types/api'

const wrap = (ui: React.ReactElement) => <I18nextProvider i18n={i18n}>{ui}</I18nextProvider>

function makeIssue(over: Partial<Issue>): Issue {
  return {
    id: 1,
    column_id: 1,
    project_id: 1,
    title: 'Issue',
    description: null,
    position: 0,
    assignees: [],
    labels: [],
    ...over,
  } as Issue
}

const column = {
  id: 1,
  project_id: 1,
  name: '待办',
  position: 0,
  lifecycle_mapping: '',
} as Column

const epic = makeIssue({ id: 10, title: 'Epic A', issue_type: 'epic', position: 0 })
const child = makeIssue({ id: 11, title: 'Child A1', parent_issue_id: 10, position: 0 })
const standalone = makeIssue({ id: 12, title: 'Standalone', position: 1 })

function renderList(over: Partial<React.ComponentProps<typeof KanbanListView>> = {}) {
  return render(
    wrap(
      <KanbanListView
        columns={[column]}
        issues={[epic, child, standalone]}
        workspaceByIssueId={{}}
        selectedIds={new Set<number>()}
        selectionActive={false}
        onToggleSelect={vi.fn()}
        onToggleSelectMany={vi.fn()}
        onIssueClick={vi.fn()}
        {...over}
      />,
    ),
  )
}

describe('KanbanListView', () => {
  it('hides subtasks until the epic is expanded', () => {
    renderList()
    expect(screen.getByText('Epic A')).toBeInTheDocument()
    expect(screen.getByText('Standalone')).toBeInTheDocument()
    expect(screen.queryByText('Child A1')).not.toBeInTheDocument()

    const expand = screen.getByRole('button', { name: /expand subtasks|展开子任务|展開子任務/i })
    fireEvent.click(expand)
    expect(screen.getByText('Child A1')).toBeInTheDocument()
  })

  it('allows selecting a subtask for bulk actions', () => {
    const onToggleSelect = vi.fn()
    renderList({ onToggleSelect })
    fireEvent.click(screen.getByRole('button', { name: /expand subtasks|展开子任务|展開子任務/i }))

    fireEvent.click(screen.getByLabelText(/Child A1/))
    expect(onToggleSelect).toHaveBeenCalledWith(11, false, expect.any(Array))
  })

  it('selects all children of an epic at once', () => {
    const onToggleSelectMany = vi.fn()
    renderList({ onToggleSelectMany })
    fireEvent.click(screen.getByRole('button', { name: /expand subtasks|展开子任务|展開子任務/i }))

    fireEvent.click(screen.getByLabelText(/select all subtasks|全选子任务|全選子任務/i))
    expect(onToggleSelectMany).toHaveBeenCalledWith([11], true)
  })
})
