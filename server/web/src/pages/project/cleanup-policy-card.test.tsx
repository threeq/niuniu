import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ProjectCleanupPolicyCard } from './cleanup-policy-card'

vi.mock('@/lib/cleanup-api', () => ({
  cleanupApi: {
    getPolicy: vi.fn(() =>
      Promise.resolve({ enabled: true, inactive_days: 14, statuses: ['completed', 'not_started'] }),
    ),
    setPolicy: vi.fn(() =>
      Promise.resolve({ enabled: true, inactive_days: 14, statuses: ['completed', 'not_started'] }),
    ),
    runOnce: vi.fn(() => Promise.resolve({ project_id: 42, scanned: 3, deleted: [1, 2], skipped_changes: 0, errors: 0 })),
  },
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}))
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { cleanupApi } from '@/lib/cleanup-api'

function renderWithQc() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ProjectCleanupPolicyCard projectId={42} />
    </QueryClientProvider>,
  )
}

describe('ProjectCleanupPolicyCard', () => {
  beforeEach(() => vi.clearAllMocks())

  it('loads the project policy and saves it back verbatim', async () => {
    renderWithQc()
    await waitFor(() => expect(cleanupApi.getPolicy).toHaveBeenCalledWith(42))
    fireEvent.click(await screen.findByText('tabs.settings.cleanup.save'))
    await waitFor(() =>
      expect(cleanupApi.setPolicy).toHaveBeenCalledWith(42, {
        enabled: true,
        inactive_days: 14,
        statuses: ['completed', 'not_started'],
      }),
    )
  })

  it('drops a status when its checkbox is unticked', async () => {
    renderWithQc()
    await waitFor(() => expect(cleanupApi.getPolicy).toHaveBeenCalled())
    // Wait for the seeded enable=true render: 3 checkboxes (enable, completed,
    // not_started). Index 0 is the enable toggle; index 2 is not_started.
    await waitFor(() => expect(screen.getAllByRole('checkbox')).toHaveLength(3))
    const boxes = screen.getAllByRole('checkbox')
    fireEvent.click(boxes[2]) // untick not_started
    fireEvent.click(screen.getByText('tabs.settings.cleanup.save'))
    await waitFor(() =>
      expect(cleanupApi.setPolicy).toHaveBeenCalledWith(42, {
        enabled: true,
        inactive_days: 14,
        statuses: ['completed'],
      }),
    )
  })

  it('triggers a manual sweep via runOnce', async () => {
    renderWithQc()
    await waitFor(() => expect(cleanupApi.getPolicy).toHaveBeenCalled())
    fireEvent.click(await screen.findByText('tabs.settings.cleanup.runNow'))
    await waitFor(() => expect(cleanupApi.runOnce).toHaveBeenCalledWith(42))
  })
})
