import React from 'react'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

vi.mock('@tanstack/react-router', () => ({
  Link: ({
    children,
    to,
    search,
  }: {
    children: React.ReactNode
    to?: string
    search?: Record<string, unknown>
  }) => (
    <a href={to ?? '#'} data-search={search ? JSON.stringify(search) : undefined}>
      {children}
    </a>
  ),
}))

// Echo keys so assertions can pin which i18n key rendered, and interpolate
// {{value}} for the unit strings.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, vars?: Record<string, unknown>) =>
      vars && 'value' in vars ? `${key}:${String(vars.value)}` : key,
  }),
}))

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

vi.mock('@/lib/mission-control-api', () => ({
  missionControlApi: {
    get: vi.fn(),
    setPaused: vi.fn(),
  },
}))

import { missionControlApi, type MissionControlView } from '@/lib/mission-control-api'
import { MissionControlPanels } from './mission-control-panels'

const metric = (
  key: string,
  over: Partial<MissionControlView['intake']['metrics'][number]> = {},
) => ({
  key,
  value: 3,
  unit: 'count' as const,
  status: 'ok' as const,
  drill: 'intake',
  ...over,
})

function view(over: Partial<MissionControlView> = {}): MissionControlView {
  return {
    intake: {
      paused: false,
      project_count: 1,
      metrics: [
        metric('today_accepted'),
        metric('pending', { value: null, status: 'no_data' }),
        metric('approval_rate', { value: 50, unit: 'percent', num: 1, denom: 2 }),
        metric('misfire_rate', { value: 25, unit: 'percent', status: 'warning' }),
        metric('duplicate_rate', { value: 0, unit: 'percent' }),
        metric('event_to_start', { value: 12, unit: 'minutes', drill: 'intake' }),
      ],
      per_source: [{ source_kind: 'im_bot', day: '2026-11-03', count: 2 }],
      per_chat: [{ source_kind: 'im_bot', thread_ext_id: 'thread-1', day: '2026-11-03', count: 1 }],
    },
    completion: {
      metrics: [
        metric('in_progress', { drill: 'workspaces' }),
        metric('stuck_rate', { value: 0, unit: 'percent' }),
        metric('first_pass_rate', { value: 100, unit: 'percent' }),
        metric('avg_rounds', { value: 2, unit: 'rounds' }),
        metric('fake_completion_rate', { value: 0, unit: 'percent' }),
        metric('interventions', { value: 0, drill: 'workspaces' }),
        metric('today_tokens', { value: 1200, unit: 'tokens', drill: 'tokens' }),
        metric('today_cost', { value: 0.42, unit: 'usd', drill: 'tokens' }),
      ],
    },
    ...over,
  }
}

function renderPanels() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MissionControlPanels ownerParam={null} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('MissionControlPanels', () => {
  it('renders two independent panels and never shows 0 for missing data', async () => {
    vi.mocked(missionControlApi.get).mockResolvedValue(view())
    renderPanels()

    await waitFor(() => screen.getByText('missionControl.metrics.pending'))

    // Both panel headings are present in one render — the panels are siblings,
    // not alternatives.
    expect(screen.getByText('missionControl.intakeTitle')).toBeTruthy()
    expect(screen.getByText('missionControl.completionTitle')).toBeTruthy()

    // No-data tile shows the em-dash key, not a 0.
    const pendingTile = screen.getByText('missionControl.metrics.pending').closest('a') as HTMLElement
    expect(pendingTile.textContent).toContain('missionControl.noData')
    expect(pendingTile.textContent).not.toContain('missionControl.metrics.pending0')
  })

  it('marks abnormal metrics with both warning text and tone', async () => {
    vi.mocked(missionControlApi.get).mockResolvedValue(view())
    renderPanels()

    await waitFor(() => screen.getByText('missionControl.warning'))
    const misfire = screen.getByText('missionControl.metrics.misfire_rate').closest('a') as HTMLElement
    expect(misfire.textContent).toContain('missionControl.warning')
    expect(misfire.querySelector('.text-warning')).toBeTruthy()
  })

  it('drills each tile to its page and per-source rows to the filtered queue', async () => {
    vi.mocked(missionControlApi.get).mockResolvedValue(view())
    renderPanels()

    await waitFor(() => screen.getByText('missionControl.perSource'))
    const intake = screen.getByText('missionControl.metrics.today_accepted').closest('a') as HTMLElement
    expect(intake.getAttribute('href')).toBe('/intake')

    const workspaces = screen.getByText('missionControl.metrics.in_progress').closest('a') as HTMLElement
    expect(workspaces.getAttribute('href')).toBe('/workspaces')

    const tokens = screen.getByText('missionControl.metrics.today_tokens').closest('a') as HTMLElement
    expect(tokens.getAttribute('href')).toBe('#owner-token-trend')

    // The per-source row carries ?source=<kind> into the calibration queue.
    const sourceRows = screen.getAllByText(/im_bot/)
    const row = sourceRows.find((el) => el.closest('a')?.getAttribute('data-search'))
    expect(row).toBeTruthy()
    expect(row!.closest('a')!.getAttribute('data-search')).toContain('"source":"im_bot"')
  })

  it('toggles the global intake pause switch', async () => {
    vi.mocked(missionControlApi.get).mockResolvedValue(view())
    vi.mocked(missionControlApi.setPaused).mockResolvedValue({ paused: true, projects: 1 })
    renderPanels()

    const toggle = await waitFor(() => screen.getByRole('switch'))
    fireEvent.click(toggle)
    await waitFor(() =>
      expect(missionControlApi.setPaused).toHaveBeenCalledWith(true, null),
    )
  })

  it('renders the error state with a retry action', async () => {
    vi.mocked(missionControlApi.get).mockRejectedValue(new Error('boom'))
    renderPanels()
    // The component retries once before surfacing the error state.
    await waitFor(() => screen.getByText('missionControl.loadError'), { timeout: 4000 })
    expect(screen.getByText('missionControl.retry')).toBeTruthy()
  })
})
