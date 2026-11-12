import React from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children }: { children: React.ReactNode }) => <a href="#">{children}</a>,
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, vars?: Record<string, unknown>) => {
      if (!vars) return key
      const parts = Object.entries(vars)
        .map(([name, value]) => `${name}=${String(value)}`)
        .join(',')
      return `${key}(${parts})`
    },
  }),
}))

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

vi.mock('@/lib/api', () => ({
  opsApi: {
    getBot: vi.fn(),
    list: vi.fn(),
    budget: vi.fn(),
    metrics: vi.fn(),
    calendar: vi.fn(),
    engagements: vi.fn(),
    listApprovals: vi.fn(),
    browserStatus: vi.fn(),
    login: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    approve: vi.fn(),
    reject: vi.fn(),
  },
}))

import { opsApi } from '@/lib/api'
import { OpsBotDetailPage } from '../ops-bot-detail'
import type { MissionControlOpsView, OpsAccount, OpsBot, OpsBrowserStatus } from '@/types/ops'

const bot: OpsBot = {
  id: 7,
  owner_type: 'user',
  owner_id: 1,
  role: 'account',
  pack_id: 'xiaohongshu',
  pack_version: 1,
  account_ref: 'brand-a',
  display_name: '品牌 A',
  persona: {},
  credential_refs: {},
  knowledge_scope: {},
  goals: {},
  cadence: {},
  approval_policy: {},
  budget: {},
  epic_issue_id: null,
  control_workspace_id: null,
  director_bot_id: null,
  status: 'active',
  created_at: '2026-11-07T00:00:00Z',
  updated_at: '2026-11-07T00:00:00Z',
}

const account: OpsAccount = {
  bot_id: 7,
  role: 'account',
  pack_id: 'xiaohongshu',
  pack_version: 1,
  account_ref: 'brand-a',
  display_name: '品牌 A',
  status: 'active',
  operator: 'alice',
  output: { today: 0, week: 0 },
  budget: {
    limit: 100,
    currency: 'USD',
    spent: 0,
    remaining: 100,
    unlimited: false,
    configured: true,
    period_from: '2026-11-01T00:00:00Z',
    period_to: '2026-12-01T00:00:00Z',
  },
  pending_approvals: 0,
  high_risk_engagements: 0,
  risk: 'warning',
  risk_reasons: ['browser_login_expired'],
  browser: { backend: 'local', login_status: 'expired', health: 'degraded', risk_state: 'normal' },
  updated_at: '2026-11-07T00:00:00Z',
}

const view: MissionControlOpsView = {
  owners: [
    {
      owner_type: 'user',
      owner_id: 1,
      owner_label: 'alice',
      channels: [{ pack_id: 'xiaohongshu', pack_version: 1, accounts: [account] }],
    },
  ],
  account_count: 1,
}

const browserStatus: OpsBrowserStatus = {
  bot_id: 7,
  state: 'failed',
  login_status: 'expired',
  backend: 'local',
  health: 'degraded',
  risk_state: 'normal',
  has_session: true,
  has_login_recipe: true,
  next_action: 'relogin',
  last_error: 'session expired',
  evidence_url: '/api/ops/bots/7/browser/evidence',
}

function renderDetail() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <OpsBotDetailPage botId={7} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(opsApi.getBot).mockResolvedValue(bot)
  vi.mocked(opsApi.list).mockResolvedValue(view)
  vi.mocked(opsApi.budget).mockResolvedValue({
    bot_id: 7,
    period_from: '2026-11-01T00:00:00Z',
    period_to: '2026-12-01T00:00:00Z',
    limit: 100,
    currency: 'USD',
    unlimited: false,
    spent: 0,
    remaining: 100,
    items: [],
  })
  vi.mocked(opsApi.metrics).mockResolvedValue({ bot_id: 7, from: '', to: '', items: [] })
  vi.mocked(opsApi.calendar).mockResolvedValue({ bot_id: 7, cadence: {}, items: [], count: 0 })
  vi.mocked(opsApi.engagements).mockResolvedValue({ items: [], count: 0 })
  vi.mocked(opsApi.listApprovals).mockResolvedValue({ items: [], count: 0 })
  vi.mocked(opsApi.browserStatus).mockResolvedValue(browserStatus)
  vi.mocked(opsApi.login).mockResolvedValue({ ...browserStatus, state: 'running' })
})

describe('OpsBotDetailPage login', () => {
  it('starts a relogin and surfaces the QR evidence', async () => {
    renderDetail()

    const button = await screen.findByText('login.relogin')
    expect(screen.getByAltText('login.qrAlt')).toBeInTheDocument()
    expect(screen.getByText('login.failed(message=session expired)')).toBeInTheDocument()

    fireEvent.click(button)
    await waitFor(() => expect(opsApi.login).toHaveBeenCalledWith(7))
  })
})
