import { describe, expect, it } from 'vitest'
import { formatActivity } from './activity-format'

describe('formatActivity', () => {
  it('returns empty for undefined', () => {
    expect(formatActivity(undefined)).toBe('')
  })
  it('formats tool_use with tool name', () => {
    expect(formatActivity({ kind: 'tool_use', tool_name: 'Bash', occurred_at: '' })).toBe('Running: Bash')
  })
  it('falls back to "tool" when tool_name missing', () => {
    expect(formatActivity({ kind: 'tool_use', occurred_at: '' })).toBe('Running: tool')
  })
  it('text uses preview with ellipsis', () => {
    expect(formatActivity({ kind: 'text', text_preview: 'hi', occurred_at: '' })).toBe('hi…')
  })
  it('thinking is constant', () => {
    expect(formatActivity({ kind: 'thinking', occurred_at: '' })).toBe('Thinking…')
  })
  it('inbox_sent with target', () => {
    expect(formatActivity({ kind: 'inbox_sent', target: 'bob', occurred_at: '' })).toBe('→ inbox to bob')
  })
})
