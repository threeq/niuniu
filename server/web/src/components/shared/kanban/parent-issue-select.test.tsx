import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, within } from '@testing-library/react'
import '@/i18n'
import { ParentIssueSelect } from './parent-issue-select'
import type { Issue } from '@/types/api'

const ISSUES = [
  { id: 101, title: '登录功能' },
  { id: 202, title: '支付功能' },
  { id: 303, title: '搜索优化' },
] as unknown as Issue[]

function renderSelect(props: Partial<React.ComponentProps<typeof ParentIssueSelect>> = {}) {
  const onChange = vi.fn()
  render(
    <ParentIssueSelect
      issues={ISSUES}
      value={null}
      onChange={onChange}
      noneLabel="无（顶层）"
      data-testid="parent-select"
      {...props}
    />,
  )
  return { onChange }
}

function openListbox() {
  fireEvent.click(screen.getByTestId('parent-select'))
}

/** The popover content (search box + option list), excluding the trigger button
 *  which also renders the none label when nothing is selected. */
function panel() {
  const search = screen.getByPlaceholderText('搜索标题或 #ID...')
  return search.closest('div')!.parentElement as HTMLElement
}

describe('ParentIssueSelect', () => {
  beforeEach(() => vi.clearAllMocks())

  it('shows each option with its #id and the none option', () => {
    renderSelect()
    openListbox()
    const p = within(panel())
    expect(p.getByText('无（顶层）')).toBeTruthy()
    expect(p.getByText('#101')).toBeTruthy()
    expect(p.getByText('登录功能')).toBeTruthy()
    expect(p.getByText('#202')).toBeTruthy()
  })

  it('filters by title text', () => {
    renderSelect()
    openListbox()
    fireEvent.change(screen.getByPlaceholderText('搜索标题或 #ID...'), { target: { value: '支付' } })
    const p = within(panel())
    expect(p.getByText('支付功能')).toBeTruthy()
    expect(p.queryByText('登录功能')).toBeNull()
    expect(p.queryByText('搜索优化')).toBeNull()
  })

  it('filters by id substring', () => {
    renderSelect()
    openListbox()
    fireEvent.change(screen.getByPlaceholderText('搜索标题或 #ID...'), { target: { value: '202' } })
    const p = within(panel())
    expect(p.getByText('支付功能')).toBeTruthy()
    expect(p.queryByText('登录功能')).toBeNull()
  })

  it('emits the chosen id on select', () => {
    const { onChange } = renderSelect()
    openListbox()
    fireEvent.click(within(panel()).getByText('搜索优化'))
    expect(onChange).toHaveBeenCalledWith(303)
  })

  it('emits null when the none option is chosen', () => {
    const { onChange } = renderSelect({ value: 101 })
    openListbox()
    fireEvent.click(within(panel()).getByText('无（顶层）'))
    expect(onChange).toHaveBeenCalledWith(null)
  })

  it('shows a no-results message when nothing matches', () => {
    renderSelect()
    openListbox()
    fireEvent.change(screen.getByPlaceholderText('搜索标题或 #ID...'), { target: { value: 'zzz' } })
    expect(within(panel()).getByText('无匹配的 Issue')).toBeTruthy()
  })

  it('hides the none option when allowNone is false', () => {
    renderSelect({ allowNone: false })
    openListbox()
    expect(within(panel()).queryByText('无（顶层）')).toBeNull()
  })
})
