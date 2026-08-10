import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { it, expect, vi, beforeEach } from 'vitest'
import { ColumnExtensionDialog } from './column-extension-dialog'
import { columnExtensionApi, type ColumnExtension } from '@/lib/project-template-api'

vi.mock('@/lib/project-template-api')

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const baseColumn: ColumnExtension = {
  id: 7,
  project_id: 3,
  name: '实现',
  position: 1,
  lifecycle_mapping: 'implement',
  reviewer_agent: null,
  phase_prompt: '老指令',
  auto_advance: 1,
  gate_specs: [],
  op_primitive: 'instruct',
  when_to_use: '需要写代码时',
}

beforeEach(() => vi.clearAllMocks())

it('saves op_primitive + op_instruction + when_to_use; blank when_to_use -> null', async () => {
  const updateMock = vi.mocked(columnExtensionApi.update).mockResolvedValue({ ...baseColumn })
  // Radix Dialog renders into a portal on document.body, so query the document.
  wrap(<ColumnExtensionDialog open column={baseColumn} projectId={3} onOpenChange={() => {}} />)

  const wtu = document.getElementById('when-to-use') as HTMLTextAreaElement
  fireEvent.change(wtu, { target: { value: '   ' } })
  // zh-CN test env: common:actions.save renders as "保存"
  fireEvent.click(screen.getByRole('button', { name: /保存/ }))

  await waitFor(() =>
    expect(updateMock).toHaveBeenCalledWith(7, {
      op_primitive: 'instruct',
      op_instruction: '老指令',
      when_to_use: null,
    }),
  )
})

it('hides op_instruction when primitive is none', () => {
  const noneCol: ColumnExtension = { ...baseColumn, op_primitive: 'none', phase_prompt: null }
  wrap(<ColumnExtensionDialog open column={noneCol} projectId={3} onOpenChange={() => {}} />)
  expect(document.getElementById('op-instruction')).toBeNull()
  // when_to_use stays available for every primitive
  expect(document.getElementById('when-to-use')).not.toBeNull()
})
