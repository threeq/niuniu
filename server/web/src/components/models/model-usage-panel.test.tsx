import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ModelUsagePanel } from './model-usage-panel'
import * as modelApi from '@/lib/model-api'
import type { ModelUsageSummary } from '@/types/model'

vi.mock('@/lib/model-api', () => ({
  getUsage: vi.fn(),
}))

const total: ModelUsageSummary = {
  total_tokens: 1200,
  requests: 12,
}

const byModel: ModelUsageSummary = {
  total_tokens: 1200,
  requests: 12,
  by_model: [
    { key: 'glm-4', total_tokens: 900, requests: 9 },
    { key: 'gpt-5', total_tokens: 300, requests: 3 },
  ],
}

const byCli: ModelUsageSummary = {
  total_tokens: 1200,
  requests: 12,
  by_cli: [{ key: 'claude', total_tokens: 1200, requests: 12 }],
}

function renderPanel() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <ModelUsagePanel />
    </QueryClientProvider>
  )
}

describe('ModelUsagePanel', () => {
  beforeEach(() => {
    vi.mocked(modelApi.getUsage).mockImplementation(async (groupBy) => {
      if (groupBy === 'cli') return byCli
      if (groupBy) return byModel
      return total
    })
  })

  it('shows the total and the per-model breakdown', async () => {
    renderPanel()

    await screen.findByText('glm-4')
    expect(screen.getByText('1,200')).toBeInTheDocument()
    expect(screen.getByText('gpt-5')).toBeInTheDocument()
    expect(modelApi.getUsage).toHaveBeenCalled()
  })

  it('switches the breakdown dimension to CLI', async () => {
    renderPanel()
    await screen.findByText('glm-4')

    fireEvent.click(screen.getByRole('tab', { name: 'CLI' }))

    await waitFor(() => expect(screen.getByText('claude')).toBeInTheDocument())
    expect(screen.queryByText('glm-4')).not.toBeInTheDocument()
  })
})
