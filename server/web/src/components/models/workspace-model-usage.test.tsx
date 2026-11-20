import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { WorkspaceModelUsage } from './workspace-model-usage'
import * as modelApi from '@/lib/model-api'
import type { ModelBudget, ModelUsageSummary } from '@/types/model'

vi.mock('@/lib/model-api', () => ({
  getBudget: vi.fn(),
  getUsage: vi.fn(),
}))

const usage: ModelUsageSummary = {
  total_tokens: 500,
  requests: 5,
  by_workspace: [{ key: '77', total_tokens: 500, requests: 5 }],
}

function budget(monthlyTokens: number, usedTokens: number): ModelBudget {
  return {
    owner_type: 'user',
    owner_id: 2,
    monthly_tokens: monthlyTokens,
    used_tokens: usedTokens,
    period: '2026-11',
    projects: [],
  }
}

function renderCard() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <WorkspaceModelUsage workspaceId="77" model="sonnet" />
    </QueryClientProvider>
  )
}

describe('WorkspaceModelUsage', () => {
  beforeEach(() => {
    vi.mocked(modelApi.getUsage).mockResolvedValue(usage)
    vi.mocked(modelApi.getBudget).mockResolvedValue(budget(1000, 850))
  })

  it('shows the current model, this workspace month tokens and budget status', async () => {
    renderCard()

    await screen.findByText('sonnet')
    expect(await screen.findByText('500')).toBeInTheDocument()
    // 850 / 1000 = 85% → near cap
    expect(await screen.findByText(/85%/)).toBeInTheDocument()
  })

  it('shows unlimited when the owner has no cap', async () => {
    vi.mocked(modelApi.getBudget).mockResolvedValue(budget(0, 850))
    renderCard()
    await screen.findByText('sonnet')
    expect(await screen.findByText('不限')).toBeInTheDocument()
  })
})
