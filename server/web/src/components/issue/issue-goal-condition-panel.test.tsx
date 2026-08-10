import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { IssueGoalConditionPanel } from './issue-goal-condition-panel'
import { apiFetch } from '@/lib/api'

// The default-expand-vs-collapse state machine is the load-bearing UX bit of
// this panel: the AI suggest button + textarea live inside the expanded view,
// so the discoverability of the feature depends entirely on which state the
// panel boots into. The four test cases below pin every branch of the
// useState initializer.

const noopSave = async () => {}

// vi.mock the api module so AI-suggest tests can stub the network round-
// trip without spinning a real backend. `apiFetch` is the named export the
// panel imports; we cast through unknown to keep TS happy on the mock shape.
vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api')
  return {
    ...actual,
    apiFetch: vi.fn(),
  }
})
const mockedApiFetch = vi.mocked(apiFetch)

describe('IssueGoalConditionPanel default open state', () => {
  beforeEach(() => {
    // src/i18n/test-setup.ts's afterEach already does this, but be explicit
    // so tests are robust to ordering changes.
    window.localStorage.clear()
    vi.clearAllMocks()
  })

  it('expands by default when condition is unset and no user preference is stored', () => {
    render(
      <IssueGoalConditionPanel issueId={42} initialCondition="" onSave={noopSave} />,
    )
    // <Textarea> is rendered only inside the expanded view.
    expect(screen.queryByRole('textbox')).toBeInTheDocument()
  })

  it('collapses by default when a condition is already saved (returning user)', () => {
    render(
      <IssueGoalConditionPanel
        issueId={43}
        initialCondition="all tests pass"
        onSave={noopSave}
      />,
    )
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  })

  it("respects an explicit user preference of '0' (collapsed) even when condition is unset", () => {
    window.localStorage.setItem('issue-goal-cond-open-44', '0')
    render(
      <IssueGoalConditionPanel issueId={44} initialCondition="" onSave={noopSave} />,
    )
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  })

  it("respects an explicit user preference of '1' (expanded) even when condition is set", () => {
    window.localStorage.setItem('issue-goal-cond-open-45', '1')
    render(
      <IssueGoalConditionPanel
        issueId={45}
        initialCondition="PR is merge-ready"
        onSave={noopSave}
      />,
    )
    expect(screen.queryByRole('textbox')).toBeInTheDocument()
  })

  it('shows "已设置" label when initialCondition is non-empty (regression: server returned the value but UI used the wrong field)', () => {
    // Regression: 2026-05-15 backend bug — IssueDetail's loadIssueDetail
    // struct literal forgot to copy GoalCondition from the DB row, so
    // initialCondition always came down as "" even when DB had a value.
    // The frontend can't fix that, but a "set" label sanity check here
    // catches if anyone ever forgets the prop wiring on the SPA side.
    window.localStorage.setItem('issue-goal-cond-open-99', '0')
    render(
      <IssueGoalConditionPanel
        issueId={99}
        initialCondition="all unit tests pass"
        onSave={noopSave}
      />,
    )
    // Header chip should read "已设置" (or i18n equivalent) — not "未设置".
    expect(screen.getByText(/已设置/)).toBeInTheDocument()
    expect(screen.queryByText(/未设置/)).not.toBeInTheDocument()
  })
})

