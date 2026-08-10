import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { ToolCard } from './system-deps-settings'
import { openExternalSmart } from '@/lib/shell'
import type { SystemDepsInfo, ToolStatus } from '@/types/api'

vi.mock('@/lib/shell', () => ({
  openClaudeLoginTerminal: vi.fn(),
  openCodexLoginTerminal: vi.fn(),
  openExternalSmart: vi.fn(() => Promise.resolve()),
}))

function info(over: Partial<SystemDepsInfo> = {}): SystemDepsInfo {
  return {
    platform: 'darwin',
    package_manager: 'brew',
    can_install: true,
    personal_mode: true,
    tools: [],
    ...over,
  }
}

function tool(over: Partial<ToolStatus> = {}): ToolStatus {
  return { name: 'node', found: true, version: 'v20.11.0', path: '/usr/bin/node', installable: true, ...over }
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

  it('does not render Claude 登录 in team mode (personal_mode=false)', () => {
    render(
      <ToolCard
        tool={tool({ name: 'claude', version: '2.1.119', path: '/usr/local/bin/claude' })}
        info={info({ personal_mode: false })}
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

  it('does not render Codex 登录 in team mode (personal_mode=false)', () => {
    render(
      <ToolCard
        tool={tool({ name: 'codex', version: 'codex-cli 0.132.0', path: '/usr/local/bin/codex' })}
        info={info({ personal_mode: false })}
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
})
