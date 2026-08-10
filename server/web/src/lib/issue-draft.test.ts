import { describe, it, expect, beforeEach } from 'vitest'
import { loadIssueDraft, saveIssueDraft, clearIssueDraft, type IssueDraft } from './issue-draft'

const empty: IssueDraft = { title: '', description: '', priority: 'medium', parentIssueId: null }

describe('issue-draft', () => {
  beforeEach(() => localStorage.clear())

  it('returns null when no draft stored', () => {
    expect(loadIssueDraft(1, 2)).toBeNull()
  })

  it('round-trips a non-empty draft', () => {
    const d: IssueDraft = { title: '加登录', description: '用 JWT', priority: 'high', parentIssueId: 5 }
    saveIssueDraft(1, 2, d)
    expect(loadIssueDraft(1, 2)).toEqual(d)
  })

  it('isolates by project and column', () => {
    saveIssueDraft(1, 2, { ...empty, title: 'A' })
    expect(loadIssueDraft(1, 3)).toBeNull()
    expect(loadIssueDraft(9, 2)).toBeNull()
    expect(loadIssueDraft(1, 2)?.title).toBe('A')
  })

  it('does not persist an empty draft (and clears an existing one)', () => {
    saveIssueDraft(1, 2, { ...empty, title: 'A' })
    saveIssueDraft(1, 2, empty)
    expect(loadIssueDraft(1, 2)).toBeNull()
  })

  it('clear removes the draft', () => {
    saveIssueDraft(1, 2, { ...empty, title: 'A' })
    clearIssueDraft(1, 2)
    expect(loadIssueDraft(1, 2)).toBeNull()
  })
})