describe('IssueGoalConditionPanel dirty-mode after AI suggest', () => {
  beforeEach(() => {
    window.localStorage.clear()
    vi.clearAllMocks()
  })

  it('clicking AI suggest enters dirty mode: shows 保存 + 丢弃 buttons + 未保存 badge', async () => {
    mockedApiFetch.mockResolvedValueOnce({
      data: { suggestion: 'AI-generated criterion' },
    } as never)
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(
      <IssueGoalConditionPanel issueId={101} initialCondition="" onSave={onSave} />,
    )

    fireEvent.click(screen.getByRole('button', { name: /AI 建议/ }))

    // Textarea filled with the AI suggestion.
    await waitFor(() => {
      const ta = screen.getByRole('textbox') as HTMLTextAreaElement
      expect(ta.value).toBe('AI-generated criterion')
    })
    // Save + Discard buttons + Unsaved badge appear; no onSave call yet.
    expect(screen.getByRole('button', { name: /保存/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /丢弃/ })).toBeInTheDocument()
    expect(screen.getByText(/未保存/)).toBeInTheDocument()
    expect(onSave).not.toHaveBeenCalled()
  })

  it('blur in dirty mode does NOT autosave (user must explicitly click 保存)', async () => {
    mockedApiFetch.mockResolvedValueOnce({
      data: { suggestion: 'pending review value' },
    } as never)
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(
      <IssueGoalConditionPanel issueId={102} initialCondition="" onSave={onSave} />,
    )

    fireEvent.click(screen.getByRole('button', { name: /AI 建议/ }))
    await waitFor(() => {
      expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('pending review value')
    })

    fireEvent.blur(screen.getByRole('textbox'))
    // Give any latent async chain a tick to fire.
    await new Promise((r) => setTimeout(r, 0))
    expect(onSave).not.toHaveBeenCalled()
  })

  it('clicking 保存 commits via onSave and exits dirty mode', async () => {
    mockedApiFetch.mockResolvedValueOnce({
      data: { suggestion: 'commit me' },
    } as never)
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(
      <IssueGoalConditionPanel issueId={103} initialCondition="" onSave={onSave} />,
    )

    fireEvent.click(screen.getByRole('button', { name: /AI 建议/ }))
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /保存/ })).toBeInTheDocument()
    })

    fireEvent.click(screen.getByRole('button', { name: /保存/ }))
    await waitFor(() => {
      expect(onSave).toHaveBeenCalledWith('commit me')
    })
    // Dirty UI cleared after save resolves.
    await waitFor(() => {
      expect(screen.queryByText(/未保存/)).not.toBeInTheDocument()
      expect(screen.queryByRole('button', { name: /^保存$/ })).not.toBeInTheDocument()
      expect(screen.queryByRole('button', { name: /丢弃/ })).not.toBeInTheDocument()
    })
  })

  it('clicking 丢弃 reverts textarea to initialCondition and exits dirty mode (no save)', async () => {
    mockedApiFetch.mockResolvedValueOnce({
      data: { suggestion: 'discard me' },
    } as never)
    const onSave = vi.fn().mockResolvedValue(undefined)
    // Seed localStorage BEFORE render so the panel boots expanded
    // (otherwise initialCondition="original" → default-collapsed branch).
    window.localStorage.setItem('issue-goal-cond-open-104', '1')

    render(
      <IssueGoalConditionPanel
        issueId={104}
        initialCondition="original"
        onSave={onSave}
      />,
    )

    fireEvent.click(screen.getByRole('button', { name: /AI 建议/ }))
    await waitFor(() => {
      expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('discard me')
    })

    fireEvent.click(screen.getByRole('button', { name: /丢弃/ }))
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).value).toBe('original')
    expect(onSave).not.toHaveBeenCalled()
    expect(screen.queryByText(/未保存/)).not.toBeInTheDocument()
  })

  it('manual edit (no AI suggest) still autosaves onBlur — existing behavior preserved', async () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    render(
      <IssueGoalConditionPanel issueId={105} initialCondition="" onSave={onSave} />,
    )

    const ta = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.change(ta, { target: { value: 'typed by hand' } })
    fireEvent.blur(ta)
    await waitFor(() => {
      expect(onSave).toHaveBeenCalledWith('typed by hand')
    })
    // No save/discard buttons should appear for the manual path.
    expect(screen.queryByRole('button', { name: /^保存$/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /丢弃/ })).not.toBeInTheDocument()
  })
})
