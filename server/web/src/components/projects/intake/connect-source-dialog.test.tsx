import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { intakeApi, type IntakeSource, type IntakeTestResult } from '@/lib/intake-api'
import { integrationApi } from '@/lib/integration-api'
import { ConnectSourceDialog } from './connect-source-dialog'

vi.mock('@/lib/intake-api', () => ({
  intakeApi: {
    createSource: vi.fn(),
    testSource: vi.fn(),
  },
}))

vi.mock('@/lib/integration-api', () => ({
  integrationApi: {
    listCredentials: vi.fn(),
  },
}))

const createdSource: IntakeSource = {
  id: 7,
  project_id: 1,
  source_kind: 'webhook',
  name: 'deploy-hook',
  mode: 'push',
  provider: '',
  inbound_path: '/api/integrations/webhook/7',
  has_secret: true,
  status: 'ok',
  events_24h: 0,
  config: {},
}

const testResult: IntakeTestResult = {
  source_kind: 'webhook',
  event_kind: 'message',
  actor_ext_id: 'intake-test',
  thread_ext_id: '',
  text: 'Test delivery from deploy-hook',
  mode: 'observe',
  paused: false,
  strength: 'inferred',
  reason_code: 'observe_only',
  allowed: true,
}

function renderDialog(
  over: Partial<React.ComponentProps<typeof ConnectSourceDialog>> = {},
) {
  const onConnected = vi.fn()
  const onOpenChange = vi.fn()
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <ConnectSourceDialog
        projectId={1}
        sourceKind="webhook"
        open
        onOpenChange={onOpenChange}
        onConnected={onConnected}
        {...over}
      />
    </QueryClientProvider>,
  )
  return { onConnected, onOpenChange }
}

describe('ConnectSourceDialog', () => {
  it('creates a webhook binding with the chosen limits and shows the inbound URL', async () => {
    vi.mocked(intakeApi.createSource).mockResolvedValue({
      source: createdSource,
      secret_link: '/intake/sources/secret/tok',
    })

    const { onConnected } = renderDialog()

    fireEvent.change(screen.getByLabelText('名称'), { target: { value: 'deploy-hook' } })
    fireEvent.change(screen.getByLabelText('体积上限（KB）'), { target: { value: '2048' } })
    fireEvent.click(screen.getByRole('button', { name: '创建并接入' }))

    await waitFor(() => expect(intakeApi.createSource).toHaveBeenCalledTimes(1))
    expect(intakeApi.createSource).toHaveBeenCalledWith(1, {
      source_kind: 'webhook',
      name: 'deploy-hook',
      mode: 'push',
      config: { events: [], max_body_bytes: 2048 * 1024 },
    })
    expect(onConnected).toHaveBeenCalledTimes(1)

    expect(await screen.findByText('已接入「deploy-hook」')).toBeInTheDocument()
    expect(screen.getByLabelText('入站地址')).toHaveValue(
      `${window.location.origin}/api/integrations/webhook/7`,
    )
    expect(screen.getByRole('link', { name: '打开一次性密钥页' })).toHaveAttribute(
      'href',
      '/intake/sources/secret/tok',
    )
  })

  it('stays on the form and shows the server reason when the name is taken', async () => {
    vi.mocked(intakeApi.createSource).mockRejectedValue(
      new Error('an intake source with this name already exists'),
    )

    const { onConnected } = renderDialog()

    fireEvent.change(screen.getByLabelText('名称'), { target: { value: 'deploy-hook' } })
    fireEvent.click(screen.getByRole('button', { name: '创建并接入' }))

    expect(
      await screen.findByText('an intake source with this name already exists'),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '创建并接入' })).toBeInTheDocument()
    expect(onConnected).not.toHaveBeenCalled()
  })

  it('shows the normalized test delivery and its policy decision', async () => {
    vi.mocked(intakeApi.createSource).mockResolvedValue({ source: createdSource })
    vi.mocked(intakeApi.testSource).mockResolvedValue(testResult)

    renderDialog()

    fireEvent.change(screen.getByLabelText('名称'), { target: { value: 'deploy-hook' } })
    fireEvent.click(screen.getByRole('button', { name: '创建并接入' }))
    fireEvent.click(await screen.findByRole('button', { name: '测试投递' }))

    expect(await screen.findByText('会进入接活流程')).toBeInTheDocument()
    expect(intakeApi.testSource).toHaveBeenCalledWith(1, 7)
    expect(screen.getByText('observe_only')).toBeInTheDocument()
    expect(screen.getByText('推断')).toBeInTheDocument()
    expect(screen.getByText('Test delivery from deploy-hook')).toBeInTheDocument()
  })

  it('blocks an IMAP binding until an existing credential is chosen', async () => {
    vi.mocked(integrationApi.listCredentials).mockResolvedValue([])

    renderDialog({ sourceKind: 'email' })

    expect(await screen.findByText(/还没有 IMAP 凭据/)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('名称'), { target: { value: 'support-inbox' } })
    expect(screen.getByRole('button', { name: '创建并接入' })).toBeDisabled()
  })

  it('rejects a name the backend would refuse', () => {
    renderDialog()

    fireEvent.change(screen.getByLabelText('名称'), { target: { value: 'deploy hook!' } })

    expect(screen.getByText(/名称只能包含字母/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '创建并接入' })).toBeDisabled()
  })
})
