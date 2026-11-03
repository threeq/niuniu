import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { IssueBriefCard } from './issue-brief-card'
import { api } from '@/lib/api'
import { useIssueChecklists } from '@/lib/hooks/use-issue-checklists'
import type { IssueBriefResponse, TaskBriefVersion } from '@/types/api'

vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api')
  return {
    ...actual,
    api: {
      ...actual.api,
      getIssueBrief: vi.fn(),
      putIssueBrief: vi.fn(),
    },
  }
})

vi.mock('@/lib/hooks/use-issue-checklists', () => ({
  useIssueChecklists: vi.fn(),
}))

const mockedGetBrief = vi.mocked(api.getIssueBrief)
const mockedUseChecklists = vi.mocked(useIssueChecklists)

const updateChecklist = vi.fn()

const brief: TaskBriefVersion = {
  id: 1,
  issue_id: 7,
  version: 1,
  goal: '修复登录接口 500',
  acceptance: ['复现并定位', '修复并补测试'],
  scope_in: ['api/auth'],
  scope_out: ['前端'],
  delivery: { source_kind: 'im_bot', thread_ext_id: 'thread-1', reply_mode: 'thread' },
  provenance: {
    decision_id: 42,
    policy_version: 3,
    strength: 'explicit',
    actor_ext_id: 'ou_1',
    rule_ids: [5],
  },
  intake_decision_id: 42,
  policy_version: 3,
  created_by: null,
  created_at: '2026-11-03T10:00:00Z',
}

function briefResponse(overrides: Partial<IssueBriefResponse> = {}): IssueBriefResponse {
  return {
    issue_id: 7,
    goal_condition: brief.goal,
    brief,
    versions: [brief],
    provenance_event: {
      source_kind: 'im_bot',
      event_kind: 'message',
      actor_ext_id: 'ou_1',
      actor_name: 'Alice',
      thread_ext_id: 'thread-1',
      event_ext_id: 'msg-1',
      source_ref: '',
      payload: { Text: '登录接口又 500 了，麻烦看下' },
      received_at: '2026-11-03T09:59:00Z',
      rule_names: ['线上缺陷自动接单'],
    },
    ...overrides,
  }
}

function renderCard(issueId = 7) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <IssueBriefCard issueId={issueId} />
    </QueryClientProvider>,
  )
}

describe('IssueBriefCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockedUseChecklists.mockReturnValue({
      checklists: [
        {
          id: 11,
          issue_id: 7,
          title: '复现并定位',
          is_completed: 0,
          position: 0,
          kind: 'acceptance',
          created_at: '2026-11-03T10:00:00Z',
          updated_at: '2026-11-03T10:00:00Z',
        },
        {
          id: 12,
          issue_id: 7,
          title: '修复并补测试',
          is_completed: 0,
          position: 1,
          kind: 'acceptance',
          created_at: '2026-11-03T10:00:00Z',
          updated_at: '2026-11-03T10:00:00Z',
        },
      ],
      isLoading: false,
      createChecklist: vi.fn(),
      updateChecklist,
      updateChecklistPosition: vi.fn(),
      deleteChecklist: vi.fn(),
    })
  })

  it('renders nothing for an issue without a brief (non-Intake)', async () => {
    mockedGetBrief.mockResolvedValue(briefResponse({ brief: null, versions: [] }))
    const { container } = renderCard()
    await waitFor(() => expect(mockedGetBrief).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  it('shows goal, acceptance, scope and delivery for an intake issue', async () => {
    mockedGetBrief.mockResolvedValue(briefResponse())
    renderCard()
    expect(await screen.findByText('修复登录接口 500')).toBeInTheDocument()
    expect(screen.getByText('复现并定位')).toBeInTheDocument()
    expect(screen.getByText('api/auth')).toBeInTheDocument()
    expect(screen.getByText('前端')).toBeInTheDocument()
    expect(screen.getByText('thread-1', { exact: false })).toBeInTheDocument()
  })

  it('toggles the matching acceptance checklist row', async () => {
    mockedGetBrief.mockResolvedValue(briefResponse())
    renderCard()
    const checkbox = await screen.findByRole('checkbox', { name: '复现并定位' })
    fireEvent.click(checkbox)
    expect(updateChecklist).toHaveBeenCalledWith({ id: 11, title: '复现并定位', is_completed: 1 })
  })

  it('shows version history with a diff and a rollback action for older versions', async () => {
    const v2: TaskBriefVersion = {
      ...brief,
      id: 2,
      version: 2,
      goal: '修复登录接口 500 并补回归测试',
      acceptance: ['复现并定位', '修复并补测试', '回归测试'],
    }
    mockedGetBrief.mockResolvedValue(
      briefResponse({ brief: v2, versions: [v2, brief] }),
    )
    renderCard()
    await screen.findByText('修复登录接口 500 并补回归测试')
    fireEvent.click(screen.getByRole('button', { name: /版本历史/ }))
    expect(screen.getByText('v1')).toBeInTheDocument()
    expect(screen.getAllByText('v2').length).toBeGreaterThan(0)
    expect(screen.getByRole('button', { name: '回滚到此版本' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '对比' }))
    expect(screen.getByText('新增')).toBeInTheDocument()
    expect(screen.getAllByText('回归测试').length).toBeGreaterThan(0)
  })

  it('opens the provenance popover with actor, rules, decision and the original event', async () => {
    mockedGetBrief.mockResolvedValue(briefResponse())
    renderCard()
    await screen.findByText('修复登录接口 500')
    fireEvent.click(screen.getByRole('button', { name: '来源追溯' }))
    expect(await screen.findByText('来源追溯')).toBeInTheDocument()
    expect(screen.getByText('Alice')).toBeInTheDocument()
    expect(screen.getByText('线上缺陷自动接单')).toBeInTheDocument()
    expect(screen.getByText('#42')).toBeInTheDocument()
    expect(screen.getByText('登录接口又 500 了，麻烦看下')).toBeInTheDocument()
  })

  it('shows repo, thread, event and a permalink for a GitHub-origin brief', async () => {
    const url = 'https://github.com/acme/foo/pull/42#issuecomment-1'
    mockedGetBrief.mockResolvedValue(
      briefResponse({
        provenance_event: {
          source_kind: 'github',
          event_kind: 'issue_comment',
          actor_ext_id: 'alice',
          actor_name: 'alice',
          thread_ext_id: '99',
          event_ext_id: 'issue_comment:99:1700000000',
          source_ref: 'acme/foo',
          payload: { body: 'please fix', html_url: url },
          received_at: '2026-11-03T09:59:00Z',
          rule_names: [],
          text: 'please fix',
          url,
        },
      }),
    )
    renderCard()
    await screen.findByText('修复登录接口 500')
    fireEvent.click(screen.getByRole('button', { name: '来源追溯' }))

    expect(await screen.findByText('acme/foo')).toBeInTheDocument()
    expect(screen.getByText('#99')).toBeInTheDocument()
    expect(screen.getByText('issue_comment:99:1700000000')).toBeInTheDocument()
    expect(screen.getByText('please fix')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /打开来源/ })).toHaveAttribute('href', url)
  })
})
