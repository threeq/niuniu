import { describe, it, expect, vi, afterEach } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { SystemDepsSettings, ToolCard } from './system-deps-settings'
import { openExternalSmart } from '@/lib/shell'
import { api } from '@/lib/api'
import type { SystemDepsInfo, ToolStatus } from '@/types/api'

vi.mock('@/lib/shell', () => ({
  openClaudeLoginTerminal: vi.fn(),
  openCodexLoginTerminal: vi.fn(),
  openExternalSmart: vi.fn(() => Promise.resolve()),
}))

// Partial mock: only the three system-deps endpoints are stubbed so the
// page-level install test can drive the SSE stream deterministically; the rest
// of the api surface stays real for transitive importers (config store, gate).
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api')>()
  return {
    ...actual,
    api: {
      ...actual.api,
      getSystemDeps: vi.fn(),
      startSystemDepsInstall: vi.fn(),
      systemDepsInstallStreamUrl: vi.fn((id: string) => `/api/system-deps/install/stream?id=${id}`),
    },
  }
})

function info(over: Partial<SystemDepsInfo> = {}): SystemDepsInfo {
  return {
    platform: 'darwin',
    package_manager: 'brew',
    can_install: true,
    personal_mode: true,
    browser_cli_login: false,
    tools: [],
    ...over,
  }
}

// The ToolStatus.name union in types/api.ts predates the optional tools the
// backend probe already emits (tesseract/cairosvg, and now ffmpeg), so accept
// any string here and cast at the fixture boundary.
function tool(over: Omit<Partial<ToolStatus>, 'name'> & { name?: string } = {}): ToolStatus {
  return { name: 'node', found: true, version: 'v20.11.0', path: '/usr/bin/node', installable: true, ...over } as ToolStatus
}

function ffmpeg(over: Partial<ToolStatus> = {}): ToolStatus {
  return tool({ name: 'ffmpeg', found: false, version: '', path: '', installable: true, ...over })
}

