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
  mode: 'observe',
  daily_cap: 4,
  allow_list: ['ou_alice'],
  bot_list: [],
}

function renderCard(over: Partial<React.ComponentProps<typeof SourceCard>> = {}) {
  const onModeChange = vi.fn()
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
      isMobile={false}
      onModeChange={onModeChange}
      onOpenConfig={onOpenConfig}
      onConnect={onConnect}
      {...over}
    />,
  )
  return { onModeChange, onOpenConfig, onConnect }
}

describe('SourceCard', () => {
  it('shows no mode control for an unconnected source', () => {
    renderCard({ connected: false })
    expect(screen.getByText('未连接')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '接入' })).toBeInTheDocument()
    expect(screen.queryByRole('group', { name: '模式' })).not.toBeInTheDocument()
  })

  it('switches mode from the desktop segmented control', () => {
    const { onModeChange } = renderCard()
    const auto = screen.getByRole('button', { name: '自动' })
    expect(auto).toHaveAttribute('aria-pressed', 'false')
    fireEvent.click(auto)
    expect(onModeChange).toHaveBeenCalledWith('auto')
  })

  it('uses a Select on mobile', () => {
    renderCard({ isMobile: true })
    expect(screen.queryByRole('group', { name: '模式' })).not.toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: '模式' })).toBeInTheDocument()
  })

  it('summarises cap, rules and allowed members', () => {
    renderCard()
    expect(screen.getByText(/每天最多 4 个/)).toBeInTheDocument()
    expect(screen.getByText(/3 条规则/)).toBeInTheDocument()
    expect(screen.getByText(/1 位允许成员/)).toBeInTheDocument()
  })
})
