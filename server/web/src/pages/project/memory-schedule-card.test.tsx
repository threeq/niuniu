import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ProjectMemoryScheduleCard } from './memory-schedule-card'

vi.mock('@/lib/memory-api', () => ({
  memoryApi: {
    getSchedule: vi.fn(() => Promise.resolve({ cron: '33 3 3 * *' })),
    setSchedule: vi.fn(() => Promise.resolve({ cron: '33 3 3 * *' })),
  },
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}))

import { memoryApi } from '@/lib/memory-api'

function renderWithQc() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ProjectMemoryScheduleCard projectId={42} />
    </QueryClientProvider>,
  )
}

describe('ProjectMemoryScheduleCard', () => {
  beforeEach(() => vi.clearAllMocks())

  it('loads the project schedule (default monthly day 3 03:33) and saves it back', async () => {
    renderWithQc()
    await waitFor(() => expect(memoryApi.getSchedule).toHaveBeenCalledWith(42))
    fireEvent.click(await screen.findByText('tabs.settings.memory.save'))
    await waitFor(() =>
      expect(memoryApi.setSchedule).toHaveBeenCalledWith(42, '33 3 3 * *'),
    )
  })

  it('rebuilds the cron when the frequency changes to daily', async () => {
    renderWithQc()
    await waitFor(() => expect(memoryApi.getSchedule).toHaveBeenCalled())
    const freq = (await screen.findAllByRole('combobox'))[0]
    fireEvent.change(freq, { target: { value: 'daily' } })
    fireEvent.click(screen.getByText('tabs.settings.memory.save'))
    await waitFor(() =>
      expect(memoryApi.setSchedule).toHaveBeenCalledWith(42, '33 3 * * *'),
    )
  })
})
