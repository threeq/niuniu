import React from 'react'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const navigateMock = vi.fn()
const searchMock = vi.hoisted(() => ({ current: {} as Record<string, unknown> }))

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, to }: { children: React.ReactNode; to?: string }) => (
    <a href={to ?? '#'}>{children}</a>
  ),
  useNavigate: () => navigateMock,
  useSearch: () => searchMock.current,
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { language: 'en' },
  }),
}))

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

vi.mock('@/hooks/use-media-query', () => ({
  useMediaQuery: () => true,
}))

vi.mock('@/lib/hooks/use-projects', () => ({
  useProjects: () => ({
    projects: [{ id: 1, name: 'Project One' }],
    isLoading: false,
  }),
}))

vi.mock('@/lib/intake-api', () => ({
  intakeApi: {
    listDecisions: vi.fn(),
    listRules: vi.fn(),
    saveDraft: vi.fn(),
    accept: vi.fn(),
    ignore: vi.fn(),
    suggestGoal: vi.fn(),
  },
}))

import { intakeApi, type IntakeDecision } from '@/lib/intake-api'
import { IntakeReviewPage } from './intake-review-page'

const decision = (over: Partial<IntakeDecision> = {}): IntakeDecision => ({
  id: 1,
  event_id: 1,
  project_id: 1,
  source_kind: 'im_bot',
  event_kind: 'message',
  actor_ext_id: 'u1',
  actor_name: 'alice',
  thread_ext_id: 'thread-1',
  mode_at_decision: 'observe',
  strength: 'inferred',
  decision: 'accept',
  executed: false,
  reason_code: '',
  reason_text: '',
  matched_rule_ids: [5],
  policy_version: 3,
  issue_id: null,
  workspace_id: null,
  would_do: {
    decision: 'accept',
    brief: {
      Goal: '修复登录接口 500',
      Acceptance: ['复现并定位'],
      ScopeIn: ['api/auth'],
      ScopeOut: [],
    },
  },
  payload: { Text: '登录接口又 500 了', Attachments: [] },
  created_at: new Date().toISOString(),
  ...over,
})

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <IntakeReviewPage />
    </QueryClientProvider>,
  )
}

describe('IntakeReviewPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    searchMock.current = {}
    vi.mocked(intakeApi.listDecisions).mockResolvedValue([decision()])
    vi.mocked(intakeApi.listRules).mockResolvedValue([
      {
        id: 5,
        project_id: 1,
        source_kind: 'im_bot',
        name: '线上缺陷自动接单',
        instruction: '',
        conditions: { keywords: ['500'] },
        priority: 0,
        enabled: true,
        created_at: '2026-11-03T00:00:00Z',
      },
    ])
  })

  it('lists the pending decision with its summary, strength and rule name', async () => {
    renderPage()

    await waitFor(() =>
      expect(intakeApi.listDecisions).toHaveBeenCalledWith(1, {
        executed: false,
        decision: undefined,
        source_kind: undefined,
        limit: 100,
      }),
    )
    expect((await screen.findAllByText('修复登录接口 500')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('review.strength.inferred').length).toBeGreaterThan(0)
    expect(screen.getByText('线上缺陷自动接单')).toBeInTheDocument()
    expect(screen.getByText('登录接口又 500 了')).toBeInTheDocument()
  })

  it('requests the ignore filter', async () => {
    renderPage()
    await screen.findAllByText('修复登录接口 500')

    fireEvent.click(screen.getByText('review.filter.ignore'))

    await waitFor(() =>
      expect(intakeApi.listDecisions).toHaveBeenLastCalledWith(1, {
        executed: false,
        decision: 'ignore',
        source_kind: undefined,
        limit: 100,
      }),
    )
  })

  it('narrows the queue to one source (server-side filter)', async () => {
    searchMock.current = { source: 'github' }
    renderPage()

    await waitFor(() =>
      expect(intakeApi.listDecisions).toHaveBeenCalledWith(1, {
        executed: false,
        decision: undefined,
        source_kind: 'github',
        limit: 100,
      }),
    )
    expect(screen.getByLabelText('review.source')).toBeInTheDocument()
  })

  it('accepts with the edited brief and opens the created workspace', async () => {
    vi.mocked(intakeApi.accept).mockResolvedValue(
      decision({ issue_id: 7, workspace_id: 9, executed: true }),
    )
    renderPage()
    await screen.findAllByText('修复登录接口 500')

    fireEvent.click(screen.getByText('review.edit'))
    fireEvent.change(screen.getByLabelText('review.goal'), {
      target: { value: '修复并补回归测试' },
    })
    fireEvent.click(screen.getByText('review.accept'))

    await waitFor(() =>
      expect(intakeApi.accept).toHaveBeenCalledWith(1, 1, {
        Goal: '修复并补回归测试',
        Acceptance: ['复现并定位'],
        ScopeIn: ['api/auth'],
        ScopeOut: [],
      }),
    )
    await waitFor(() =>
      expect(navigateMock).toHaveBeenCalledWith({
        to: '/workspaces/$id',
        params: { id: '9' },
      }),
    )
  })

  it('requires a reason before ignoring and never calls the API without one', async () => {
    renderPage()
    await screen.findAllByText('修复登录接口 500')

    fireEvent.click(screen.getByText('review.ignore'))
    fireEvent.click(await screen.findByText('review.confirmIgnore'))

    expect(await screen.findByText('review.ignoreReasonRequired')).toBeInTheDocument()
    expect(intakeApi.ignore).not.toHaveBeenCalled()
  })

  it('shows the empty state when the queue has no decisions', async () => {
    vi.mocked(intakeApi.listDecisions).mockResolvedValue([])
    renderPage()

    expect(await screen.findByText('review.emptyTitle')).toBeInTheDocument()
    expect(screen.getByText('review.goToSources')).toBeInTheDocument()
  })
})
