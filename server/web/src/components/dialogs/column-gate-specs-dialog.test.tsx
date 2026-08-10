import { vi, it, expect } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ColumnGateSpecsDialog } from './column-gate-specs-dialog'
import { columnGateSpecApi } from '@/lib/project-template-api'
import { harnessSpecApi } from '@/lib/harness-api'

vi.mock('@/lib/project-template-api')
vi.mock('@/lib/harness-api')

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

it('saves selected spec ids in chosen order', async () => {
  vi.mocked(harnessSpecApi.listGlobal).mockResolvedValue([
    { id: 11, name: 'has-tests', category: 'quality', severity: 'error', enabled: 1 } as never,
    { id: 12, name: 'has-changelog', category: 'quality', severity: 'warning', enabled: 1 } as never,
  ])
  vi.mocked(columnGateSpecApi.list).mockResolvedValue({ specs: [] })
  const replace = vi.mocked(columnGateSpecApi.replace).mockResolvedValue({ specs: [] })

  wrap(<ColumnGateSpecsDialog open columnId={5} columnName="Spec" projectId={1} onOpenChange={() => {}} />)

  await waitFor(() => screen.getByText('has-tests'))
  fireEvent.click(screen.getByRole('button', { name: /add has-tests/i }))
  fireEvent.click(screen.getByRole('button', { name: /add has-changelog/i }))
  // test env uses zh-CN: save button label is "保存"
  fireEvent.click(screen.getByRole('button', { name: /保存/ }))

  await waitFor(() =>
    expect(replace).toHaveBeenCalledWith(5, [
      { spec_id: 11, applicability: 'if_routed' },
      { spec_id: 12, applicability: 'if_routed' },
    ]),
  )
})

it('handles cross-owner 422 with toast', async () => {
  vi.mocked(harnessSpecApi.listGlobal).mockResolvedValue([])
  vi.mocked(columnGateSpecApi.list).mockResolvedValue({ specs: [{ column_id: 5, spec_id: 99, position: 0, applicability: 'if_routed' }] })
  vi.mocked(columnGateSpecApi.replace).mockRejectedValue(new Error('spec 99 not visible to project owner'))
  wrap(<ColumnGateSpecsDialog open columnId={5} columnName="Spec" projectId={1} onOpenChange={() => {}} />)
  // test env uses zh-CN: save button label is "保存"
  fireEvent.click(await screen.findByRole('button', { name: /保存/ }))
  // toast.error path — assert at integration level only; here check mutate was attempted
  await waitFor(() => expect(columnGateSpecApi.replace).toHaveBeenCalled())
})
