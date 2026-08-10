import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import { AccountRow } from './account-row'
import * as api from '@/lib/api'
import type { ClaudeAccount } from '@/types/api'

vi.mock('@/lib/api')

afterEach(() => {
  vi.clearAllMocks()
})

function renderWithQuery(ui: ReactElement) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>)
}

const account: ClaudeAccount = {
  id: 7,
  name: 'test-account',
  email: 'foo@example.com',
  config_dir: '/some/dir',
  visibility: 'public',
  status: 'pending',
  created_at: 0,
  created_by: 1,
  last_used_at: undefined,
}

const noopHandlers = {
  isAdmin: true,
  callerID: 1,
  personalMode: true,
  onLogin: vi.fn(),
  onRename: vi.fn(),
  onToggleVisibility: vi.fn(),
  onDelete: vi.fn(),
  onRefresh: vi.fn(),
}

describe('<AccountRow> expand toggle', () => {
  it('does not call usage api when row is collapsed by default', () => {
    vi.mocked(api.getClaudeAccountUsage).mockResolvedValue({
      source: 'live', fetched_at: '', next_refresh_after: '',
      shared: false, data_source_paths: [], windows: [],
    })
    renderWithQuery(<AccountRow account={account} {...noopHandlers} />)
    expect(api.getClaudeAccountUsage).not.toHaveBeenCalled()
  })

  it('mounts AccountUsageCard and calls api after chevron click', async () => {
    vi.mocked(api.getClaudeAccountUsage).mockResolvedValue({
      source: 'live', fetched_at: '', next_refresh_after: '',
      shared: false, data_source_paths: ['/x'], windows: [],
    })
    renderWithQuery(<AccountRow account={account} {...noopHandlers} />)

    // The chevron toggle button. aria-label resolves to either translation
    // (if i18n initialized) or raw key (otherwise).
    const toggle = screen.getByRole('button', { name: /claudeAccounts\.expand|展开用量|Show usage/i })
    await userEvent.click(toggle)

    await waitFor(() => {
      expect(api.getClaudeAccountUsage).toHaveBeenCalledWith(7, false)
    })
  })

  it('unmounts AccountUsageCard on collapse (second chevron click)', async () => {
    vi.mocked(api.getClaudeAccountUsage).mockResolvedValue({
      source: 'live', fetched_at: '', next_refresh_after: '',
      shared: false, data_source_paths: ['/x'], windows: [],
    })
    renderWithQuery(<AccountRow account={account} {...noopHandlers} />)

    const expandBtn = screen.getByRole('button', { name: /claudeAccounts\.expand|展开用量|Show usage/i })
    await userEvent.click(expandBtn)
    await waitFor(() => expect(api.getClaudeAccountUsage).toHaveBeenCalled())

    // After expansion the same button should now be the collapse button.
    const collapseBtn = screen.getByRole('button', { name: /claudeAccounts\.collapse|收起用量|Hide usage/i })
    await userEvent.click(collapseBtn)

    // After collapse, the expand button is back.
    expect(screen.getByRole('button', { name: /claudeAccounts\.expand|展开用量|Show usage/i })).toBeInTheDocument()
  })
})
