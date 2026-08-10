import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { OrchestrationSettings } from './orchestration-settings'

vi.mock('@/lib/api', () => ({
  adminSettingsApi: {
    get: vi.fn((key: string) => Promise.resolve({ key, value: '20' })),
    put: vi.fn(() => Promise.resolve({ key: 'x', value: '20' })),
  },
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}))

import { adminSettingsApi } from '@/lib/api'

function renderWithQc() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <OrchestrationSettings />
    </QueryClientProvider>,
  )
}

describe('OrchestrationSettings', () => {
  beforeEach(() => vi.clearAllMocks())

  it('loads four keys then saves all with four PUTs', async () => {
    renderWithQc()
    await waitFor(() => expect(adminSettingsApi.get).toHaveBeenCalledTimes(4))
    const btn = await screen.findByText('orchestration.saveAll')
    fireEvent.click(btn)
    await waitFor(() => expect(adminSettingsApi.put).toHaveBeenCalledTimes(4))
  })
})
