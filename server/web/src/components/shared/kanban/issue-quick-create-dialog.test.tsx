import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/i18n'
import { IssueQuickCreateDialog } from './issue-quick-create-dialog'
import { api } from '@/lib/api'
import type { Issue } from '@/types/api'

vi.mock('@/lib/api', () => ({ api: { post: vi.fn() } }))

function renderDialog(props: Partial<React.ComponentProps<typeof IssueQuickCreateDialog>> = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <IssueQuickCreateDialog
        open
        onOpenChange={() => {}}
        projectId={1}
        columnId={2}
        issues={[]}
        {...props}
      />
    </QueryClientProvider>
  )
}

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

describe('IssueQuickCreateDialog', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.mocked(api.post).mockReset()
    vi.mocked(api.post).mockResolvedValue({ id: 99 } as never)
  })

  it('disables save until a title is entered', () => {
    renderDialog()
    const save = screen.getByTestId('quick-create-save') as HTMLButtonElement
    expect(save.disabled).toBe(true)
    fireEvent.change(screen.getByTestId('quick-create-title'), { target: { value: '加登录' } })
    expect(save.disabled).toBe(false)
  })

  it('POSTs once with the filled fields on save', async () => {
    renderDialog()
    fireEvent.change(screen.getByTestId('quick-create-title'), { target: { value: '加登录' } })
    fireEvent.change(screen.getByTestId('quick-create-description'), { target: { value: '用 JWT' } })
    fireEvent.click(screen.getByTestId('quick-create-save'))
    await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1))
    expect(api.post).toHaveBeenCalledWith('/columns/2/issues', expect.objectContaining({
      title: '加登录',
      description: '用 JWT',
    }))
  })

  it('clears the draft on save', async () => {
    renderDialog()
    fireEvent.change(screen.getByTestId('quick-create-title'), { target: { value: '加登录' } })
    await waitFor(() => expect(localStorage.getItem('niuniu:issue-draft:1:2')).not.toBeNull())
    fireEvent.click(screen.getByTestId('quick-create-save'))
    await waitFor(() => expect(localStorage.getItem('niuniu:issue-draft:1:2')).toBeNull())
  })

  it('clears the draft on cancel', async () => {
    renderDialog()
    fireEvent.change(screen.getByTestId('quick-create-title'), { target: { value: '加登录' } })
    await waitFor(() => expect(localStorage.getItem('niuniu:issue-draft:1:2')).not.toBeNull())
    fireEvent.click(screen.getByTestId('quick-create-cancel'))
    expect(localStorage.getItem('niuniu:issue-draft:1:2')).toBeNull()
  })

  it('drops workspace-bound plain tasks from the parent picker (spec 2026-09-28 s3)', () => {
    renderDialog({
      issues: [
        makeIssue({ id: 11, title: 'free plain task' }),
        makeIssue({ id: 12, title: 'bound plain task', has_workspace: true }),
        makeIssue({ id: 13, title: 'bound epic', issue_type: 'epic', has_workspace: true }),
      ],
    })
    // Open the general-mode parent picker and inspect its option list.
    fireEvent.click(screen.getByTestId('quick-create-parent'))
    expect(screen.getByText('#11')).toBeInTheDocument()
    expect(screen.getByText('#13')).toBeInTheDocument()
    // A plain task with a live workspace cannot collect children (server 409s)
    // — its entry is hidden. Epic parents are exempt and stay listed.
    expect(screen.queryByText('#12')).not.toBeInTheDocument()
  })

  describe('Epic subtask mode (parentIssueId set)', () => {
    it('hides the parent picker', () => {
      renderDialog({ parentIssueId: 7, defaultWave: 2 })
      expect(screen.queryByTestId('quick-create-parent')).toBeNull()
    })

    it('POSTs with the fixed parent + wave and never touches localStorage', async () => {
      renderDialog({ parentIssueId: 7, defaultWave: 2 })
      fireEvent.change(screen.getByTestId('quick-create-title'), { target: { value: '子任务' } })
      // Subtask mode must not persist a draft (would collide on the Epic's column).
      expect(localStorage.getItem('niuniu:issue-draft:1:2')).toBeNull()
      fireEvent.click(screen.getByTestId('quick-create-save'))
      await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1))
      expect(api.post).toHaveBeenCalledWith('/columns/2/issues', expect.objectContaining({
        title: '子任务',
        parent_issue_id: 7,
        issue_type: 'task',
        exec_wave: 2,
      }))
    })
  })
})
