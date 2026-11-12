import React from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

vi.mock('@tanstack/react-router', () => ({
  Link: ({
    children,
    to,
    params,
  }: {
    children: React.ReactNode
    to?: string
    params?: Record<string, unknown>
  }) => (
    <a href={to ?? '#'} data-params={params ? JSON.stringify(params) : undefined}>
      {children}
    </a>
  ),
}))

// Echo keys and interpolate vars so assertions pin the key that rendered
// without depending on the translation text.
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
    list: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    login: vi.fn(),
    browserStatus: vi.fn(),
  },
}))

import { opsApi } from '@/lib/api'
import { OpsBotsPage } from '../ops-bots'
import type { MissionControlOpsView, OpsAccount } from '@/types/ops'

const account = (over: Partial<OpsAccount> = {}): OpsAccount => ({
  bot_id: 7,
  role: 'account',
  pack_id: 'wechat_mp',
  pack_version: 1,
  account_ref: 'brand-a',
  display_name: '品牌 A',
  status: 'active',
  operator: 'alice',
  output: { today: 2, week: 5 },
  budget: {
    limit: 100,
    currency: 'USD',
    spent: 12,
    remaining: 88,
    unlimited: false,
    configured: true,
    period_from: '2026-11-01T00:00:00Z',
    period_to: '2026-12-01T00:00:00Z',
  },
  pending_approvals: 1,
  high_risk_engagements: 0,
  risk: 'warning',
  risk_reasons: ['browser_login_expired'],
  updated_at: '2026-11-07T00:00:00Z',
  ...over,
})

const view = (accounts: OpsAccount[] = [account()]): MissionControlOpsView => ({
  owners: [
    {
      owner_type: 'user',
      owner_id: 1,
      owner_label: 'alice',
      channels: [{ pack_id: 'wechat_mp', pack_version: 1, accounts }],
    },
  ],
  account_count: accounts.length,
})

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <OpsBotsPage />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('OpsBotsPage', () => {
  it('groups accounts by owner and channel and shows the operator signals', async () => {
    vi.mocked(opsApi.list).mockResolvedValue(view())
    renderPage()

    expect(await screen.findAllByText('品牌 A')).not.toHaveLength(0)
    expect(screen.getAllByText('alice').length).toBeGreaterThan(0)
    expect(screen.getAllByText('wechat_mp').length).toBeGreaterThan(0)
    expect(screen.getAllByText(/^status\.active/).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/^risk\.warning/).length).toBeGreaterThan(0)
    expect(screen.getAllByText('output(today=2,week=5)').length).toBeGreaterThan(0)
    expect(screen.getAllByText('approvalsCount(count=1)').length).toBeGreaterThan(0)
    expect(
      screen.getAllByText(/budget\.remainingOf\(amount=88,limit=100,currency=USD\)/).length,
    ).toBeGreaterThan(0)
    // The account drills into its detail route with the bot id.
    const links = screen.getAllByText('品牌 A')
    expect(links.some((link) => link.getAttribute('data-params') === '{"botId":"7"}')).toBe(true)
  })

  it('pauses an active account from the list', async () => {
    vi.mocked(opsApi.list).mockResolvedValue(view())
    vi.mocked(opsApi.pause).mockResolvedValue({} as never)
    renderPage()

    const buttons = await screen.findAllByRole('button', { name: /actions\.pause/ })
    fireEvent.click(buttons[0])

    await waitFor(() => expect(opsApi.pause).toHaveBeenCalledWith(7))
    expect(opsApi.resume).not.toHaveBeenCalled()
  })

  it('offers resume for a paused account', async () => {
    vi.mocked(opsApi.list).mockResolvedValue(view([account({ status: 'paused' })]))
    vi.mocked(opsApi.resume).mockResolvedValue({} as never)
    renderPage()

    const buttons = await screen.findAllByRole('button', { name: /actions\.resume/ })
    fireEvent.click(buttons[0])

    await waitFor(() => expect(opsApi.resume).toHaveBeenCalledWith(7))
  })

  it('shows the conversation-first empty state when there are no accounts', async () => {
    vi.mocked(opsApi.list).mockResolvedValue({ owners: [], account_count: 0 })
    renderPage()

    expect(await screen.findByText('emptyTitle')).toBeInTheDocument()
    expect(screen.getByText('emptyDescription')).toBeInTheDocument()
    expect(screen.getByText(/emptySuggestion1/)).toBeInTheDocument()
  })

  it('keeps rendering the panel when one owner fails to load', async () => {
    vi.mocked(opsApi.list).mockResolvedValue({
      owners: [
        {
          owner_type: 'user',
          owner_id: 1,
          owner_label: 'alice',
          channels: [],
          error: 'boom',
        },
      ],
      account_count: 0,
    })
    renderPage()

    expect(await screen.findByText('ownerError(message=boom)')).toBeInTheDocument()
  })

  it('offers a relogin action when the browser session expired', async () => {
    vi.mocked(opsApi.list).mockResolvedValue(view())
    vi.mocked(opsApi.login).mockResolvedValue({
      bot_id: 7,
      state: 'running',
      login_status: 'expired',
      backend: 'local',
      has_session: true,
      has_login_recipe: true,
      next_action: 'relogin',
    })
    renderPage()

    const buttons = await screen.findAllByText('actions.relogin')
    fireEvent.click(buttons[0])
    await waitFor(() => expect(opsApi.login).toHaveBeenCalledWith(7))
  })
})
