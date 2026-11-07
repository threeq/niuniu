import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { toast } from 'sonner'
import { confirm } from '@/lib/confirm'
import { intakeApi, type IntakeSource, type IntakeTestResult } from '@/lib/intake-api'
import { ReachabilityHint, SourceBindings } from './source-bindings'

vi.mock('@/lib/intake-api', () => ({
  intakeApi: {
    testSource: vi.fn(),
    rotateSource: vi.fn(),
    pollSource: vi.fn(),
    deleteSource: vi.fn(),
  },
}))

vi.mock('@/lib/confirm', () => ({
  confirm: vi.fn(),
}))

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

function makeSource(over: Partial<IntakeSource> = {}): IntakeSource {
  return {
    id: 1,
    project_id: 1,
    source_kind: 'webhook',
    name: 'hooks',
    mode: 'push',
    provider: '',
    inbound_path: '/api/integrations/webhook/1',
    has_secret: true,
    status: 'ok',
    events_24h: 0,
    config: {},
    ...over,
  }
}

const testResult: IntakeTestResult = {
  source_kind: 'webhook',
  event_kind: 'message',
  actor_ext_id: 'intake-test',
  thread_ext_id: '',
  text: 'Test delivery',
  mode: 'observe',
  paused: false,
  strength: 'inferred',
  reason_code: 'explicit_request',
  allowed: true,
}

function renderBindings(bindings: IntakeSource[], loading = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <SourceBindings projectId={1} bindings={bindings} loading={loading} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.mocked(confirm).mockResolvedValue(true)
})

