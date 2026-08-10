import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import { AccountUsageCard } from './account-usage-card'
import * as api from '@/lib/api'
import type { AccountUsage } from '@/types/claude-usage'

vi.mock('@/lib/api')

afterEach(() => {
  vi.clearAllMocks()
})

function renderWithQuery(ui: ReactElement) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>)
}

const sampleUsage: AccountUsage = {
  source: 'live',
  fetched_at: new Date().toISOString(),
  next_refresh_after: new Date(Date.now() + 5000).toISOString(),
  shared: false,
  data_source_paths: ['/fake/dir'],
  windows: [
    {
      type: 'five_hour_billing',
      total_tokens: 100, input_tokens: 50, output_tokens: 50,
      cache_read_tokens: 0, cache_creation_tokens: 0, cost_usd: 0.5, models: [],
    },
    {
      type: 'seven_day_all',
      total_tokens: 1000, input_tokens: 500, output_tokens: 500,
      cache_read_tokens: 0, cache_creation_tokens: 0, cost_usd: 5, models: [],
    },
    {
      type: 'seven_day_sonnet',
      total_tokens: 0, input_tokens: 0, output_tokens: 0,
      cache_read_tokens: 0, cache_creation_tokens: 0, cost_usd: 0, models: [],
    },
    {
      type: 'seven_day_opus',
      total_tokens: 0, input_tokens: 0, output_tokens: 0,
      cache_read_tokens: 0, cache_creation_tokens: 0, cost_usd: 0, models: [],
    },
  ],
}

describe('<AccountUsageCard>', () => {
  it('renders four windows on success', async () => {
    vi.mocked(api.getClaudeAccountUsage).mockResolvedValue(sampleUsage)
    renderWithQuery(<AccountUsageCard accountId={5} />)
    await waitFor(() => expect(screen.getAllByText(/tokens/i).length).toBeGreaterThan(0))
    expect(api.getClaudeAccountUsage).toHaveBeenCalledWith(5, false)
  })

  it('renders empty-state guidance on projects_not_found', async () => {
    vi.mocked(api.getClaudeAccountUsage).mockRejectedValue({
      code: 'projects_not_found', message: 'no projects', hint_zh: '',
    })
    renderWithQuery(<AccountUsageCard accountId={5} />)
    await waitFor(() => expect(screen.getByText(/usage\.empty\.notLoggedInTitle|尚未记录任何用量|No usage recorded/i)).toBeInTheDocument())
  })

  it('renders walkFailed branch on jsonl_walk_failed', async () => {
    vi.mocked(api.getClaudeAccountUsage).mockRejectedValue({
      code: 'jsonl_walk_failed', message: 'permission denied', hint_zh: '扫描失败',
    })
    renderWithQuery(<AccountUsageCard accountId={5} />)
    await waitFor(() => expect(screen.getByText(/usage\.walkFailed|扫描会话日志失败|Failed to scan/i)).toBeInTheDocument())
  })

  it('does not call api when accountId is 0 (guard)', () => {
    vi.mocked(api.getClaudeAccountUsage).mockResolvedValue(sampleUsage)
    renderWithQuery(<AccountUsageCard accountId={0} />)
    expect(api.getClaudeAccountUsage).not.toHaveBeenCalled()
  })
})
