import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import type { ImBotChat } from '@/types/imbot'
import type { IntakeDecision } from '@/lib/intake-api'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, to }: { children: React.ReactNode; to?: string }) => (
    <a href={to ?? '#'}>{children}</a>
  ),
}))

const patchChat = vi.fn()
vi.mock('@/lib/imbot-api', () => ({
  imbotApi: {
    listProjectIssues: vi.fn().mockResolvedValue([]),
    patchChat: (...args: unknown[]) => patchChat(...args),
    deleteChat: vi.fn(),
  },
  imbotOwnerApi: { reassignChat: vi.fn() },
}))

const getSettings = vi.fn()
const listDecisions = vi.fn()
vi.mock('@/lib/intake-api', () => ({
  intakeApi: {
    getSettings: (...args: unknown[]) => getSettings(...args),
    listDecisions: (...args: unknown[]) => listDecisions(...args),
  },
}))

vi.mock('@/pages/settings/imbot-settings/use-writable-projects', () => ({
  useWritableProjects: () => ({ projects: [] }),
}))

import { ChatRow } from './chat-row'

function chat(over: Partial<ImBotChat> = {}): ImBotChat {
  return {
    id: 7,
    channel_id: 1,
    chat_ext_id: 'oc_1',
    chat_name: '研发群',
    bind_mode: 'project',
    agent_mode: 'command',
    intake_mode: 'inherit',
    pinned_issue_id: null,
    active_issue_id: null,
    status: 'active',
    paired_by: null,
    created_at: '',
    updated_at: '',
    ...over,
  }
}


function decision(over: Partial<IntakeDecision> = {}): IntakeDecision {
  return {
    id: 1,
    event_id: 1,
    project_id: 1,
    source_kind: 'im_bot',
    event_kind: 'message',
    actor_ext_id: 'u1',
    actor_name: 'alice',
    thread_ext_id: '',
    mode_at_decision: 'observe',
    strength: 'inferred',
    decision: 'accept',
    executed: false,
    reason_code: '',
    reason_text: '',
    matched_rule_ids: [],
    policy_version: 1,
    issue_id: null,
    workspace_id: null,
    would_do: {},
    payload: { ChatExtID: 'oc_1' },
    created_at: '',
    ...over,
  }
}

function renderRow(over: Partial<ImBotChat> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <ChatRow projectId={1} chat={chat(over)} canManage />
    </QueryClientProvider>,
  )
}

describe('ChatRow intake mode', () => {
  beforeEach(() => {
    listDecisions.mockResolvedValue([])
  })

  it('renders a legacy inherit row as respond only', async () => {
    renderRow({ intake_mode: 'inherit' })

    expect(await screen.findByRole('combobox', { name: '接活模式' })).toHaveTextContent('仅响应')
  })

  it('links to the calibration queue with this conversation counted', async () => {
    listDecisions.mockResolvedValue([
      decision({ id: 1 }),
      decision({ id: 2, payload: { ChatExtID: 'oc_other' } }),
      decision({ id: 3, executed: true }),
    ])
    renderRow()

    expect(await screen.findByText('查看待校准 1')).toBeInTheDocument()
  })
})