describe('ToolCard', () => {
  it('shows version and 重新检测 when tool is found', () => {
    render(
      <ToolCard
        tool={tool()}
        info={info()}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.getByText('v20.11.0')).toBeTruthy()
    expect(screen.getByRole('button', { name: '重新检测' })).toBeTruthy()
  })

  it('shows 一键安装 + 重新检测 + download link when missing and can_install', () => {
    const onRefresh = vi.fn()
    render(
      <ToolCard
        tool={tool({ name: 'python3', found: false, version: '', path: '' })}
        info={info()}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={onRefresh}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.getByRole('button', { name: '一键安装' })).toBeTruthy()
    expect(screen.getByText('手动下载 →')).toBeTruthy()
    const recheck = screen.getByRole('button', { name: '重新检测' }) as HTMLButtonElement
    expect(recheck).toBeTruthy()
    recheck.click()
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })

  it('routes 手动下载 through the OS browser shell in personal mode (webview swallows target=_blank)', () => {
    vi.mocked(openExternalSmart).mockClear()
    render(
      <ToolCard
        tool={tool({ name: 'node', found: false, version: '', path: '' })}
        info={info({ personal_mode: true })}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    const link = screen.getByText('手动下载 →') as HTMLAnchorElement
    link.click()
    expect(openExternalSmart).toHaveBeenCalledWith('https://nodejs.org/', true)
  })

  it('shows 重新检测 even when tool is not installable (team edition / manual install path)', () => {
    const onRefresh = vi.fn()
    render(
      <ToolCard
        tool={tool({ name: 'git', found: false, version: '', path: '', installable: false })}
        info={info({ can_install: false, package_manager: '' })}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={onRefresh}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.queryByRole('button', { name: '一键安装' })).toBeNull()
    const recheck = screen.getByRole('button', { name: '重新检测' }) as HTMLButtonElement
    expect(recheck).toBeTruthy()
    recheck.click()
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })

  it('shows 检测中… and disables 重新检测 while a refresh is in flight (found row)', () => {
    const onRefresh = vi.fn()
    render(
      <ToolCard
        tool={tool()}
        info={info()}
        installing={null}
        loginPending={false}
        isRefreshing={true}
        onInstall={() => {}}
        onRefresh={onRefresh}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.queryByRole('button', { name: '重新检测' })).toBeNull()
    const refreshing = screen.getByRole('button', { name: '检测中…' }) as HTMLButtonElement
    expect(refreshing.disabled).toBe(true)
    refreshing.click()
    expect(onRefresh).not.toHaveBeenCalled()
  })

  it('shows 检测中… and disables 重新检测 while a refresh is in flight (not-installed row)', () => {
    const onRefresh = vi.fn()
    render(
      <ToolCard
        tool={tool({ name: 'python3', found: false, version: '', path: '' })}
        info={info()}
        installing={null}
        loginPending={false}
        isRefreshing={true}
        onInstall={() => {}}
        onRefresh={onRefresh}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.queryByRole('button', { name: '重新检测' })).toBeNull()
    const refreshing = screen.getByRole('button', { name: '检测中…' }) as HTMLButtonElement
    expect(refreshing.disabled).toBe(true)
    refreshing.click()
    expect(onRefresh).not.toHaveBeenCalled()
  })

  it('disables 重新检测 on a not-installed row while its own install is in progress', () => {
    const onRefresh = vi.fn()
    render(
      <ToolCard
        tool={tool({ name: 'python3', found: false, version: '', path: '' })}
        info={info()}
        installing={'python3'}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={onRefresh}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    const recheck = screen.getByRole('button', { name: '重新检测' }) as HTMLButtonElement
    expect(recheck.disabled).toBe(true)
    recheck.click()
    expect(onRefresh).not.toHaveBeenCalled()
  })

  it('hides 一键安装 when tool is not installable (team edition)', () => {
    render(
      <ToolCard
        tool={tool({ name: 'git', found: false, version: '', path: '', installable: false })}
        info={info({ can_install: false, package_manager: '' })}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.queryByRole('button', { name: '一键安装' })).toBeNull()
    expect(screen.getByText('手动下载 →')).toBeTruthy()
  })

  it('disables 一键安装 for claude when node is missing', () => {
    const onInstall = vi.fn()
    render(
      <ToolCard
        tool={tool({ name: 'claude', found: false, version: '', path: '' })}
        info={info()}
        installing={null}
        loginPending={false}
        onInstall={onInstall}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        nodeFound={false}
      />,
    )
    const btn = screen.getByRole('button', { name: '一键安装' }) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    btn.click()
    expect(onInstall).not.toHaveBeenCalled()
  })

  it('shows Claude 登录 when claude is found in personal mode and triggers callback', () => {
    const onClaudeLogin = vi.fn()
    render(
      <ToolCard
        tool={tool({ name: 'claude', version: '2.1.119', path: '/usr/local/bin/claude' })}
        info={info()}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={onClaudeLogin}
        nodeFound={true}
      />,
    )
    const btn = screen.getByRole('button', { name: 'Claude 登录' }) as HTMLButtonElement
    expect(btn).toBeTruthy()
    btn.click()
    expect(onClaudeLogin).toHaveBeenCalledTimes(1)
  })

  it('disables Claude 登录 when loginPending is true (debounce)', () => {
    const onClaudeLogin = vi.fn()
    render(
      <ToolCard
        tool={tool({ name: 'claude', version: '2.1.119', path: '/usr/local/bin/claude' })}
        info={info()}
        installing={null}
        loginPending={true}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={onClaudeLogin}
        nodeFound={true}
      />,
    )
    const btn = screen.getByRole('button', { name: 'Claude 登录' }) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    btn.click()
    expect(onClaudeLogin).not.toHaveBeenCalled()
  })

  // Team edition serves the login through a server-side PTY bridged to a browser
  // terminal, so the button IS present there (#677). It only disappears when
  // neither transport is available — personal_mode off AND browser login off,
  // which is the "hosted deployment that can't host a terminal either" case.
  it('does not render Claude 登录 when neither login transport is available', () => {
    render(
      <ToolCard
        tool={tool({ name: 'claude', version: '2.1.119', path: '/usr/local/bin/claude' })}
        info={info({ personal_mode: false, browser_cli_login: false })}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.queryByRole('button', { name: 'Claude 登录' })).toBeNull()
    expect(screen.queryByRole('button', { name: '登录' })).toBeNull()
  })

  // The regression this whole change exists to prevent: before #677 team users
  // saw NO login affordance at all, so an unauthenticated CLI only revealed
  // itself when an agent failed at run time.
  it('renders 登录 in team mode when the browser login transport is available', () => {
    const onClaudeLogin = vi.fn()
    render(
      <ToolCard
        tool={tool({ name: 'claude', version: '2.1.119', path: '/usr/local/bin/claude', logged_in: false })}
        info={info({ personal_mode: false, browser_cli_login: true })}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={onClaudeLogin}
        nodeFound={true}
      />,
    )
    const btn = screen.getByRole('button', { name: '登录' }) as HTMLButtonElement
    expect(btn.disabled).toBe(false)
    btn.click()
    expect(onClaudeLogin).toHaveBeenCalledTimes(1)
    // The unauthenticated state must be visible, not merely actionable.
    expect(screen.getByText('未登录')).toBeTruthy()
  })

  it('shows 已登录 and offers 重新登录 when the CLI already has credentials', () => {
    render(
      <ToolCard
        tool={tool({ name: 'codex', version: 'codex-cli 0.132.0', path: '/usr/local/bin/codex', logged_in: true })}
        info={info({ personal_mode: false, browser_cli_login: true })}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        onCodexLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.getByText('已登录')).toBeTruthy()
    expect(screen.getByRole('button', { name: '重新登录' })).toBeTruthy()
  })

  // Server $HOME is shared, so a member logging in would swap the credentials
  // every other member's agents run under. Gate the action, but keep the status
  // visible so a non-admin can still see WHY their agents are failing.
  it('disables the browser login for non-admins but still shows login status', () => {
    const onClaudeLogin = vi.fn()
    render(
      <ToolCard
        tool={tool({ name: 'claude', version: '2.1.119', path: '/usr/local/bin/claude', logged_in: false })}
        info={info({ personal_mode: false, browser_cli_login: true })}
        installing={null}
        loginPending={false}
        canBrowserLogin={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={onClaudeLogin}
        nodeFound={true}
      />,
    )
    const btn = screen.getByRole('button', { name: '登录' }) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    btn.click()
    expect(onClaudeLogin).not.toHaveBeenCalled()
    expect(screen.getByText('未登录')).toBeTruthy()
  })

  // Login status is only meaningful for CLIs that have a login flow; git/node
  // must not grow a badge just because the field exists on the shared type.
  it('does not show login status for tools without a login flow', () => {
    render(
      <ToolCard
        tool={tool({ name: 'git', version: '2.42.0', path: '/usr/bin/git' })}
        info={info({ personal_mode: false, browser_cli_login: true })}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.queryByText('未登录')).toBeNull()
    expect(screen.queryByText('已登录')).toBeNull()
  })

  it('does not render Claude 登录 when claude is not found', () => {
    render(
      <ToolCard
        tool={tool({ name: 'claude', found: false, version: '', path: '' })}
        info={info()}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.queryByRole('button', { name: 'Claude 登录' })).toBeNull()
  })

  it('does not render Claude 登录 for non-claude tools even when found', () => {
    render(
      <ToolCard
        tool={tool({ name: 'git', version: '2.42.0', path: '/usr/bin/git' })}
        info={info()}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.queryByRole('button', { name: 'Claude 登录' })).toBeNull()
  })

  it('shows Codex 登录 when codex is found in personal mode and triggers callback', () => {
    const onCodexLogin = vi.fn()
    render(
      <ToolCard
        tool={tool({ name: 'codex', version: 'codex-cli 0.132.0', path: '/usr/local/bin/codex' })}
        info={info()}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        onCodexLogin={onCodexLogin}
        nodeFound={true}
      />,
    )
    const btn = screen.getByRole('button', { name: 'Codex 登录' }) as HTMLButtonElement
    expect(btn).toBeTruthy()
    btn.click()
    expect(onCodexLogin).toHaveBeenCalledTimes(1)
  })

  it('does not render Codex 登录 when neither login transport is available', () => {
    render(
      <ToolCard
        tool={tool({ name: 'codex', version: 'codex-cli 0.132.0', path: '/usr/local/bin/codex' })}
        info={info({ personal_mode: false, browser_cli_login: false })}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        onCodexLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.queryByRole('button', { name: 'Codex 登录' })).toBeNull()
    expect(screen.queryByRole('button', { name: '登录' })).toBeNull()
  })

  it('disables 一键安装 for codex when node is missing', () => {
    const onInstall = vi.fn()
    render(
      <ToolCard
        tool={tool({ name: 'codex', found: false, version: '', path: '' })}
        info={info()}
        installing={null}
        loginPending={false}
        onInstall={onInstall}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        onCodexLogin={() => {}}
        nodeFound={false}
      />,
    )
    const btn = screen.getByRole('button', { name: '一键安装' }) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    btn.click()
    expect(onInstall).not.toHaveBeenCalled()
  })

  // ffmpeg is an optional in-app download (spec §7.3), not a package-manager
  // package: the row must carry the localized label/descriptor, the download
  // copy and the resume affordance, and must never show a bare `将运行：`
  // command preview (commandFor returns '' for it so the OS-PM tables — which
  // have no ffmpeg entry — can't render an empty package name).
  it('renders the ffmpeg row with the download copy, resume hint and manual fallback', () => {
    render(
      <ToolCard
        tool={ffmpeg()}
        info={info()}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.getByText('FFmpeg（视频合成）')).toBeTruthy()
    expect(screen.getByText('应用内下载（约 160 MB，支持断点续传）')).toBeTruthy()
    expect(screen.getByText('下载中断后无需重新开始，再次点击将从中断处继续。')).toBeTruthy()
    expect(screen.queryByText(/将运行：/)).toBeNull()
    const link = screen.getByText('手动下载 →') as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('https://ffmpeg.org/download.html')
  })

  it('offers a download button (not 一键安装) for ffmpeg when missing and installable', () => {
    const onInstall = vi.fn()
    render(
      <ToolCard
        tool={ffmpeg()}
        info={info()}
        installing={null}
        loginPending={false}
        onInstall={onInstall}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.queryByRole('button', { name: '一键安装' })).toBeNull()
    const btn = screen.getByRole('button', { name: '下载安装' }) as HTMLButtonElement
    btn.click()
    expect(onInstall).toHaveBeenCalledTimes(1)
  })

  it('shows 下载中… and blocks recheck while the ffmpeg download is in flight', () => {
    render(
      <ToolCard
        tool={ffmpeg()}
        info={info()}
        installing={'ffmpeg'}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    const btn = screen.getByRole('button', { name: '下载中…' }) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    // The other tools' label must not leak into the ffmpeg row.
    expect(screen.queryByRole('button', { name: '安装中…' })).toBeNull()
    const recheck = screen.getByRole('button', { name: '重新检测' }) as HTMLButtonElement
    expect(recheck.disabled).toBe(true)
  })

  it('shows version and 重新检测 once ffmpeg is detected', () => {
    render(
      <ToolCard
        tool={ffmpeg({
          found: true,
          version: 'ffmpeg version 7.1-static',
          path: '/home/u/.niuniu/bin/ffmpeg/linux-amd64/ffmpeg',
        })}
        info={info()}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.getByText('ffmpeg version 7.1-static')).toBeTruthy()
    expect(screen.getByText('/home/u/.niuniu/bin/ffmpeg/linux-amd64/ffmpeg')).toBeTruthy()
    expect(screen.getByRole('button', { name: '重新检测' })).toBeTruthy()
    // Found rows must not advertise an install action.
    expect(screen.queryByRole('button', { name: '下载安装' })).toBeNull()
  })

  // A team-edition / no-download row still links the manual fallback, but must
  // not promise an in-app download path or a resume behaviour that isn't there.
  it('hides the in-app download copy and resume hint when ffmpeg is not installable', () => {
    render(
      <ToolCard
        tool={ffmpeg({ installable: false })}
        info={info({ can_install: false, package_manager: '' })}
        installing={null}
        loginPending={false}
        onInstall={() => {}}
        onRefresh={() => {}}
        onClaudeLogin={() => {}}
        nodeFound={true}
      />,
    )
    expect(screen.queryByRole('button', { name: '下载安装' })).toBeNull()
    expect(screen.queryByText('应用内下载（约 160 MB，支持断点续传）')).toBeNull()
    expect(screen.queryByText('下载中断后无需重新开始，再次点击将从中断处继续。')).toBeNull()
    expect(screen.getByText('手动下载 →')).toBeTruthy()
  })
})

// The install SSE stream is shared machinery, but ffmpeg is the first tool
// whose stream carries download progress ("[niuniu] 进度 42% …") instead of
// package-manager output — the page must render those lines verbatim and
// re-probe when the job finishes.
class FakeEventSource {
  static instances: FakeEventSource[] = []
  onmessage: ((ev: MessageEvent) => unknown) | null = null
  onerror: ((ev: Event) => unknown) | null = null
  closed = false
  url: string
  constructor(url: string) {
    this.url = url
    FakeEventSource.instances.push(this)
  }
  close() { this.closed = true }
  emit(payload: unknown) {
    this.onmessage?.({ data: JSON.stringify(payload) } as MessageEvent)
  }
}

describe('SystemDepsSettings · ffmpeg in-app download stream', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    FakeEventSource.instances = []
  })

  it('renders streamed download progress lines and re-detects when the job is done', async () => {
    vi.mocked(api.getSystemDeps)
      .mockResolvedValueOnce(info({ tools: [ffmpeg()] }))
      .mockResolvedValue(info({
        tools: [ffmpeg({ found: true, version: 'ffmpeg version 7.1-static' })],
      }))
    vi.mocked(api.startSystemDepsInstall).mockResolvedValue({ job_id: 'job-ffmpeg-1' })
    vi.stubGlobal('EventSource', FakeEventSource)

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(<QueryClientProvider client={qc}><SystemDepsSettings /></QueryClientProvider>)

    const btn = await screen.findByRole('button', { name: '下载安装' })
    fireEvent.click(btn)
    await waitFor(() => expect(api.startSystemDepsInstall).toHaveBeenCalledWith('ffmpeg'))
    const es = FakeEventSource.instances[0]
    expect(es.url).toContain('job-ffmpeg-1')
    await waitFor(() => expect(screen.getByRole('button', { name: '下载中…' })).toBeTruthy())

    act(() => { es.emit({ line: '[niuniu] 进度 42% (67.2/160.0 MB, 2.3 MB/s)' }) })
    expect(await screen.findByText('[niuniu] 进度 42% (67.2/160.0 MB, 2.3 MB/s)')).toBeTruthy()
    expect(screen.getByRole('log')).toBeTruthy()

    act(() => { es.emit({ done: true, exit_code: 0 }) })
    expect(await screen.findByText('--- 完成（exit code 0）---')).toBeTruthy()
    await waitFor(() => expect(api.getSystemDeps).toHaveBeenCalledTimes(2))
    // The fresh probe flips the row to its installed state.
    expect(await screen.findByText('ffmpeg version 7.1-static')).toBeTruthy()
    expect(screen.queryByRole('button', { name: '下载中…' })).toBeNull()
  })
})
