import { describe, it, expect } from 'vitest'
import { formatAcceleratorFromEvent, keyTokenFromEvent } from './accelerator'

type Ev = Parameters<typeof formatAcceleratorFromEvent>[0]

function ev(partial: Partial<Ev>): Ev {
  return {
    code: '',
    key: '',
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    metaKey: false,
    ...partial,
  }
}

describe('keyTokenFromEvent', () => {
  it('maps physical letter/digit/space codes', () => {
    expect(keyTokenFromEvent({ code: 'KeyZ', key: 'z' })).toBe('Z')
    expect(keyTokenFromEvent({ code: 'Digit9', key: '9' })).toBe('9')
    expect(keyTokenFromEvent({ code: 'Space', key: ' ' })).toBe('Space')
  })
  it('returns null for non-bindable keys', () => {
    expect(keyTokenFromEvent({ code: 'F5', key: 'F5' })).toBeNull()
    expect(keyTokenFromEvent({ code: 'Enter', key: 'Enter' })).toBeNull()
  })
})

describe('formatAcceleratorFromEvent', () => {
  it('formats Ctrl+Shift+Z on non-mac', () => {
    expect(
      formatAcceleratorFromEvent(ev({ code: 'KeyZ', key: 'z', ctrlKey: true, shiftKey: true }), false)
    ).toBe('Ctrl+Shift+Z')
  })

  it('uses Cmd/Option token names on mac', () => {
    expect(
      formatAcceleratorFromEvent(ev({ code: 'KeyZ', key: 'z', metaKey: true, shiftKey: true }), true)
    ).toBe('Cmd+Shift+Z')
    expect(
      formatAcceleratorFromEvent(ev({ code: 'KeyA', key: 'a', altKey: true, metaKey: true }), true)
    ).toBe('Cmd+Option+A')
  })

  it('refuses a bare key (no modifier)', () => {
    expect(formatAcceleratorFromEvent(ev({ code: 'KeyZ', key: 'z' }), false)).toBeNull()
  })

  it('waits while only modifiers are held', () => {
    expect(
      formatAcceleratorFromEvent(ev({ code: 'ShiftLeft', key: 'Shift', shiftKey: true }), false)
    ).toBeNull()
  })

  it('de-dups metaKey→Ctrl collision on non-mac', () => {
    expect(
      formatAcceleratorFromEvent(ev({ code: 'KeyZ', key: 'z', ctrlKey: true, metaKey: true }), false)
    ).toBe('Ctrl+Z')
  })
})
