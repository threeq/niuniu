import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { IntakeSetting } from '@/lib/intake-api'
import { SourceCard } from './source-card'
import { draftFromSettings } from './intake-utils'

vi.mock('@tanstack/react-router', () => ({
  Link: ({
    children,
    to,
  }: {
    children: React.ReactNode
    to?: string
    search?: Record<string, unknown>
  }) => <a href={to}>{children}</a>,
}))

const source: IntakeSetting = {
  source_kind: 'im_bot',
  enabled: true,
  mode: 'observe',
  daily_cap: 4,
  allow_list: ['ou_alice'],
  bot_list: [],
}

function renderCard(over: Partial<React.ComponentProps<typeof SourceCard>> = {}) {
  const onEnabledChange = vi.fn()
  const onOpenConfig = vi.fn()
  const onConnect = vi.fn()
  render(
    <SourceCard
      projectId={1}
      source={source}
      draft={draftFromSettings([source]).im_bot}
      connected
      canConnect
      pending={2}
      ruleCount={3}
      onEnabledChange={onEnabledChange}
      onOpenConfig={onOpenConfig}
      onConnect={onConnect}
      {...over}
    />,
  )
  return { onEnabledChange, onOpenConfig, onConnect }
}

describe('SourceCard', () => {
  it('shows no switch for an unconnected source', () => {
    renderCard({ connected: false })
    expect(screen.getByText('未连接')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '接入' })).toBeInTheDocument()
    expect(screen.queryByRole('group', { name: '来源开关' })).not.toBeInTheDocument()
  })

  it('toggles the source on and off', () => {
    const { onEnabledChange } = renderCard()
    const off = screen.getByRole('button', { name: '关闭' })
    const on = screen.getByRole('button', { name: '启用' })
    expect(on).toHaveAttribute('aria-pressed', 'true')

    fireEvent.click(off)
    expect(onEnabledChange).toHaveBeenCalledWith(false)

    fireEvent.click(on)
    expect(onEnabledChange).toHaveBeenCalledWith(true)
  })

  it('explains that IM chats carry their own intake mode', () => {
    renderCard()
    expect(screen.getByText(/每个 IM 会话/)).toBeInTheDocument()
  })

  it('summarises cap, rules and allowed members', () => {
    renderCard()
    expect(screen.getByText(/每天最多 4 个/)).toBeInTheDocument()
    expect(screen.getByText(/3 条规则/)).toBeInTheDocument()
    expect(screen.getByText(/1 位允许成员/)).toBeInTheDocument()
  })
})