describe('SourceBindings', () => {
  it('lists name, mode, provider, last delivery, 24h events and status', () => {
    renderBindings([
      makeSource({
        id: 1,
        name: 'deploy-hook',
        provider: 'gitlab',
        last_event_at: '2026-11-06T10:00:00Z',
        events_24h: 3,
      }),
      makeSource({
        id: 2,
        source_kind: 'email',
        name: 'support-inbox',
        mode: 'imap',
        provider: '',
        inbound_path: '/api/integrations/email/2',
        status: 'backoff',
        last_error: 'IMAP: authentication failed',
        backoff_until: '2026-11-07T10:00:00Z',
        events_24h: 7,
      }),
    ])

    expect(screen.getByText('deploy-hook')).toBeInTheDocument()
    expect(screen.getByText('正常')).toBeInTheDocument()
    expect(screen.getByText(/推送 · GitLab|推送 · gitlab/)).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()

    expect(screen.getByText('support-inbox')).toBeInTheDocument()
    expect(screen.getByText('退避中')).toBeInTheDocument()
    expect(screen.getByText('IMAP 轮询 · 通用')).toBeInTheDocument()
    expect(screen.getByText('7')).toBeInTheDocument()
    expect(screen.queryByText('IMAP: authentication failed')).not.toBeInTheDocument()
  })

  it('marks a binding that never delivered as never', () => {
    renderBindings([makeSource({ id: 3, source_kind: 'ci', name: 'jenkins', provider: 'jenkins' })])

    expect(screen.getAllByText('从未投递').length).toBeGreaterThan(0)
  })

  it('expands the latest error and the backoff time', () => {
    renderBindings([
      makeSource({
        id: 2,
        source_kind: 'email',
        name: 'support-inbox',
        mode: 'imap',
        inbound_path: '/api/integrations/email/2',
        status: 'error',
        last_error: 'IMAP: authentication failed',
        backoff_until: '2026-11-07T10:00:00Z',
      }),
    ])

    fireEvent.click(screen.getByRole('button', { name: '展开最近错误' }))

    expect(screen.getByText('IMAP: authentication failed')).toBeInTheDocument()
    expect(screen.getByText(/恢复轮询/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '收起错误' })).toBeInTheDocument()
  })

  it('runs a test delivery and shows the policy verdict', async () => {
    vi.mocked(intakeApi.testSource).mockResolvedValue(testResult)

    renderBindings([makeSource({ id: 9 })])
    fireEvent.click(screen.getByRole('button', { name: '测试投递' }))

    expect(await screen.findByText('会进入接活流程')).toBeInTheDocument()
    expect(intakeApi.testSource).toHaveBeenCalledWith(1, 9)
    expect(screen.getByText(/事件 message · 判定 explicit_request/)).toBeInTheDocument()
  })

  it('rotates the secret only after confirmation and shows the one-time link', async () => {
    vi.mocked(intakeApi.rotateSource).mockResolvedValue({
      source: makeSource({ id: 9 }),
      secret_link: '/intake/sources/secret/tok',
    })

    renderBindings([makeSource({ id: 9 })])
    fireEvent.click(screen.getByRole('button', { name: '轮换密钥' }))

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(await screen.findByRole('link', { name: '打开一次性密钥页' })).toHaveAttribute(
      'href',
      '/intake/sources/secret/tok',
    )
    expect(intakeApi.rotateSource).toHaveBeenCalledWith(1, 9)
  })

  it('does not rotate when the confirmation is dismissed', () => {
    vi.mocked(confirm).mockResolvedValue(false)

    renderBindings([makeSource({ id: 9 })])
    fireEvent.click(screen.getByRole('button', { name: '轮换密钥' }))

    expect(intakeApi.rotateSource).not.toHaveBeenCalled()
  })

  it('offers poll only for IMAP bindings and triggers one immediately', async () => {
    vi.mocked(intakeApi.pollSource).mockResolvedValue({ ok: true, polled: true })

    renderBindings([
      makeSource({
        id: 4,
        source_kind: 'email',
        name: 'support-inbox',
        mode: 'imap',
        inbound_path: '/api/integrations/email/4',
      }),
    ])
    fireEvent.click(screen.getByRole('button', { name: '立即轮询' }))

    await waitFor(() => expect(intakeApi.pollSource).toHaveBeenCalledWith(1, 4))
    expect(toast.success).toHaveBeenCalled()
  })

  it('deletes a binding after confirmation', async () => {
    vi.mocked(intakeApi.deleteSource).mockResolvedValue({ ok: true })

    renderBindings([makeSource({ id: 9, name: 'deploy-hook' })])
    fireEvent.click(screen.getByRole('button', { name: '删除绑定 deploy-hook' }))

    expect(confirm).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(intakeApi.deleteSource).toHaveBeenCalledWith(1, 9))
    expect(toast.success).toHaveBeenCalled()
  })

  it('does not delete when the confirmation is dismissed', () => {
    vi.mocked(confirm).mockResolvedValue(false)

    renderBindings([makeSource({ id: 9, name: 'deploy-hook' })])
    fireEvent.click(screen.getByRole('button', { name: '删除绑定 deploy-hook' }))

    expect(intakeApi.deleteSource).not.toHaveBeenCalled()
  })

  it('shows an empty state when the source has no bindings', () => {
    renderBindings([])
    expect(screen.getByText('还没有绑定')).toBeInTheDocument()
  })
})

describe('SourceBindings reachability', () => {
  it('warns that a loopback inbound address is only reachable locally', () => {
    render(<ReachabilityHint origin="http://localhost:5173" />)

    expect(screen.getByText('可达性提示')).toBeInTheDocument()
    expect(screen.getByText(/只有本机能访问/)).toBeInTheDocument()
    expect(screen.getByText(/本机\/局域网地址、自备隧道/)).toBeInTheDocument()
  })

  it('warns that a private LAN address is not reachable by public CI', () => {
    render(<ReachabilityHint origin="http://192.168.1.20:3000" />)

    expect(screen.getByText(/仅同一局域网内可达/)).toBeInTheDocument()
  })

  it('only reminds about the reverse proxy for a public origin', () => {
    render(<ReachabilityHint origin="https://ci.example.com" />)

    expect(screen.getByText(/看起来是公网地址/)).toBeInTheDocument()
    expect(screen.queryByText(/本机\/局域网地址、自备隧道/)).not.toBeInTheDocument()
  })

  it('is not rendered for an IMAP-only binding', () => {
    renderBindings([
      makeSource({
        id: 4,
        source_kind: 'email',
        name: 'support-inbox',
        mode: 'imap',
        inbound_path: '/api/integrations/email/4',
      }),
    ])

    expect(screen.queryByText('可达性提示')).not.toBeInTheDocument()
  })
})
