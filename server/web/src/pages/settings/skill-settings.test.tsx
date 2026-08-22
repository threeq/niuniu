import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { SkillSettings } from './skill-settings'
import { skillApi, getWorkspacesOverview } from '@/lib/api'
import type { SkillInfo } from '@/types/api'

// The test setup (src/i18n/test-setup.ts) forces zh-CN, so assertions match
// the zh-CN strings under the `skills` key in settings.json.
vi.mock('@/lib/api', () => ({
  skillApi: {
    list: vi.fn(),
    install: vi.fn(),
    enable: vi.fn(),
    disable: vi.fn(),
    update: vi.fn(),
    uninstall: vi.fn(),
  },
  getWorkspacesOverview: vi.fn(),
}))

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

function skill(over: Partial<SkillInfo> = {}): SkillInfo {
  return {
    name: 'site-audit',
    description: '技术 SEO 审计',
    version: '1.0.0',
    source: 'builtin',
    global_installed: true,
    installed: [],
    ...over,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(skillApi.list).mockResolvedValue([])
  vi.mocked(getWorkspacesOverview).mockResolvedValue({
    summary: {
      total_count: 1,
      active_count: 1,
      stuck_count: 0,
      user_message_count: 0,
      ai_message_count: 0,
      input_tokens: 0,
      output_tokens: 0,
      cache_creation_tokens: 0,
      cache_read_tokens: 0,
    },
    workspaces: [
      {
        workspace_id: 7,
        name: 'ws-7',
        owner_type: 'user',
        owner_id: 1,
        owner: { type: 'user', id: 1 } as never,
        status: 'active' as never,
        session_status: '',
        updated_at: '',
        message_count: 0,
        user_message_count: 0,
        ai_message_count: 0,
        input_tokens: 0,
        output_tokens: 0,
        cache_creation_tokens: 0,
        cache_read_tokens: 0,
      },
    ],
  })
})

describe('SkillSettings', () => {
  it('renders the catalog rows with per-agent enable switches', async () => {
    vi.mocked(skillApi.list).mockResolvedValue([
      skill({ installed: [{ agent: 'claude', scope: 'global', managed: true }] }),
      skill({
        name: 'document-skills',
        description: '文档技能包',
        source: 'marketplace',
        plugin_source: 'document-skills@anthropic-agent-skills',
        global_installed: false,
      }),
      skill({
        name: 'my-own-skill',
        source: 'user',
        global_installed: false,
        installed: [{ agent: 'claude', scope: 'global', managed: false }],
      }),
    ])

    wrap(<SkillSettings />)

    expect(await screen.findByText('site-audit')).toBeTruthy()
    expect(screen.getByText('document-skills')).toBeTruthy()
    expect(screen.getByText('my-own-skill')).toBeTruthy()
    // Source badges.
    expect(screen.getByText('内置')).toBeTruthy()
    expect(screen.getByText('市场')).toBeTruthy()
    expect(screen.getByText('用户')).toBeTruthy()

    // Agent column headers for the matrix.
    for (const agent of ['claude', 'codex', 'qwen', 'omp', 'goose']) {
      expect(screen.getByRole('columnheader', { name: agent })).toBeTruthy()
    }
  })

  it('flips a per-agent switch through the enable API', async () => {
    vi.mocked(skillApi.list).mockResolvedValue([skill()])
    vi.mocked(skillApi.enable).mockResolvedValue({
      results: [{ target: { agent: 'claude', scope: 'global' }, ok: true }],
    })

    wrap(<SkillSettings />)
    const sw = await screen.findByRole('switch', { name: 'site-audit claude' })
    fireEvent.click(sw)

    await waitFor(() =>
      expect(skillApi.enable).toHaveBeenCalledWith({
        name: 'site-audit',
        workspace_id: undefined,
        targets: [{ agent: 'claude', scope: 'global' }],
      }),
    )
  })

  it('disables switches for marketplace skills on non-claude agents', async () => {
    vi.mocked(skillApi.list).mockResolvedValue([
      skill({
        name: 'document-skills',
        source: 'marketplace',
        plugin_source: 'document-skills@anthropic-agent-skills',
      }),
    ])

    wrap(<SkillSettings />)
    const goose = await screen.findByRole('switch', { name: 'document-skills goose' })
    expect((goose as HTMLButtonElement).disabled).toBe(true)
    const claude = screen.getByRole('switch', { name: 'document-skills claude' })
    expect((claude as HTMLButtonElement).disabled).toBe(false)
  })

  it('offers 安装 for skills not yet in the store and calls the install API', async () => {
    vi.mocked(skillApi.list).mockResolvedValue([skill({ global_installed: false })])
    vi.mocked(skillApi.install).mockResolvedValue({ ok: true })

    wrap(<SkillSettings />)
    const btn = await screen.findByRole('button', { name: /安装/ })
    fireEvent.click(btn)

    await waitFor(() => expect(skillApi.install).toHaveBeenCalledWith('site-audit'))
  })
})
